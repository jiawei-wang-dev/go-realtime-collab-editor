package storage

import (
	"context"
	"errors"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
)

var (
	ErrFolderNotEmpty = errors.New("folder is not empty")
	ErrFolderCycle    = errors.New("cannot move a folder into itself or a descendant")
)

type Folder struct {
	ID       string          `json:"id"`
	ParentID string          `json:"parent_id,omitempty"`
	Name     string          `json:"name"`
	Role     permission.Role `json:"role"`
}

type FolderStore interface {
	DocumentName(ctx context.Context, documentID string) (string, error)
	RenameDocument(ctx context.Context, documentID, ownerID, name string) error
	RenameFolder(ctx context.Context, folderID, ownerID, name string) error
	DeleteEmptyFolder(ctx context.Context, folderID, ownerID string) error
	MoveFolder(ctx context.Context, folderID, ownerID, parentID string, preview bool) ([]AccessChange, error)
	ListFolderAccess(ctx context.Context, folderID, ownerID string) ([]FolderAccess, error)
	ListDocumentAccess(ctx context.Context, documentID, ownerID string) ([]FolderAccess, error)
	ListFolders(context.Context, string) ([]Folder, error)
	CreateFolder(ctx context.Context, folder Folder, ownerID string) error
	SetFolderPermission(ctx context.Context, folderID, actorID, userID string, role permission.Role) error
	CreateFolderDocument(ctx context.Context, id, content, ownerID, folderID, name string) (collab.Document, error)
	MoveDocument(ctx context.Context, documentID, ownerID, folderID string) error
	PreviewDocumentMove(ctx context.Context, documentID, ownerID, folderID string) ([]AccessChange, error)
}

type AccessChange struct {
	UserID   string          `json:"user_id"`
	Username string          `json:"username,omitempty"`
	Before   permission.Role `json:"before"`
	After    permission.Role `json:"after"`
}

type FolderAccess struct {
	UserID     string          `json:"user_id"`
	Username   string          `json:"username"`
	Role       permission.Role `json:"role"`
	FolderID   string          `json:"folder_id"`
	FolderName string          `json:"folder_name"`
}
