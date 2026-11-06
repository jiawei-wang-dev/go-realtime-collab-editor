package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

type authFixture struct {
	server  *httptest.Server
	service *authentication.Service
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	users := authentication.NewMemoryUsers()
	sessions := authentication.NewMemorySessions()
	tokens, err := authentication.NewTokenManager(authentication.JWTConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"), Issuer: "test", Audience: "api", AccessTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := authentication.NewService(users, sessions, tokens, authentication.ServiceConfig{RefreshTTL: time.Hour, BcryptCost: bcrypt.MinCost})
	application := NewServerWithConfig(Config{Store: storage.NewMemory(), Authenticator: service, AuthService: service})
	server := httptest.NewServer(application)
	t.Cleanup(func() {
		server.Close()
		_ = application.Shutdown(context.Background())
	})
	return &authFixture{server: server, service: service}
}

func (f *authFixture) registerLogin(t *testing.T, username string) authentication.TokenPair {
	t.Helper()
	body := `{"username":"` + username + `","password":"password1"}`
	response, err := http.Post(f.server.URL+"/api/auth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d", response.StatusCode)
	}
	response, err = http.Post(f.server.URL+"/api/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var pair authentication.TokenPair
	if err := json.NewDecoder(response.Body).Decode(&pair); err != nil {
		t.Fatal(err)
	}
	return pair
}

func TestRegisterLoginMeAndProtectedDocuments(t *testing.T) {
	fixture := newAuthFixture(t)
	pair := fixture.registerLogin(t, "owner-user")
	response := authRequest(t, http.MethodGet, fixture.server.URL+"/api/auth/me", pair.AccessToken, nil)
	var user authentication.User
	if err := json.NewDecoder(response.Body).Decode(&user); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if user.Username != "owner-user" {
		t.Fatalf("me = %#v", user)
	}
	response = authRequest(t, http.MethodPost, fixture.server.URL+"/api/documents", pair.AccessToken, []byte(`{"id":"secure","content":"abc","owner_id":"forged"}`))
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", response.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodGet, fixture.server.URL+"/api/documents/secure", nil)
	request.Header.Set("X-User-ID", "forged")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged identity status = %d", response.StatusCode)
	}
}

func TestAuthenticationTimeoutFailsClosed(t *testing.T) {
	application := NewServerWithConfig(Config{
		Store:                 storage.NewMemory(),
		Authenticator:         blockingAuthenticator{},
		AuthenticationTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	request := httptest.NewRequest(http.MethodGet, "/api/documents/doc", nil)
	response := httptest.NewRecorder()
	started := time.Now()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("authentication timeout took %s", elapsed)
	}
}

type blockingAuthenticator struct{}

func (blockingAuthenticator) AuthenticateToken(ctx context.Context, _ string) (authentication.Principal, error) {
	<-ctx.Done()
	return authentication.Principal{}, ctx.Err()
}

func (blockingAuthenticator) AuthenticateHTTP(request *http.Request) (authentication.Principal, error) {
	return blockingAuthenticator{}.AuthenticateToken(request.Context(), "")
}

func (blockingAuthenticator) ValidatePrincipal(ctx context.Context, _ authentication.Principal) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRefreshAndLogoutEndpoints(t *testing.T) {
	fixture := newAuthFixture(t)
	pair := fixture.registerLogin(t, "session-user")
	body, _ := json.Marshal(map[string]string{"refresh_token": pair.RefreshToken})
	response, err := http.Post(fixture.server.URL+"/api/auth/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var rotated authentication.TokenPair
	if err := json.NewDecoder(response.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response = authRequest(t, http.MethodPost, fixture.server.URL+"/api/auth/logout", rotated.AccessToken, nil)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d", response.StatusCode)
	}
	response = authRequest(t, http.MethodGet, fixture.server.URL+"/api/auth/me", rotated.AccessToken, nil)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logged-out token status = %d", response.StatusCode)
	}
}

func TestRefreshEndpointReplaysStableRequestID(t *testing.T) {
	fixture := newAuthFixture(t)
	pair := fixture.registerLogin(t, "refresh-replay-user")
	body, _ := json.Marshal(map[string]string{"refresh_token": pair.RefreshToken, "request_id": "stable-refresh-request"})
	firstResponse, err := http.Post(fixture.server.URL+"/api/auth/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var first authentication.TokenPair
	if err := json.NewDecoder(firstResponse.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	firstResponse.Body.Close()
	secondResponse, err := http.Post(fixture.server.URL+"/api/auth/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var second authentication.TokenPair
	if err := json.NewDecoder(secondResponse.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	secondResponse.Body.Close()
	if first.RefreshToken == "" || second.RefreshToken != first.RefreshToken {
		t.Fatal("refresh replay returned a different refresh token")
	}
}

func authRequest(t *testing.T, method, url, token string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
