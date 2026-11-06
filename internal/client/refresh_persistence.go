package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/credential"
	clientstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/storage"
)

var ErrRefreshStateMismatch = errors.New("refresh credential and pending rotation state do not match")

type RefreshPersistence interface {
	Begin(context.Context) (string, error)
	Complete(context.Context, string, string) error
}

type DurableRefreshPersistence struct {
	State       clientstorage.Store
	Credentials credential.Store
	Profile     string
}

func (p DurableRefreshPersistence) Begin(ctx context.Context) (string, error) {
	recovery, err := p.State.Recovery(ctx)
	if err != nil {
		return "", err
	}
	if recovery.RefreshAttempt != nil {
		return recovery.RefreshAttempt.RequestID, nil
	}
	requestID, err := refreshRequestID()
	if err != nil {
		return "", err
	}
	credentialRequestID := ""
	if value, credentialErr := p.Credentials.GetRefreshToken(p.Profile); credentialErr == nil {
		credentialRequestID = value.RotationRequestID
	} else if !errors.Is(credentialErr, credential.ErrNotFound) {
		return "", credentialErr
	}
	if err := p.State.SaveRefreshAttempt(ctx, clientstorage.RefreshAttempt{RequestID: requestID, CredentialRequestID: credentialRequestID, Status: "pending"}); err != nil {
		return "", err
	}
	return requestID, nil
}

func (p DurableRefreshPersistence) Complete(ctx context.Context, requestID, refreshToken string) error {
	if err := p.Credentials.SetRefreshToken(p.Profile, credential.Value{RefreshToken: refreshToken, RotationRequestID: requestID}); err != nil {
		return err
	}
	return p.State.ClearRefreshAttempt(ctx, requestID)
}

func ReconcileRefreshAttempt(ctx context.Context, state clientstorage.Store, credentials credential.Store, profile string) (credential.Value, error) {
	value, err := credentials.GetRefreshToken(profile)
	if err != nil && !errors.Is(err, credential.ErrNotFound) {
		return credential.Value{}, err
	}
	recovery, recoveryErr := state.Recovery(ctx)
	if recoveryErr != nil {
		return credential.Value{}, recoveryErr
	}
	if recovery.RefreshAttempt == nil {
		return value, err
	}
	if value.RefreshToken != "" && value.RotationRequestID == recovery.RefreshAttempt.RequestID {
		if clearErr := state.ClearRefreshAttempt(ctx, recovery.RefreshAttempt.RequestID); clearErr != nil {
			return credential.Value{}, clearErr
		}
		return value, nil
	}
	if value.RefreshToken == "" {
		return credential.Value{}, credential.ErrNotFound
	}
	if value.RotationRequestID != recovery.RefreshAttempt.RequestID && value.RotationRequestID != recovery.RefreshAttempt.CredentialRequestID {
		return credential.Value{}, ErrRefreshStateMismatch
	}
	// Preserve the old credential and durable request ID so an uncertain
	// rotation can be completed through the server's idempotent replay.
	return value, nil
}

func refreshRequestID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "refresh-" + hex.EncodeToString(value), nil
}
