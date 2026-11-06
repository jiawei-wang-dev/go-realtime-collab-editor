package credential

import (
	"errors"
	"sync"
	"testing"
)

type Memory struct {
	mu          sync.Mutex
	values      map[string]Value
	Unavailable bool
}

func NewMemory() *Memory { return &Memory{values: map[string]Value{}} }

func (m *Memory) GetRefreshToken(profile string) (Value, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return Value{}, ErrUnavailable
	}
	value, ok := m.values[profile]
	if !ok {
		return Value{}, ErrNotFound
	}
	return value, nil
}

func (m *Memory) SetRefreshToken(profile string, value Value) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return ErrUnavailable
	}
	m.values[profile] = value
	return nil
}

func (m *Memory) DeleteRefreshToken(profile string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return ErrUnavailable
	}
	delete(m.values, profile)
	return nil
}

func TestCredentialStoreContractAndProfileIsolation(t *testing.T) {
	store := NewMemory()
	if _, err := store.GetRefreshToken("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	if err := store.SetRefreshToken("a", Value{RefreshToken: "secret-a", RotationRequestID: "rotation"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRefreshToken("b", Value{RefreshToken: "secret-b"}); err != nil {
		t.Fatal(err)
	}
	value, _ := store.GetRefreshToken("a")
	if value.RefreshToken != "secret-a" || value.RotationRequestID != "rotation" {
		t.Fatalf("value = %#v", value)
	}
	if err := store.DeleteRefreshToken("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRefreshToken("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted error = %v", err)
	}
	value, _ = store.GetRefreshToken("b")
	if value.RefreshToken != "secret-b" {
		t.Fatal("profiles were not isolated")
	}
	store.Unavailable = true
	if _, err := store.GetRefreshToken("b"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
}
