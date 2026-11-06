package authentication

import (
	"strings"
	"testing"
)

func TestRefreshReplayAESGCMConfidentialityAndAuthentication(t *testing.T) {
	cipher, err := newReplayCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext := "session.plain-refresh-secret"
	aad := replayAAD("session", "request", "old", "new")
	encrypted, err := cipher.encrypt(plaintext, aad)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, plaintext) || strings.Contains(encrypted, "plain-refresh-secret") {
		t.Fatal("ciphertext exposed refresh token")
	}
	decrypted, err := cipher.decrypt(encrypted, aad)
	if err != nil || decrypted != plaintext {
		t.Fatalf("decrypt = %q, %v", decrypted, err)
	}
	if _, err := cipher.decrypt(encrypted, aad+"tampered"); err == nil {
		t.Fatal("AES-GCM accepted wrong associated data")
	}
	wrong, _ := newReplayCipher([]byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))
	if _, err := wrong.decrypt(encrypted, aad); err == nil {
		t.Fatal("wrong encryption key decrypted replay")
	}
}
