package client

import (
	"testing"
	"time"
)

func TestBackoffExponentAndMaximum(t *testing.T) {
	backoff := NewBackoff(500*time.Millisecond, 30*time.Second, 0)
	wants := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for index, want := range wants {
		if got := backoff.Duration(index + 1); got != want {
			t.Fatalf("Duration(%d) = %v, want %v", index+1, got, want)
		}
	}
}

func TestBackoffJitterStaysBounded(t *testing.T) {
	backoff := NewBackoff(time.Second, 30*time.Second, 0.2)
	seenDifferent := false
	first := backoff.Duration(1)
	for range 100 {
		got := backoff.Duration(1)
		if got < 800*time.Millisecond || got > 1200*time.Millisecond {
			t.Fatalf("jittered duration out of bounds: %v", got)
		}
		seenDifferent = seenDifferent || got != first
	}
	if !seenDifferent {
		t.Fatal("jitter did not vary")
	}
}
