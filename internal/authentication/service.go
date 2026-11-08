package authentication

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type ServiceConfig struct {
	RefreshTTL          time.Duration
	RefreshReplayTTL    time.Duration
	ReplayEncryptionKey []byte
	BcryptCost          int
	NodeID              string
	OperationLeaseTTL   time.Duration
	LogoutWaitTimeout   time.Duration
	PresenceTTL         time.Duration
	ReconcileInterval   time.Duration
	Observer            Observer
}

type Observer interface {
	RedisOperation(operation string, duration time.Duration, err error)
	RedisEvent(event, result string)
}
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}
type Service struct {
	users    UserRepository
	sessions SessionStore
	tokens   *TokenManager
	config   ServiceConfig
	registry *ConnectionRegistry
	gates    sync.Map
	replay   *replayCipher
	ctx      context.Context
	cancel   context.CancelFunc
	workers  sync.WaitGroup

	lifecycleMu            sync.Mutex
	closing                bool
	revocationSubscription RevocationSubscription
	closeOnce              sync.Once
	closeErr               error
}

type sessionGate struct{ sync.RWMutex }

var validRefreshRequestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func NewService(users UserRepository, sessions SessionStore, tokens *TokenManager, config ServiceConfig) *Service {
	if config.BcryptCost == 0 {
		config.BcryptCost = bcrypt.DefaultCost
	}
	if config.RefreshReplayTTL <= 0 {
		config.RefreshReplayTTL = 10 * time.Minute
	}
	if config.OperationLeaseTTL <= 0 {
		config.OperationLeaseTTL = 30 * time.Second
	}
	if config.LogoutWaitTimeout <= 0 {
		config.LogoutWaitTimeout = 10 * time.Second
	}
	if config.PresenceTTL <= 0 {
		config.PresenceTTL = 30 * time.Second
	}
	if config.ReconcileInterval <= 0 {
		config.ReconcileInterval = 5 * time.Second
	}
	key := config.ReplayEncryptionKey
	if len(key) != 32 {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	replay, _ := newReplayCipher(key)
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{users: users, sessions: sessions, tokens: tokens, config: config, replay: replay, ctx: ctx, cancel: cancel}
	service.registry = newCoordinatedConnectionRegistry(ctx, distributedStore(sessions), config.NodeID, config.PresenceTTL, config.Observer)
	service.startRevocationListener()
	return service
}
func (s *Service) Register(ctx context.Context, username, password string) (User, error) {
	if err := ValidateUsername(username); err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.config.BcryptCost)
	if err != nil {
		return User{}, err
	}
	id, err := randomID()
	if err != nil {
		return User{}, err
	}
	user := User{ID: id, Username: username, PasswordHash: string(hash), Status: UserActive}
	if err := s.users.CreateUser(ctx, user); err != nil {
		return User{}, err
	}
	user.PasswordHash = ""
	return user, nil
}

// LookupUser exposes only the public user record needed by owner-managed
// sharing. Authorization is enforced by the API before this method is called.
func (s *Service) LookupUser(ctx context.Context, username string) (User, error) {
	user, err := s.users.GetUserByUsername(ctx, username)
	if err != nil {
		return User{}, err
	}
	if user.Status != UserActive {
		return User{}, ErrUserDisabled
	}
	return user, nil
}
func (s *Service) Login(ctx context.Context, username, password string) (TokenPair, error) {
	user, err := s.users.GetUserByUsername(ctx, username)
	if err != nil {
		return TokenPair{}, ErrInvalidCredentials
	}
	if user.Status != UserActive {
		return TokenPair{}, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return TokenPair{}, ErrInvalidCredentials
	}
	return s.newSession(ctx, user.ID)
}
func (s *Service) Refresh(ctx context.Context, refresh string) (TokenPair, error) {
	requestID, err := randomID()
	if err != nil {
		return TokenPair{}, err
	}
	return s.RefreshWithRequestID(ctx, refresh, requestID)
}

func (s *Service) RefreshWithRequestID(ctx context.Context, refresh, requestID string) (TokenPair, error) {
	if !validRefreshRequestID.MatchString(requestID) {
		return TokenPair{}, ErrInvalidRefreshToken
	}
	sessionID, secret, userID, err := s.validateRefreshSubject(ctx, refresh)
	if err != nil {
		return TokenPair{}, err
	}
	newRefresh, err := s.rotateRefreshToken(ctx, sessionID, secret, requestID)
	if err != nil {
		return TokenPair{}, err
	}
	access, err := s.tokens.Issue(userID, sessionID)
	if err != nil {
		return TokenPair{}, err
	}
	return s.tokenPair(access, newRefresh), nil
}

