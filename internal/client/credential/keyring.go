package credential

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const serviceName = "go-realtime-collab-editor"

type Keyring struct{}

func (Keyring) GetRefreshToken(profile string) (Value, error) {
	secret, err := keyring.Get(serviceName, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return Value{}, ErrNotFound
	}
	if err != nil {
		return Value{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var value Value
	if err := json.Unmarshal([]byte(secret), &value); err != nil || value.RefreshToken == "" {
		return Value{}, fmt.Errorf("%w: invalid stored credential", ErrUnavailable)
	}
	return value, nil
}

func (Keyring) SetRefreshToken(profile string, value Value) error {
	if value.RefreshToken == "" {
		return errors.New("refresh token is required")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := keyring.Set(serviceName, profile, string(payload)); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (Keyring) DeleteRefreshToken(profile string) error {
	if err := keyring.Delete(serviceName, profile); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

var _ Store = Keyring{}
