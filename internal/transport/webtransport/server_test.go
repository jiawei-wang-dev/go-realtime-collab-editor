package webtransport

import (
	"net/http/httptest"
	"testing"
)

func TestOriginValidationRequiresExactConfiguredOrigin(t *testing.T) {
	server := &Server{config: Config{AllowedOrigins: []string{"https://editor.example.com"}}}

	allowed := httptest.NewRequest("CONNECT", "https://node.example.com"+Path, nil)
	allowed.Header.Set("Origin", "https://editor.example.com")
	if !server.allowOrigin(allowed) {
		t.Fatal("configured origin was rejected")
	}

	for _, origin := range []string{"", "https://evil.example.com", "https://editor.example.com.evil.test"} {
		request := httptest.NewRequest("CONNECT", "https://node.example.com"+Path, nil)
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		if server.allowOrigin(request) {
			t.Fatalf("untrusted origin %q was accepted", origin)
		}
	}
}