func (s *Service) validateRefreshSubject(ctx context.Context, refresh string) (sessionID, secret, userID string, err error) {
	sessionID, secret, err = parseRefresh(refresh)
	if err != nil {
		return "", "", "", err
	}
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return "", "", "", ErrInvalidRefreshToken
	}
	user, err := s.users.GetUserByID(ctx, session.UserID)
	if err != nil || user.Status != UserActive {
		_ = s.sessions.Delete(ctx, sessionID)
		s.registry.CloseSession(sessionID)
		return "", "", "", ErrInvalidRefreshToken
	}
	return sessionID, secret, session.UserID, nil
}

func (s *Service) rotateRefreshToken(ctx context.Context, sessionID, oldSecret, requestID string) (string, error) {
	newSecret, err := randomHex(32)
	if err != nil {
		return "", err
	}
	oldHash := hashRefresh(oldSecret)
	newHash := hashRefresh(newSecret)
	newRefresh := sessionID + "." + newSecret
	if store, ok := s.sessions.(IdempotentSessionStore); ok {
		encrypted, err := s.replay.encrypt(newRefresh, replayAAD(sessionID, requestID, oldHash, newHash))
		if err != nil {
			return "", err
		}
		stored, storedNewHash, replayed, err := store.RotateReplay(ctx, sessionID, oldHash, newHash, requestID, encrypted, s.config.RefreshTTL, s.config.RefreshReplayTTL)
		if err != nil {
			return "", ErrInvalidRefreshToken
		}
		if replayed {
			// Decrypt a replay only while the session still references this rotation,
			// preventing an obsolete token from being returned.
			newRefresh, err = s.replay.decrypt(stored, replayAAD(sessionID, requestID, oldHash, storedNewHash))
			if err != nil {
				return "", ErrInvalidRefreshToken
			}
		}
	} else if err := s.sessions.Rotate(ctx, sessionID, oldHash, newHash, s.config.RefreshTTL); err != nil {
		return "", ErrInvalidRefreshToken
	}
	return newRefresh, nil
}
func (s *Service) Logout(ctx context.Context, principal Principal) error {
	gate := s.gate(principal.SessionID)
	gate.Lock()
	defer gate.Unlock()
	if err := s.ValidatePrincipal(ctx, principal); err != nil {
		return err
	}
	coordination := distributedStore(s.sessions)
	var err error
	if coordination != nil {
		err = coordination.Revoke(ctx, principal.SessionID)
	} else {
		err = s.sessions.Delete(ctx, principal.SessionID)
	}
	if err != nil {
		return err
	}
	s.registry.CloseSession(principal.SessionID)
	if coordination != nil {
		waitCtx, cancel := context.WithTimeout(ctx, s.config.LogoutWaitTimeout)
		defer cancel()
		if err := coordination.WaitForOperations(waitCtx, principal.SessionID); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return ErrLogoutIncomplete
			}
			return err
		}
	}
	return nil
}
func (s *Service) Me(ctx context.Context, principal Principal) (User, error) {
	if err := s.ValidatePrincipal(ctx, principal); err != nil {
		return User{}, err
	}
	user, err := s.users.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return User{}, err
	}
	user.PasswordHash = ""
	return user, nil
}
func (s *Service) newSession(ctx context.Context, userID string) (TokenPair, error) {
	sid, err := randomID()
	if err != nil {
		return TokenPair{}, err
	}
	secret, err := randomHex(32)
	if err != nil {
		return TokenPair{}, err
	}
	session := Session{UserID: userID, RefreshHash: hashRefresh(secret), CreatedAt: time.Now().UTC()}
	if err := s.sessions.Create(ctx, sid, session, s.config.RefreshTTL); err != nil {
		return TokenPair{}, err
	}
	access, err := s.tokens.Issue(userID, sid)
	if err != nil {
		_ = s.sessions.Delete(ctx, sid)
		return TokenPair{}, err
	}
	return s.tokenPair(access, sid+"."+secret), nil
}

