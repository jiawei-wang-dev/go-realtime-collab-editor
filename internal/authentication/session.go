package authentication

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
)

type Session struct {
	UserID, RefreshHash string
	CreatedAt           time.Time
}
type SessionStore interface {
	Create(context.Context, string, Session, time.Duration) error
	Get(context.Context, string) (Session, error)
	Rotate(context.Context, string, string, string, time.Duration) error
	Delete(context.Context, string) error
	Close() error
}

type IdempotentSessionStore interface {
	RotateReplay(ctx context.Context, sessionID, oldHash, newHash, requestID, encryptedResult string, sessionTTL, replayTTL time.Duration) (storedResult, storedNewHash string, replayed bool, err error)
}

// DistributedSessionStore provides cross-node logout notifications, operation
// leases, and presence. Pub/Sub only accelerates connection closure; the
// session key remains authoritative for authentication.
type DistributedSessionStore interface {
	Revoke(context.Context, string) error
	AcquireOperation(context.Context, string, string, time.Duration) (release func(context.Context) error, err error)
	WaitForOperations(context.Context, string) error
	NewRevocationSubscription() RevocationSubscription
	TouchPresence(context.Context, string, string, time.Duration) error
	DeletePresence(context.Context, string, string) error
}

// RevocationSubscription owns one subscription connection, not its underlying
// Redis client. Service closes it explicitly to unblock network reads.
type RevocationSubscription interface {
	Run(context.Context, func(string), func()) error
	Close() error
}
type MemorySessions struct {
	mu       sync.Mutex
	sessions map[string]memorySession
	replays  map[string]memoryReplay
	now      func() time.Time
}
type memorySession struct {
	Session
	expires time.Time
}
type memoryReplay struct {
	oldHash, newHash, encrypted string
	expires                     time.Time
}

func NewMemorySessions() *MemorySessions {
	return &MemorySessions{sessions: map[string]memorySession{}, replays: map[string]memoryReplay{}, now: time.Now}
}
func (m *MemorySessions) Create(ctx context.Context, id string, session Session, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[id] = memorySession{Session: session, expires: m.now().Add(ttl)}
	return nil
}
func (m *MemorySessions) Get(ctx context.Context, id string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || !m.now().Before(s.expires) {
		delete(m.sessions, id)
		return Session{}, ErrSessionInvalid
	}
	return s.Session, nil
}
func (m *MemorySessions) Rotate(ctx context.Context, id, oldHash, newHash string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || !m.now().Before(s.expires) || subtle.ConstantTimeCompare([]byte(s.RefreshHash), []byte(oldHash)) != 1 {
		return ErrInvalidRefreshToken
	}
	s.RefreshHash = newHash
	s.expires = m.now().Add(ttl)
	m.sessions[id] = s
	return nil
}

func (m *MemorySessions) RotateReplay(ctx context.Context, id, oldHash, newHash, requestID, encrypted string, sessionTTL, replayTTL time.Duration) (string, string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", "", false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	session, ok := m.sessions[id]
	if !ok || !now.Before(session.expires) {
		delete(m.sessions, id)
		return "", "", false, ErrInvalidRefreshToken
	}
	key := id + "\x00" + requestID
	if replay, exists := m.replays[key]; exists && now.Before(replay.expires) {
		if replay.oldHash != oldHash || session.RefreshHash != replay.newHash {
			return "", "", false, ErrInvalidRefreshToken
		}
		return replay.encrypted, replay.newHash, true, nil
	}
	if subtle.ConstantTimeCompare([]byte(session.RefreshHash), []byte(oldHash)) != 1 {
		return "", "", false, ErrInvalidRefreshToken
	}
	session.RefreshHash = newHash
	session.expires = now.Add(sessionTTL)
	m.sessions[id] = session
	m.replays[key] = memoryReplay{oldHash: oldHash, newHash: newHash, encrypted: encrypted, expires: now.Add(replayTTL)}
	return encrypted, newHash, false, nil
}
func (m *MemorySessions) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	return nil
}
func (*MemorySessions) Close() error { return nil }

