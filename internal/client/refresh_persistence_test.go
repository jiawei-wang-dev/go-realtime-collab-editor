package client

import (
	"context"
	"errors"
	"testing"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/credential"
	clientstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/storage"
)

type mockCredentials struct{ values map[string]credential.Value }

func (m *mockCredentials) GetRefreshToken(profile string) (credential.Value, error) {
	value, ok := m.values[profile]
	if !ok {
		return credential.Value{}, credential.ErrNotFound
	}
	return value, nil
}
func (m *mockCredentials) SetRefreshToken(profile string, value credential.Value) error {
	m.values[profile] = value
	return nil
}
func (m *mockCredentials) DeleteRefreshToken(profile string) error {
	delete(m.values, profile)
	return nil
}

func TestDurableRefreshPersistenceCrashWindows(t *testing.T) {
	ctx := context.Background()
	state, err := clientstorage.Open(ctx, t.TempDir(), "profile")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	credentials := &mockCredentials{values: map[string]credential.Value{}}
	persistence := DurableRefreshPersistence{State: state, Credentials: credentials, Profile: "profile"}
	requestID, err := persistence.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := persistence.Begin(ctx)
	if err != nil || repeated != requestID {
		t.Fatalf("stable request ID = %q, %v", repeated, err)
	}
	if err := credentials.SetRefreshToken("profile", credential.Value{RefreshToken: "rotated", RotationRequestID: requestID}); err != nil {
		t.Fatal(err)
	}
	value, err := ReconcileRefreshAttempt(ctx, state, credentials, "profile")
	if err != nil || value.RefreshToken != "rotated" {
		t.Fatalf("reconcile = %#v, %v", value, err)
	}
	recovery, _ := state.Recovery(ctx)
	if recovery.RefreshAttempt != nil {
		t.Fatal("completed refresh attempt was not cleared")
	}
}

func TestRefreshStateMismatchIsNotGuessed(t *testing.T) {
	ctx := context.Background()
	state, _ := clientstorage.Open(ctx, t.TempDir(), "profile")
	defer state.Close()
	_ = state.SaveRefreshAttempt(ctx, clientstorage.RefreshAttempt{RequestID: "pending", Status: "pending"})
	credentials := &mockCredentials{values: map[string]credential.Value{"profile": {RefreshToken: "new", RotationRequestID: "different"}}}
	if _, err := ReconcileRefreshAttempt(ctx, state, credentials, "profile"); !errors.Is(err, ErrRefreshStateMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestRefreshAttemptAcceptsCredentialVersionObservedAtStart(t *testing.T) {
	ctx := context.Background()
	state, _ := clientstorage.Open(ctx, t.TempDir(), "profile")
	defer state.Close()
	credentials := &mockCredentials{values: map[string]credential.Value{"profile": {RefreshToken: "current", RotationRequestID: "previous-success"}}}
	persistence := DurableRefreshPersistence{State: state, Credentials: credentials, Profile: "profile"}
	requestID, err := persistence.Begin(ctx)
	if err != nil || requestID == "" {
		t.Fatalf("Begin = %q, %v", requestID, err)
	}
	value, err := ReconcileRefreshAttempt(ctx, state, credentials, "profile")
	if err != nil || value.RefreshToken != "current" {
		t.Fatalf("reconcile prior credential = %#v, %v", value, err)
	}
}
