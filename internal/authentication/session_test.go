package authentication

import (
	"errors"
	"strings"
	"testing"
)

func TestParseRotateReplayResult(t *testing.T) {
	tests := []struct {
		name         string
		result       []any
		wantStored   string
		wantHash     string
		wantReplayed bool
		wantInvalid  bool
		wantError    string
	}{
		{name: "rotation", result: []any{int64(1), "ciphertext", "new-hash"}, wantStored: "ciphertext", wantHash: "new-hash"},
		{name: "replay", result: []any{int64(2), "ciphertext", "new-hash"}, wantStored: "ciphertext", wantHash: "new-hash", wantReplayed: true},
		{name: "rejected", result: []any{int64(0), "", ""}, wantInvalid: true},
		{name: "nil result", result: nil, wantError: "result length"},
		{name: "short result", result: []any{int64(1)}, wantError: "result length"},
		{name: "long result", result: []any{int64(1), "ciphertext", "new-hash", "extra"}, wantError: "result length"},
		{name: "status type", result: []any{"1", "ciphertext", "new-hash"}, wantError: "status type"},
		{name: "unknown status", result: []any{int64(3), "ciphertext", "new-hash"}, wantError: "status 3"},
		{name: "payload type", result: []any{int64(1), int64(1), "new-hash"}, wantError: "payload type"},
		{name: "empty payload", result: []any{int64(1), "", "new-hash"}, wantError: "empty Redis rotate replay payload"},
		{name: "hash type", result: []any{int64(1), "ciphertext", int64(1)}, wantError: "hash type"},
		{name: "empty hash", result: []any{int64(1), "ciphertext", ""}, wantError: "empty Redis rotate replay hash"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stored, newHash, replayed, err := parseRotateReplayResult(test.result)
			if test.wantInvalid {
				if !errors.Is(err, ErrInvalidRefreshToken) {
					t.Fatalf("error = %v, want ErrInvalidRefreshToken", err)
				}
				return
			}
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want text %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stored != test.wantStored || newHash != test.wantHash || replayed != test.wantReplayed {
				t.Fatalf("result = %q, %q, %t", stored, newHash, replayed)
			}
		})
	}
}
