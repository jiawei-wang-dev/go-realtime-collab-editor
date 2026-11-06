package authentication

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

var redisTestUsernameSequence atomic.Uint64

func init() {
	redisTestUsernameSequence.Store(uint64(time.Now().UnixNano()))
}

func TestRedisCrossNodeLogoutClosesOnlyRevokedSession(t *testing.T) {
	serviceA, serviceB, store := redisServicePair(t)
	user, pairOne, principalOne := createRedisSession(t, serviceA, "cross-node")
	pairTwo, principalTwo := loginRedisSession(t, serviceA, user.Username)
	_ = pairOne
	_ = pairTwo

	closedOne := make(chan struct{}, 1)
	closedTwo := make(chan struct{}, 1)
	serviceB.Registry().Register(principalOne.SessionID, "one", func() { closedOne <- struct{}{} })
	serviceB.Registry().Register(principalTwo.SessionID, "two", func() { closedTwo <- struct{}{} })
	waitRedisCondition(t, 3*time.Second, func() bool {
		return store.client.Exists(context.Background(), presencePrefix+principalOne.SessionID+":node-b").Val() == 1 &&
			store.client.Exists(context.Background(), presencePrefix+principalTwo.SessionID+":node-b").Val() == 1
	})
	if err := serviceA.Logout(context.Background(), principalOne); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closedOne:
	case <-time.After(3 * time.Second):
		t.Fatal("remote node did not close revoked session")
	}
	select {
	case <-closedTwo:
		t.Fatal("unrelated device session was closed")
	default:
	}
	if err := serviceB.ValidatePrincipal(context.Background(), principalOne); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked principal validation = %v", err)
	}
	if err := serviceB.ValidatePrincipal(context.Background(), principalTwo); err != nil {
		t.Fatalf("unrelated principal validation = %v", err)
	}
	if err := store.Revoke(context.Background(), principalOne.SessionID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closedOne:
		t.Fatal("duplicate revocation closed the connection twice")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRedisLogoutWaitsForRemoteOperationLease(t *testing.T) {
	serviceA, serviceB, _ := redisServicePair(t)
	_, _, principal := createRedisSession(t, serviceA, "operation-lease")
	release, err := serviceB.AcquirePrincipal(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	logout := make(chan error, 1)
	go func() { logout <- serviceA.Logout(context.Background(), principal) }()
	select {
	case err := <-logout:
		t.Fatalf("logout returned while remote operation was active: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-logout:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("logout did not finish after remote operation released")
	}
	if _, err := serviceB.AcquirePrincipal(context.Background(), principal); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("new operation after logout = %v", err)
	}
}

func TestRedisOperationLeaseExpiresAfterNodeCrashAndLogoutTimeoutIsExplicit(t *testing.T) {
	serviceA, _, store := redisServicePair(t)
	_, _, principal := createRedisSession(t, serviceA, "crashed-operation")
	if _, err := store.AcquireOperation(context.Background(), principal.SessionID, "crashed-node", 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := serviceA.Logout(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond {
		t.Fatalf("logout returned before crashed operation lease expired: %s", elapsed)
	}

	_, _, timeoutPrincipal := createRedisSession(t, serviceA, "logout-timeout")
	if _, err := store.AcquireOperation(context.Background(), timeoutPrincipal.SessionID, "stuck-node", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	serviceA.config.LogoutWaitTimeout = 100 * time.Millisecond
	if err := serviceA.Logout(context.Background(), timeoutPrincipal); !errors.Is(err, ErrLogoutIncomplete) {
		t.Fatalf("logout timeout error = %v", err)
	}
}

func TestRedisPubSubLossReconcilesRevokedSessionAndPresenceExpires(t *testing.T) {
	serviceA, serviceB, store := redisServicePair(t)
	_, _, principal := createRedisSession(t, serviceA, "pubsub-recovery")
	closed := make(chan struct{}, 1)
	serviceB.Registry().Register(principal.SessionID, "connection", func() { closed <- struct{}{} })
	waitRedisCondition(t, 3*time.Second, func() bool {
		result, err := store.client.Do(context.Background(), "PUBSUB", "NUMSUB", revocationChannel).Slice()
		return err == nil && len(result) == 2 && result[1].(int64) >= 2
	})
	if err := store.client.Do(context.Background(), "CLIENT", "KILL", "TYPE", "PUBSUB").Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(context.Background(), principal.SessionID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("session was not reconciled after Pub/Sub interruption")
	}

	if err := store.TouchPresence(context.Background(), "crashed-session", "crashed-node", 150*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	waitRedisCondition(t, 2*time.Second, func() bool {
		return store.client.Exists(context.Background(), presencePrefix+"crashed-session:crashed-node").Val() == 0
	})
}

func TestRedisServiceCloseUnblocksRevocationSubscriber(t *testing.T) {
	store := redisStore(t)
	tokens, err := NewTokenManager(JWTConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
		Issuer: "phase86-close", Audience: "phase86-close", AccessTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryUsers(), store, tokens, ServiceConfig{
		RefreshTTL: time.Hour, BcryptCost: bcrypt.MinCost, NodeID: "close-test-node",
	})
	t.Cleanup(func() { _ = service.Close() })
	waitRedisCondition(t, 5*time.Second, func() bool {
		result, err := store.client.Do(context.Background(), "PUBSUB", "NUMSUB", revocationChannel).Slice()
		return err == nil && len(result) == 2 && result[1].(int64) == 1
	})

	closed := make(chan error, 1)
	go func() { closed <- service.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Service.Close did not unblock the Redis revocation subscriber")
	}
	waitRedisCondition(t, 5*time.Second, func() bool {
		result, err := store.client.Do(context.Background(), "PUBSUB", "NUMSUB", revocationChannel).Slice()
		return err == nil && len(result) == 2 && result[1].(int64) == 0
	})
}

func redisServicePair(t *testing.T) (*Service, *Service, *RedisSessions) {
	t.Helper()
	storeA := redisStore(t)
	storeB, err := NewRedisSessions(context.Background(), &redis.Options{
		Addr: os.Getenv("REDIS_TEST_ADDR"), Password: os.Getenv("REDIS_TEST_PASSWORD"), DB: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	users := NewMemoryUsers()
	tokens, err := NewTokenManager(JWTConfig{Secret: []byte("0123456789abcdef0123456789abcdef"), Issuer: "phase86", Audience: "phase86", AccessTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	config := func(nodeID string) ServiceConfig {
		return ServiceConfig{
			RefreshTTL: time.Hour, BcryptCost: bcrypt.MinCost, NodeID: nodeID,
			OperationLeaseTTL: time.Second, LogoutWaitTimeout: 2 * time.Second,
			PresenceTTL: 300 * time.Millisecond, ReconcileInterval: 100 * time.Millisecond,
		}
	}
	serviceA := NewService(users, storeA, tokens, config("node-a"))
	serviceB := NewService(users, storeB, tokens, config("node-b"))
	t.Cleanup(func() {
		_ = serviceA.Close()
		_ = serviceB.Close()
		_ = storeB.Close()
	})
	return serviceA, serviceB, storeA
}

func createRedisSession(t *testing.T, service *Service, username string) (User, TokenPair, Principal) {
	t.Helper()
	user, err := service.Register(context.Background(), uniqueRedisTestUsername(username), "password1")
	if err != nil {
		t.Fatal(err)
	}
	pair, principal := loginRedisSession(t, service, user.Username)
	return user, pair, principal
}

func uniqueRedisTestUsername(prefix string) string {
	suffix := strconv.FormatUint(redisTestUsernameSequence.Add(1), 36)
	maxPrefixLength := 32 - len(suffix) - 1
	if len(prefix) > maxPrefixLength {
		prefix = prefix[:maxPrefixLength]
	}
	return prefix + "-" + suffix
}

func TestUniqueRedisTestUsernameIsValidAndUnique(t *testing.T) {
	prefixes := []string{"operation-lease", "crashed-operation", "logout-timeout", "very-long-test-username-prefix"}
	seen := make(map[string]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		username := uniqueRedisTestUsername(prefix)
		if len(username) < 3 || len(username) > 32 {
			t.Fatalf("generated username length = %d for %q", len(username), username)
		}
		if !usernamePattern.MatchString(username) {
			t.Fatalf("generated username contains unsupported characters: %q", username)
		}
		if _, exists := seen[username]; exists {
			t.Fatalf("generated duplicate username: %q", username)
		}
		seen[username] = struct{}{}
	}

	const concurrentUsernames = 100
	results := make(chan string, concurrentUsernames)
	var wait sync.WaitGroup
	for range concurrentUsernames {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- uniqueRedisTestUsername("concurrent")
		}()
	}
	wait.Wait()
	close(results)
	for username := range results {
		if !usernamePattern.MatchString(username) {
			t.Fatalf("concurrently generated invalid username: %q", username)
		}
		if _, exists := seen[username]; exists {
			t.Fatalf("concurrently generated duplicate username: %q", username)
		}
		seen[username] = struct{}{}
	}
}

func loginRedisSession(t *testing.T, service *Service, username string) (TokenPair, Principal) {
	t.Helper()
	pair, err := service.Login(context.Background(), username, "password1")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.AuthenticateToken(context.Background(), pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return pair, principal
}

func waitRedisCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Redis condition did not become true before timeout")
}

func redisStore(t *testing.T) *RedisSessions {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR is not configured; skipping real Redis integration test")
	}
	store, err := NewRedisSessions(context.Background(), &redis.Options{Addr: addr, Password: os.Getenv("REDIS_TEST_PASSWORD"), DB: 15})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.client.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.client.FlushDB(context.Background()).Err(); _ = store.Close() })
	return store
}
func TestRedisSessionLifecycleAndExpiry(t *testing.T) {
	store := redisStore(t)
	session := Session{UserID: "user", RefreshHash: "old", CreatedAt: time.Now().UTC()}
	if err := store.Create(context.Background(), "sid", session, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "sid"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := store.Get(context.Background(), "sid"); err == nil {
		t.Fatal("expired session returned")
	}
}

func TestRedisRateLimiterIsAtomic(t *testing.T) {
	store := redisStore(t)
	ctx := context.Background()
	for attempt := 1; attempt <= 2; attempt++ {
		allowed, err := store.Allow(ctx, "login:192.0.2.10", 2, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("attempt %d allowed=%t err=%v", attempt, allowed, err)
		}
	}
	allowed, err := store.Allow(ctx, "login:192.0.2.10", 2, time.Minute)
	if err != nil || allowed {
		t.Fatalf("third attempt allowed=%t err=%v", allowed, err)
	}
}
func TestRedisAtomicRefreshAndDelete(t *testing.T) {
	store := redisStore(t)
	if err := store.Create(context.Background(), "sid", Session{UserID: "user", RefreshHash: "old", CreatedAt: time.Now().UTC()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			results <- store.Rotate(context.Background(), "sid", "old", string(rune('a'+i)), time.Minute)
		}(i)
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("rotate successes=%d", success)
	}
	if err := store.Delete(context.Background(), "sid"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "sid"); err == nil {
		t.Fatal("deleted session returned")
	}
}
func TestRedisUnavailableRejectsSessionStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if store, err := NewRedisSessions(ctx, &redis.Options{Addr: "127.0.0.1:1"}); err == nil {
		_ = store.Close()
		t.Fatal("unavailable Redis accepted")
	}
}

func TestRedisIdempotentRotationReplayAndTTL(t *testing.T) {
	store := redisStore(t)
	ctx := context.Background()
	if err := store.Create(ctx, "sid", Session{UserID: "user", RefreshHash: "old", CreatedAt: time.Now().UTC()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	result, newHash, replayed, err := store.RotateReplay(ctx, "sid", "old", "new", "request", "ciphertext", time.Minute, 100*time.Millisecond)
	if err != nil || replayed || result != "ciphertext" || newHash != "new" {
		t.Fatalf("first rotation = %q,%q,%t,%v", result, newHash, replayed, err)
	}
	result, newHash, replayed, err = store.RotateReplay(ctx, "sid", "old", "ignored", "request", "ignored", time.Minute, 100*time.Millisecond)
	if err != nil || !replayed || result != "ciphertext" || newHash != "new" {
		t.Fatalf("replay = %q,%q,%t,%v", result, newHash, replayed, err)
	}
	ttl, err := store.client.PTTL(ctx, "auth:refresh_replay:sid:request").Result()
	if err != nil || ttl <= 0 || ttl > 100*time.Millisecond {
		t.Fatalf("replay TTL = %v, %v", ttl, err)
	}
	if _, _, _, err := store.RotateReplay(ctx, "sid", "old", "other", "different", "cipher", time.Minute, time.Minute); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("different request error = %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, _, _, err := store.RotateReplay(ctx, "sid", "old", "other", "request", "cipher", time.Minute, time.Minute); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("expired replay error = %v", err)
	}
}

func TestRedisRotationRejectsWrongKeyType(t *testing.T) {
	store := redisStore(t)
	ctx := context.Background()
	if err := store.client.Set(ctx, "auth:session:sid", "not-a-hash", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.Rotate(ctx, "sid", "old", "new", time.Minute); err == nil {
		t.Fatal("rotation accepted a non-hash session key")
	}
	if _, _, _, err := store.RotateReplay(ctx, "sid", "old", "new", "request", "ciphertext", time.Minute, time.Minute); err == nil {
		t.Fatal("replay rotation accepted a non-hash session key")
	}
}
