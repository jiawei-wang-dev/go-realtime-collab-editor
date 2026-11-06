package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrRefreshRejected       = errors.New("refresh token was rejected; login is required")
	ErrRefreshOutcomeUnknown = errors.New("refresh result is unknown; login is required")
	ErrRefreshRetryable      = errors.New("durable refresh attempt can be retried")
	ErrInvalidAccessToken    = errors.New("access token has no valid exp claim")
)

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

type RefreshClient interface {
	Refresh(context.Context, string, string) (TokenPair, error)
}

type HTTPRefreshClient struct {
	BaseURL string
	Client  *http.Client
}

func (c HTTPRefreshClient) Refresh(ctx context.Context, refreshToken, requestID string) (TokenPair, error) {
	if refreshToken == "" {
		return TokenPair{}, ErrRefreshRejected
	}
	payload, err := json.Marshal(map[string]string{"refresh_token": refreshToken, "request_id": requestID})
	if err != nil {
		return TokenPair{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/api/auth/refresh", bytes.NewReader(payload))
	if err != nil {
		return TokenPair{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	httpClient := c.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		// After a request is sent, a transport error cannot prove that the server
		// did not rotate the one-time refresh secret. Reusing it is therefore unsafe.
		return TokenPair{}, fmt.Errorf("%w: %v", ErrRefreshOutcomeUnknown, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return TokenPair{}, ErrRefreshRejected
		}
		return TokenPair{}, fmt.Errorf("%w: refresh endpoint returned HTTP %d", ErrRefreshOutcomeUnknown, response.StatusCode)
	}
	var pair TokenPair
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&pair); err != nil || pair.AccessToken == "" || pair.RefreshToken == "" {
		return TokenPair{}, fmt.Errorf("%w: invalid refresh response", ErrRefreshOutcomeUnknown)
	}
	return pair, nil
}

type TokenManager struct {
	mu            sync.Mutex
	accessToken   string
	refreshToken  string
	refresher     RefreshClient
	safetyWindow  time.Duration
	now           func() time.Time
	refreshUnsafe bool
	persistence   RefreshPersistence
}

func NewTokenManager(accessToken, refreshToken string, refresher RefreshClient, safetyWindow time.Duration) *TokenManager {
	if safetyWindow <= 0 {
		safetyWindow = time.Minute
	}
	return &TokenManager{accessToken: accessToken, refreshToken: refreshToken, refresher: refresher, safetyWindow: safetyWindow, now: time.Now}
}

func (m *TokenManager) SetRefreshPersistence(persistence RefreshPersistence) {
	m.mu.Lock()
	m.persistence = persistence
	m.mu.Unlock()
}

func (m *TokenManager) AccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshUnsafe {
		return "", ErrRefreshOutcomeUnknown
	}
	if m.accessToken == "" {
		return m.refreshLocked(ctx)
	}
	expiresAt, err := accessTokenExpiry(m.accessToken)
	if err != nil {
		return "", err
	}
	if m.now().Add(m.safetyWindow).Before(expiresAt) {
		return m.accessToken, nil
	}
	return m.refreshLocked(ctx)
}

func (m *TokenManager) ForceRefresh(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshUnsafe {
		return "", ErrRefreshOutcomeUnknown
	}
	return m.refreshLocked(ctx)
}

func (m *TokenManager) RefreshDueIn() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	expiresAt, err := accessTokenExpiry(m.accessToken)
	if err != nil {
		return 0
	}
	return expiresAt.Add(-m.safetyWindow).Sub(m.now())
}

func (m *TokenManager) refreshLocked(ctx context.Context) (string, error) {
	if m.refresher == nil || m.refreshToken == "" {
		return "", ErrRefreshRejected
	}
	requestID := ""
	if m.persistence != nil {
		var err error
		requestID, err = m.persistence.Begin(ctx)
		if err != nil {
			return "", fmt.Errorf("persist refresh attempt: %w", err)
		}
	}
	pair, err := m.refresher.Refresh(ctx, m.refreshToken, requestID)
	if err != nil {
		if errors.Is(err, ErrRefreshOutcomeUnknown) && m.persistence == nil {
			m.refreshUnsafe = true
			m.refreshToken = ""
		}
		if errors.Is(err, ErrRefreshOutcomeUnknown) && m.persistence != nil {
			return "", fmt.Errorf("%w: %v", ErrRefreshRetryable, err)
		}
		return "", err
	}
	if m.persistence != nil {
		if err := m.persistence.Complete(ctx, requestID, pair.RefreshToken); err != nil {
			return "", fmt.Errorf("persist rotated refresh token: %w", err)
		}
	}
	// Commit the local rotation in the same critical section so callers cannot
	// connect or refresh against a partially updated credential.
	m.accessToken = pair.AccessToken
	m.refreshToken = pair.RefreshToken
	return m.accessToken, nil
}

func accessTokenExpiry(tokenString string) (time.Time, error) {
	claims := jwt.RegisteredClaims{}
	parser := jwt.NewParser()
	if _, _, err := parser.ParseUnverified(tokenString, &claims); err != nil || claims.ExpiresAt == nil {
		return time.Time{}, ErrInvalidAccessToken
	}
	return claims.ExpiresAt.Time, nil
}