func (s *Service) tokenPair(accessToken, refreshToken string) TokenPair {
	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.tokens.config.AccessTTL.Seconds()),
	}
}
func (s *Service) AuthenticateToken(ctx context.Context, token string) (Principal, error) {
	principal, err := s.tokens.Parse(token)
	if err != nil {
		return Principal{}, err
	}
	if err := s.ValidatePrincipal(ctx, principal); err != nil {
		return Principal{}, err
	}
	user, err := s.users.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return Principal{}, ErrUserDisabled
	}
	principal.Username = user.Username
	return principal, nil
}
func (s *Service) AuthenticateHTTP(request *http.Request) (Principal, error) {
	token, err := BearerToken(request)
	if err != nil {
		return Principal{}, err
	}
	return s.AuthenticateToken(request.Context(), token)
}
func (s *Service) ValidatePrincipal(ctx context.Context, principal Principal) error {
	if !principal.Trusted || principal.UserID == "" || principal.SessionID == "" {
		return ErrInvalidToken
	}
	session, err := s.sessions.Get(ctx, principal.SessionID)
	if err != nil {
		return ErrSessionInvalid
	}
	if session.UserID != principal.UserID {
		return ErrSessionInvalid
	}
	user, err := s.users.GetUserByID(ctx, principal.UserID)
	if err != nil || user.Status != UserActive {
		return ErrUserDisabled
	}
	return nil
}

func (s *Service) AcquirePrincipal(ctx context.Context, principal Principal) (func(), error) {
	gate := s.gate(principal.SessionID)
	gate.RLock()
	if err := s.ValidatePrincipal(ctx, principal); err != nil {
		gate.RUnlock()
		return nil, err
	}
	coordination := distributedStore(s.sessions)
	if coordination == nil || s.config.NodeID == "" {
		return gate.RUnlock, nil
	}
	releaseLease, err := coordination.AcquireOperation(ctx, principal.SessionID, s.config.NodeID, s.config.OperationLeaseTTL)
	if err != nil {
		gate.RUnlock()
		return nil, err
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = releaseLease(releaseCtx)
		cancel()
		gate.RUnlock()
	}, nil
}

func (s *Service) gate(sessionID string) *sessionGate {
	value, _ := s.gates.LoadOrStore(sessionID, &sessionGate{})
	return value.(*sessionGate)
}
func (s *Service) Registry() *ConnectionRegistry { return s.registry }

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.closing = true
		subscription := s.revocationSubscription
		s.revocationSubscription = nil
		s.lifecycleMu.Unlock()

		s.cancel()
		if subscription != nil {
			s.closeErr = subscription.Close()
		}
		s.registry.Close()
		s.workers.Wait()
	})
	return s.closeErr
}

func distributedStore(store SessionStore) DistributedSessionStore {
	distributed, _ := store.(DistributedSessionStore)
	return distributed
}

func (s *Service) startRevocationListener() {
	coordination := distributedStore(s.sessions)
	if coordination == nil || s.config.NodeID == "" {
		return
	}
	subscription := coordination.NewRevocationSubscription()
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		if !s.registerRevocationSubscription(subscription) {
			_ = subscription.Close()
			return
		}
		defer func() {
			s.unregisterRevocationSubscription(subscription)
			_ = subscription.Close()
		}()
		_ = subscription.Run(s.ctx, func(sessionID string) {
			slog.Info("session revoked", "component", "authentication", "event", "session_revoked")
			if s.config.Observer != nil {
				s.config.Observer.RedisEvent("session_revoked", "success")
			}
			s.registry.CloseSession(sessionID)
		}, func() {
			slog.Info("redis pubsub connected", "component", "authentication", "event", "redis_reconnected")
			if s.config.Observer != nil {
				s.config.Observer.RedisEvent("pubsub_reconnect", "success")
			}
			s.reconcileLocalSessions()
		})
	}()
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		ticker := time.NewTicker(s.config.ReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.reconcileLocalSessions()
			}
		}
	}()
}

func (s *Service) registerRevocationSubscription(subscription RevocationSubscription) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closing {
		return false
	}
	s.revocationSubscription = subscription
	return true
}

func (s *Service) unregisterRevocationSubscription(subscription RevocationSubscription) {
	s.lifecycleMu.Lock()
	if s.revocationSubscription == subscription {
		s.revocationSubscription = nil
	}
	s.lifecycleMu.Unlock()
}

func (s *Service) reconcileLocalSessions() {
	for _, sessionID := range s.registry.SessionIDs() {
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
		_, err := s.sessions.Get(ctx, sessionID)
		cancel()
		if err != nil {
			s.registry.CloseSession(sessionID)
		}
	}
}
func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

var _ Authenticator = (*Service)(nil)
var _ PrincipalUseAuthenticator = (*Service)(nil)
