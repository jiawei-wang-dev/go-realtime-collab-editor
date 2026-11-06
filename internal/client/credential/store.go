package credential

import (
	"errors"
)

var (
	ErrNotFound    = errors.New("refresh credential not found")
	ErrUnavailable = errors.New("operating system credential store unavailable")
)

type Value struct {
	RefreshToken      string `json:"refresh_token"`
	RotationRequestID string `json:"rotation_request_id,omitempty"`
}

type Store interface {
	GetRefreshToken(profile string) (Value, error)
	SetRefreshToken(profile string, value Value) error
	DeleteRefreshToken(profile string) error
}
