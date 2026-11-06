package etcd

import (
	"testing"
	"time"
)

func TestLeaseConfirmationWindowKeepsSafetyMargin(t *testing.T) {
	for _, ttlSeconds := range []int64{2, 3, 9} {
		ttl := time.Duration(ttlSeconds) * time.Second
		window := leaseConfirmationWindow(ttlSeconds)
		if window >= ttl || window <= ttl/2 {
			t.Fatalf("TTL %d confirmation window %s has no safe renewal margin", ttlSeconds, window)
		}
	}
}