type RedisSessions struct {
	client   *redis.Client
	prefix   string
	observer Observer
}

const (
	revocationChannel = "auth:session_revoked"
	operationPrefix   = "auth:session_operation:"
	presencePrefix    = "presence:session:"
)

func NewRedisSessions(ctx context.Context, options *redis.Options, observers ...Observer) (*RedisSessions, error) {
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	var observer Observer
	if len(observers) > 0 {
		observer = observers[0]
	}
	return &RedisSessions{client: client, prefix: "auth:session:", observer: observer}, nil
}
func (r *RedisSessions) Create(ctx context.Context, id string, s Session, ttl time.Duration) error {
	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, r.prefix+id, map[string]any{"user_id": s.UserID, "refresh_token_hash": s.RefreshHash, "created_at": s.CreatedAt.UTC().Format(time.RFC3339Nano)})
	pipe.PExpire(ctx, r.prefix+id, ttl)
	_, err := pipe.Exec(ctx)
	return err
}
func (r *RedisSessions) Get(ctx context.Context, id string) (Session, error) {
	values, err := r.client.HGetAll(ctx, r.prefix+id).Result()
	if err != nil {
		return Session{}, err
	}
	if len(values) == 0 {
		return Session{}, ErrSessionInvalid
	}
	created, err := time.Parse(time.RFC3339Nano, values["created_at"])
	if err != nil {
		return Session{}, ErrSessionInvalid
	}
	return Session{UserID: values["user_id"], RefreshHash: values["refresh_token_hash"], CreatedAt: created}, nil
}

var rotateScript = redis.NewScript(`local current=redis.call('HGET',KEYS[1],'refresh_token_hash'); if not current or current~=ARGV[1] then return 0 end; redis.call('HSET',KEYS[1],'refresh_token_hash',ARGV[2]); redis.call('PEXPIRE',KEYS[1],ARGV[3]); return 1`)

var rotateReplayScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return {0, '', ''} end
local current=redis.call('HGET',KEYS[1],'refresh_token_hash')
if redis.call('EXISTS', KEYS[2]) == 1 then
  local replay_old=redis.call('HGET',KEYS[2],'old_hash')
  local replay_new=redis.call('HGET',KEYS[2],'new_hash')
  if replay_old~=ARGV[1] or current~=replay_new then return {0, '', ''} end
  return {2, redis.call('HGET',KEYS[2],'encrypted_result'), replay_new}
