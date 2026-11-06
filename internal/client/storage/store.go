package clientstorage

import (
	"context"
	"errors"
)

var (
	ErrProfileInUse = errors.New("client profile is already in use")
	ErrCorrupt      = errors.New("client state database failed integrity check")
	ErrNotFound     = errors.New("client state not found")
)

type Store interface {
	Recovery(context.Context) (Recovery, error)
	LoadDocument(context.Context, string) (Document, error)
	SetActiveDocument(context.Context, string) error
	SaveDocument(context.Context, Document) error
	AddPending(context.Context, PendingEdit) error
	ListPending(context.Context, string) ([]PendingEdit, error)
	UpdatePending(context.Context, string, string, int, string) error
	CommitApplied(context.Context, Document, string) error
	SaveRefreshAttempt(context.Context, RefreshAttempt) error
	ClearRefreshAttempt(context.Context, string) error
	Close() error
}
