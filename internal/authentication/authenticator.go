package authentication

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

var (
	ErrMissingIdentity     = errors.New("authentication required")
	ErrInvalidToken        = errors.New("invalid access token")
	ErrInvalidCredentials  = errors.New("invalid username or password")
	ErrSessionInvalid      = errors.New("session is not active")
	ErrUsernameExists      = errors.New("username already exists")
	ErrUserNotFound        = errors.New("user not found")
	ErrUserDisabled        = errors.New("user is disabled")
	ErrInvalidRefreshToken = errors.New("invalid refresh token")
	ErrLogoutIncomplete    = errors.New("logout could not confirm all in-flight operations completed")
)

type Principal struct {
	UserID    string
	SessionID string
	Trusted   bool
}

type Authenticator interface {
	AuthenticateToken(context.Context, string) (Principal, error)
	AuthenticateHTTP(*http.Request) (Principal, error)
	ValidatePrincipal(context.Context, Principal) error
}

// PrincipalUseAuthenticator pins a validated session for one complete
// application message. Logout takes the matching exclusive gate so it cannot
// return while an already-authorized operation is still running.
type PrincipalUseAuthenticator interface {
	Authenticator
	AcquirePrincipal(context.Context, Principal) (release func(), err error)
}

type DevelopmentAuthenticator struct{}

func (DevelopmentAuthenticator) AuthenticateToken(_ context.Context, token string) (Principal, error) {
	return developmentPrincipal(token)
}
func (DevelopmentAuthenticator) AuthenticateHTTP(request *http.Request) (Principal, error) {
	return developmentPrincipal(request.Header.Get("X-User-ID"))
}
func (DevelopmentAuthenticator) ValidatePrincipal(_ context.Context, principal Principal) error {
	if principal.UserID == "" {
		return ErrMissingIdentity
	}
	return nil
}
func developmentPrincipal(userID string) (Principal, error) {
	if userID == "" {
		return Principal{}, ErrMissingIdentity
	}
	return Principal{UserID: userID, Trusted: false}, nil
}

func BearerToken(request *http.Request) (string, error) {
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", ErrMissingIdentity
	}
	return parts[1], nil
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(contextKey{}).(Principal)
	return principal, ok
}
func UserIDFromContext(ctx context.Context) string {
	principal, _ := PrincipalFromContext(ctx)
	return principal.UserID
}