end
if current~=ARGV[1] then return {0, '', ''} end
redis.call('HSET',KEYS[1],'refresh_token_hash',ARGV[2])
redis.call('PEXPIRE',KEYS[1],ARGV[4])
redis.call('HSET',KEYS[2],'old_hash',ARGV[1],'new_hash',ARGV[2],'encrypted_result',ARGV[3])
redis.call('PEXPIRE',KEYS[2],ARGV[5])
return {1, ARGV[3], ARGV[2]}
`)

func (r *RedisSessions) Rotate(ctx context.Context, id, oldHash, newHash string, ttl time.Duration) error {
	result, err := rotateScript.Run(ctx, r.client, []string{r.prefix + id}, oldHash, newHash, ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	switch result {
	case 0:
		return ErrInvalidRefreshToken
	case 1:
		return nil
	default:
		return fmt.Errorf("unexpected Redis rotate result status %d", result)
	}
}

func (r *RedisSessions) RotateReplay(ctx context.Context, id, oldHash, newHash, requestID, encrypted string, sessionTTL, replayTTL time.Duration) (string, string, bool, error) {
	result, err := rotateReplayScript.Run(ctx, r.client, []string{r.prefix + id, "auth:refresh_replay:" + id + ":" + requestID}, oldHash, newHash, encrypted, sessionTTL.Milliseconds(), replayTTL.Milliseconds()).Slice()
	if err != nil {
		return "", "", false, err
	}
	return parseRotateReplayResult(result)
}

func parseRotateReplayResult(result []any) (string, string, bool, error) {
	if len(result) != 3 {
		return "", "", false, fmt.Errorf("unexpected Redis rotate replay result length %d, want 3", len(result))
	}
	status, ok := result[0].(int64)
	if !ok {
		return "", "", false, fmt.Errorf("unexpected Redis rotate replay status type %T", result[0])
	}
	if status == 0 {
		return "", "", false, ErrInvalidRefreshToken
	}
	if status != 1 && status != 2 {
		return "", "", false, fmt.Errorf("unexpected Redis rotate replay status %d", status)
	}
	stored, ok := result[1].(string)
	if !ok {
		return "", "", false, fmt.Errorf("unexpected Redis rotate replay payload type %T", result[1])
	}
	if stored == "" {
		return "", "", false, fmt.Errorf("unexpected empty Redis rotate replay payload")
	}
	storedNewHash, ok := result[2].(string)
	if !ok {
		return "", "", false, fmt.Errorf("unexpected Redis rotate replay hash type %T", result[2])
	}
	if storedNewHash == "" {
		return "", "", false, fmt.Errorf("unexpected empty Redis rotate replay hash")
	}
	return stored, storedNewHash, status == 2, nil
}
func (r *RedisSessions) Delete(ctx context.Context, id string) error {
	return r.Revoke(ctx, id)
}
func (r *RedisSessions) Close() error { return r.client.Close() }

func (r *RedisSessions) Ping(ctx context.Context) error {
	ctx, span := otel.Tracer("collab-editor/redis").Start(ctx, "redis.ping")
	defer span.End()
	started := time.Now()
	err := r.client.Ping(ctx).Err()
	r.observe("ping", started, err)
	return err
}

var revokeScript = redis.NewScript(`
local removed=redis.call('DEL',KEYS[1])
redis.call('PUBLISH',ARGV[1],ARGV[2])
return removed
`)

var rateLimitScript = redis.NewScript(`
local current=redis.call('INCR',KEYS[1])
if current==1 then redis.call('PEXPIRE',KEYS[1],ARGV[2]) end
if current>tonumber(ARGV[1]) then return 0 end
return 1
`)

func (r *RedisSessions) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if key == "" || limit <= 0 || window <= 0 {
		return false, errors.New("valid rate limit key, limit and window are required")
	}
	digest := sha256.Sum256([]byte(key))
	started := time.Now()
	result, err := rateLimitScript.Run(ctx, r.client, []string{"rate_limit:" + hex.EncodeToString(digest[:])}, limit, window.Milliseconds()).Int()
	r.observe("rate_limit", started, err)
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (r *RedisSessions) Revoke(ctx context.Context, sessionID string) error {
	ctx, span := otel.Tracer("collab-editor/redis").Start(ctx, "redis.session_revoke")
	defer span.End()
	if sessionID == "" {
		return ErrSessionInvalid
	}
	started := time.Now()
	err := revokeScript.Run(ctx, r.client, []string{r.prefix + sessionID}, revocationChannel, sessionID).Err()
	r.observe("session_revoke", started, err)
	return err
}

func (r *RedisSessions) observe(operation string, started time.Time, err error) {
	if r.observer != nil {
		r.observer.RedisOperation(operation, time.Since(started), err)
	}
}

var acquireOperationScript = redis.NewScript(`
if redis.call('EXISTS',KEYS[1]) == 0 then return 0 end
if redis.call('SET',KEYS[2],ARGV[1],'NX','PX',ARGV[2]) then return 1 end
return 0
`)

var releaseOperationScript = redis.NewScript(`
if redis.call('GET',KEYS[1]) == ARGV[1] then return redis.call('DEL',KEYS[1]) end
return 0
`)

func (r *RedisSessions) AcquireOperation(ctx context.Context, sessionID, nodeID string, ttl time.Duration) (func(context.Context) error, error) {
	if sessionID == "" || nodeID == "" || ttl <= 0 {
		return nil, ErrSessionInvalid
	}
	leaseID, err := randomID()
	if err != nil {
		return nil, err
	}
	key := operationPrefix + sessionID + ":" + nodeID + ":" + leaseID
	result, err := acquireOperationScript.Run(ctx, r.client, []string{r.prefix + sessionID, key}, leaseID, ttl.Milliseconds()).Int()
	if err != nil {
		return nil, err
	}
	if result != 1 {
		return nil, ErrSessionInvalid
	}
	var once sync.Once
	release := func(releaseCtx context.Context) error {
		var releaseErr error
		once.Do(func() {
			releaseErr = releaseOperationScript.Run(releaseCtx, r.client, []string{key}, leaseID).Err()
		})
		return releaseErr
	}
	return release, nil
}

func (r *RedisSessions) WaitForOperations(ctx context.Context, sessionID string) error {
	pattern := operationPrefix + sessionID + ":*"
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		keys, err := r.operationKeys(ctx, pattern)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrLogoutIncomplete
		case <-ticker.C:
		}
	}
}

func (r *RedisSessions) operationKeys(ctx context.Context, pattern string) ([]string, error) {
	var cursor uint64
	var keys []string
	for {
		batch, next, err := r.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			return keys, nil
		}
	}
}

func (r *RedisSessions) NewRevocationSubscription() RevocationSubscription {
	return &redisRevocationSubscription{client: r.client}
}

type redisRevocationSubscription struct {
	client *redis.Client
	mu     sync.Mutex
	pubsub *redis.PubSub
	closed bool
}

func (s *redisRevocationSubscription) Run(ctx context.Context, revoked func(string), reconnected func()) error {
	backoff := 50 * time.Millisecond
	for ctx.Err() == nil {
		pubsub, ok := s.open(ctx)
		if !ok {
			break
		}
		if _, err := pubsub.Receive(ctx); err != nil {
			s.release(pubsub)
			if !waitRedisRetry(ctx, backoff) {
				break
			}
			backoff = minDuration(backoff*2, time.Second)
			continue
		}
		backoff = 50 * time.Millisecond
		if reconnected != nil {
			reconnected()
		}
		for ctx.Err() == nil {
			message, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				break
			}
			if message.Channel == revocationChannel && message.Payload != "" && revoked != nil {
				revoked(message.Payload)
			}
		}
		s.release(pubsub)
		if !waitRedisRetry(ctx, backoff) {
			break
		}
		backoff = minDuration(backoff*2, time.Second)
	}
	return context.Cause(ctx)
}

func (s *redisRevocationSubscription) open(ctx context.Context) (*redis.PubSub, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	pubsub := s.client.Subscribe(ctx, revocationChannel)
	s.pubsub = pubsub
	return pubsub, true
}

func (s *redisRevocationSubscription) release(pubsub *redis.PubSub) {
	s.mu.Lock()
	if s.pubsub == pubsub {
		s.pubsub = nil
	}
	s.mu.Unlock()
	_ = pubsub.Close()
}

func (s *redisRevocationSubscription) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pubsub := s.pubsub
	s.pubsub = nil
	s.mu.Unlock()
	if pubsub == nil {
		return nil
	}
	return pubsub.Close()
}

func waitRedisRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func (r *RedisSessions) TouchPresence(ctx context.Context, sessionID, nodeID string, ttl time.Duration) error {
	if sessionID == "" || nodeID == "" || ttl <= 0 {
		return errors.New("session ID, node ID and positive presence TTL are required")
	}
	return r.client.Set(ctx, presencePrefix+sessionID+":"+nodeID, "1", ttl).Err()
}

func (r *RedisSessions) DeletePresence(ctx context.Context, sessionID, nodeID string) error {
	return r.client.Del(ctx, presencePrefix+sessionID+":"+nodeID).Err()
}

func hashRefresh(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
func parseRefresh(token string) (string, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ErrInvalidRefreshToken
	}
	return parts[0], parts[1], nil
}
