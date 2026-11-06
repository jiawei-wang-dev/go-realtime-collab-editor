package authentication

import (
	"context"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type lifecycleSessionStore struct {
	*MemorySessions
	subscription RevocationSubscription
}

func (s *lifecycleSessionStore) Revoke(ctx context.Context, sessionID string) error {
	return s.Delete(ctx, sessionID)
}

func (*lifecycleSessionStore) AcquireOperation(context.Context, string, string, time.Duration) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}

func (*lifecycleSessionStore) WaitForOperations(context.Context, string) error { return nil }

func (s *lifecycleSessionStore) NewRevocationSubscription() RevocationSubscription {
	return s.subscription
}

func (*lifecycleSessionStore) TouchPresence(context.Context, string, string, time.Duration) error {
	return nil
}

func (*lifecycleSessionStore) DeletePresence(context.Context, string, string) error { return nil }

type blockingRevocationSubscription struct {
	started   chan struct{}
	exited    chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	exitOnce  sync.Once
	closeOnce sync.Once
}

func newBlockingRevocationSubscription() *blockingRevocationSubscription {
	return &blockingRevocationSubscription{
		started: make(chan struct{}),
		exited:  make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (s *blockingRevocationSubscription) Run(context.Context, func(string), func()) error {
	s.startOnce.Do(func() { close(s.started) })
	<-s.closed
	s.exitOnce.Do(func() { close(s.exited) })
	return nil
}

func (s *blockingRevocationSubscription) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func TestServiceCloseStopsBlockedRevocationWorkerAndIsConcurrentSafe(t *testing.T) {
	subscription := newBlockingRevocationSubscription()
	service := newLifecycleTestService(t, subscription)
	select {
	case <-subscription.started:
	case <-time.After(time.Second):
		t.Fatal("revocation worker did not start")
	}

	const callers = 20
	var wait sync.WaitGroup
	errors := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- service.Close()
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent Close error = %v", err)
		}
	}
	select {
	case <-subscription.exited:
	case <-time.After(time.Second):
		t.Fatal("revocation worker remained blocked after Close")
	}
	if err := service.Close(); err != nil {
		t.Fatalf("repeated Close error = %v", err)
	}
}

func TestServiceCloseWhileRevocationWorkerStartsDoesNotLeak(t *testing.T) {
	for range 100 {
		subscription := newBlockingRevocationSubscription()
		service := newLifecycleTestService(t, subscription)
		if err := service.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-subscription.closed:
		case <-time.After(time.Second):
			t.Fatal("subscription created during Close was not closed")
		}
	}
}

func newLifecycleTestService(t *testing.T, subscription RevocationSubscription) *Service {
	t.Helper()
	tokens, err := NewTokenManager(JWTConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
		Issuer: "lifecycle", Audience: "lifecycle", AccessTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &lifecycleSessionStore{MemorySessions: NewMemorySessions(), subscription: subscription}
	return NewService(NewMemoryUsers(), store, tokens, ServiceConfig{
		RefreshTTL: time.Hour, BcryptCost: bcrypt.MinCost, NodeID: "node-a",
	})
}
