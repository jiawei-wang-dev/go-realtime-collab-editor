package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type refreshFunc func(context.Context, string, string) (TokenPair, error)

func (f refreshFunc) Refresh(ctx context.Context, token, requestID string) (TokenPair, error) {
	return f(ctx, token, requestID)
}

func clientToken(t *testing.T, expires time.Time) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(expires)}).SignedString([]byte("client-test-secret-client-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestTokenManagerRefreshesExpiringTokenOnceForConcurrentCallers(t *testing.T) {
	now := time.Now()
	newToken := clientToken(t, now.Add(time.Hour))
	var calls atomic.Int32
	manager := NewTokenManager(clientToken(t, now.Add(10*time.Second)), "old-refresh", refreshFunc(func(_ context.Context, token, _ string) (TokenPair, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		if token != "old-refresh" {
			t.Errorf("refresh token = %q", token)
		}
		return TokenPair{AccessToken: newToken, RefreshToken: "new-refresh"}, nil
	}), time.Minute)

	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if token, err := manager.AccessToken(context.Background()); err != nil || token != newToken {
				t.Errorf("AccessToken = %q, %v", token, err)
			}
		}()
	}
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

func TestTokenManagerDoesNotReuseRefreshTokenAfterUnknownOutcome(t *testing.T) {
	var calls atomic.Int32
	manager := NewTokenManager(clientToken(t, time.Now().Add(-time.Minute)), "one-time", refreshFunc(func(context.Context, string, string) (TokenPair, error) {
		calls.Add(1)
		return TokenPair{}, ErrRefreshOutcomeUnknown
	}), time.Minute)
	for range 2 {
		if _, err := manager.AccessToken(context.Background()); !errors.Is(err, ErrRefreshOutcomeUnknown) {
			t.Fatalf("error = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("unsafe refresh retried %d times", calls.Load())
	}
}

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("response lost")
}

func TestHTTPRefreshClientClassifiesTransportFailureAsUnknown(t *testing.T) {
	client := HTTPRefreshClient{BaseURL: "https://auth.invalid", Client: &http.Client{Transport: failingRoundTripper{}}}
	if _, err := client.Refresh(context.Background(), "sid.secret", "request"); !errors.Is(err, ErrRefreshOutcomeUnknown) {
		t.Fatalf("Refresh error = %v", err)
	}
}

func TestHTTPRefreshClientUsesExistingRotationContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/auth/refresh" || request.Method != http.MethodPost {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":900}`))
	}))
	defer server.Close()
	pair, err := (HTTPRefreshClient{BaseURL: server.URL}).Refresh(context.Background(), "old-refresh", "request")
	if err != nil || pair.AccessToken != "new-access" || pair.RefreshToken != "new-refresh" {
		t.Fatalf("Refresh = %#v, %v", pair, err)
	}
}

func TestHTTPRefreshClientDistinguishesRejectedAndUnknown(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{http.StatusUnauthorized, ErrRefreshRejected}, {http.StatusServiceUnavailable, ErrRefreshOutcomeUnknown}} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(test.status) }))
		_, err := (HTTPRefreshClient{BaseURL: server.URL}).Refresh(context.Background(), "refresh", "request")
		server.Close()
		if !errors.Is(err, test.want) {
			t.Fatalf("status %d error = %v", test.status, err)
		}
	}
}

type memoryRefreshPersistence struct {
	requestID string
	completed string
}

func (p *memoryRefreshPersistence) Begin(context.Context) (string, error) {
	if p.requestID == "" {
		p.requestID = "stable-refresh"
	}
	return p.requestID, nil
}
func (p *memoryRefreshPersistence) Complete(_ context.Context, requestID, token string) error {
	if requestID != p.requestID {
		return errors.New("request ID changed")
	}
	p.completed = token
	return nil
}

func TestDurableUnknownRefreshRetriesSameRequestID(t *testing.T) {
	now := time.Now()
	persistence := &memoryRefreshPersistence{}
	var calls atomic.Int32
	var requestIDs []string
	manager := NewTokenManager(clientToken(t, now.Add(-time.Minute)), "old", refreshFunc(func(_ context.Context, _, requestID string) (TokenPair, error) {
		requestIDs = append(requestIDs, requestID)
		if calls.Add(1) == 1 {
			return TokenPair{}, ErrRefreshOutcomeUnknown
		}
		return TokenPair{AccessToken: clientToken(t, now.Add(time.Hour)), RefreshToken: "rotated"}, nil
	}), time.Minute)
	manager.SetRefreshPersistence(persistence)
	if _, err := manager.AccessToken(context.Background()); !errors.Is(err, ErrRefreshRetryable) {
		t.Fatalf("first error = %v", err)
	}
	if _, err := manager.AccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requestIDs) != 2 || requestIDs[0] != requestIDs[1] || persistence.completed != "rotated" {
		t.Fatalf("requests=%#v completed=%q", requestIDs, persistence.completed)
	}
}
