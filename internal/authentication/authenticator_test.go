package authentication

import (
	"net/http/httptest"
	"testing"
)

func TestDevelopmentAuthenticatorMarksIdentityUntrusted(t *testing.T) {
	authenticator := DevelopmentAuthenticator{}
	httpRequest := httptest.NewRequest("GET", "/", nil)
	httpRequest.Header.Set("X-User-ID", "user-a")
	principal, err := authenticator.AuthenticateHTTP(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != "user-a" || principal.Trusted {
		t.Fatalf("HTTP principal = %#v", principal)
	}
}

func TestDevelopmentAuthenticatorRejectsMissingIdentity(t *testing.T) {
	authenticator := DevelopmentAuthenticator{}
	request := httptest.NewRequest("GET", "/", nil)
	if _, err := authenticator.AuthenticateHTTP(request); err == nil {
		t.Fatal("missing identity was accepted")
	}
}
