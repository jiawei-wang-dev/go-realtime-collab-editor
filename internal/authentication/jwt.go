package authentication

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const accessTokenType = "access"

type JWTConfig struct {
	Secret           []byte
	Issuer, Audience string
	AccessTTL        time.Duration
}
type Claims struct {
	SessionID string `json:"sid"`
	TokenType string `json:"typ"`
	jwt.RegisteredClaims
}
type TokenManager struct {
	config JWTConfig
	now    func() time.Time
}

func NewTokenManager(config JWTConfig) (*TokenManager, error) {
	if len(config.Secret) < 32 {
		return nil, fmt.Errorf("JWT secret must be at least 32 bytes")
	}
	if config.Issuer == "" || config.Audience == "" || config.AccessTTL <= 0 {
		return nil, fmt.Errorf("JWT issuer, audience and positive TTL are required")
	}
	return &TokenManager{config: config, now: time.Now}, nil
}
func (m *TokenManager) Issue(userID, sessionID string) (string, error) {
	now := m.now().UTC()
	jti, err := randomHex(16)
	if err != nil {
		return "", err
	}
	claims := Claims{SessionID: sessionID, TokenType: accessTokenType, RegisteredClaims: jwt.RegisteredClaims{
		Subject: userID, Issuer: m.config.Issuer, Audience: jwt.ClaimStrings{m.config.Audience}, ID: jti,
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(m.config.AccessTTL)),
	}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.config.Secret)
}
func (m *TokenManager) Parse(tokenString string) (Principal, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, ErrInvalidToken
		}
		return m.config.Secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(m.config.Issuer), jwt.WithAudience(m.config.Audience), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || claims.Subject == "" || claims.SessionID == "" || claims.ID == "" || claims.TokenType != accessTokenType {
		return Principal{}, ErrInvalidToken
	}
	if claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return Principal{}, ErrInvalidToken
	}
	return Principal{UserID: claims.Subject, SessionID: claims.SessionID, Trusted: true}, nil
}
func randomHex(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
