package api

import (
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

func workspaceID() string { return rand.Text() }

func (s *Server) listDocumentAccess(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	id := r.PathValue("docID")
	if store, ok := s.store.(storage.FolderStore); ok {
		grants, err := store.ListDocumentAccess(r.Context(), id, principal.UserID)
		if err != nil {
			writeFolderError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, grants)
		return
	}
	role, err := s.store.GetPermission(r.Context(), id, principal.UserID)
	if err != nil || !role.CanChangePermissions() {
		writeFolderError(w, storage.ErrPermissionDenied)
		return
	}
	roles, err := s.store.GetPermissions(r.Context(), id)
	if err != nil {
		writeFolderError(w, err)
		return
	}
	grants := make([]storage.FolderAccess, 0, len(roles))
	for userID, role := range roles {
		grants = append(grants, storage.FolderAccess{UserID: userID, Username: userID, Role: role})
	}
	writeJSON(w, http.StatusOK, grants)
}

func (s *Server) listFolderAccess(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	store, ok := s.store.(storage.FolderStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "folders require MySQL")
		return
	}
	grants, err := store.ListFolderAccess(r.Context(), r.PathValue("folderID"), principal.UserID)
	if err != nil {
		writeFolderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, grants)
}

func (s *Server) manageFolder(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	store, ok := s.store.(storage.FolderStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "folders require MySQL")
		return
	}
	id := r.PathValue("folderID")
	if r.Method == http.MethodDelete {
		if err := store.DeleteEmptyFolder(r.Context(), id, principal.UserID); err != nil {
			writeFolderError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	parent := r.URL.Query().Get("parent_id")
	preview := r.Method == http.MethodGet
	if !preview {
		var body struct {
			ParentID *string `json:"parent_id"`
		}
		if err := decodeJSON(r, &body); err != nil || body.ParentID == nil {
			writeError(w, http.StatusBadRequest, protocol.CodeBadMessage, "parent_id is required")
			return
		}
		parent = *body.ParentID
	}
	changes, err := store.MoveFolder(r.Context(), id, principal.UserID, parent, preview)
	if err != nil {
		writeFolderError(w, err)
		return
	}
	if preview {
		writeJSON(w, http.StatusOK, changes)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) renameWorkspaceItem(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	store, ok := s.store.(storage.FolderStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "renaming requires MySQL")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil || !validWorkspaceName(body.Name) {
		writeError(w, http.StatusBadRequest, protocol.CodeBadMessage, "invalid name")
		return
	}
	var err error
	if folderID := r.PathValue("folderID"); folderID != "" {
		err = store.RenameFolder(r.Context(), folderID, principal.UserID, body.Name)
	} else {
		err = store.RenameDocument(r.Context(), r.PathValue("docID"), principal.UserID, body.Name)
	}
	if err != nil {
		writeFolderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) moveDocument(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	store, ok := s.store.(storage.FolderStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "folders require MySQL")
		return
	}
	if r.Method == http.MethodGet {
		changes, err := store.PreviewDocumentMove(r.Context(), r.PathValue("docID"), principal.UserID, r.URL.Query().Get("folder_id"))
		if err != nil {
			writeFolderError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, changes)
		return
	}
	var body struct {
		FolderID *string `json:"folder_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.FolderID == nil {
		writeError(w, http.StatusBadRequest, protocol.CodeBadMessage, "folder_id is required")
		return
	}
	if err := store.MoveDocument(r.Context(), r.PathValue("docID"), principal.UserID, *body.FolderID); err != nil {
		writeFolderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validWorkspaceName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 200 &&
		name == strings.TrimSpace(name) && !strings.ContainsAny(name, "/\\%") &&
		strings.IndexFunc(name, unicode.IsControl) < 0
}

func (s *Server) listFolders(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	folders := []storage.Folder{}
	if store, ok := s.store.(storage.FolderStore); ok {
		var err error
		folders, err = store.ListFolders(r.Context(), principal.UserID)
		if err != nil {
			writeFolderError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string][]storage.Folder{"folders": folders})
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	store, ok := s.store.(storage.FolderStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "folders require MySQL")
		return
	}
	var body struct {
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
	}
	if err := decodeJSON(r, &body); err != nil || !validWorkspaceName(body.Name) {
		writeError(w, http.StatusBadRequest, protocol.CodeBadMessage, "invalid folder name")
		return
	}
	folder := storage.Folder{ID: workspaceID(), Name: body.Name, ParentID: body.ParentID, Role: permission.RoleOwner}
	if err := store.CreateFolder(r.Context(), folder, principal.UserID); err != nil {
		writeFolderError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, folder)
}

func (s *Server) updateFolderPermission(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	store, ok := s.store.(storage.FolderStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "folders require MySQL")
		return
	}
	var body updatePermissionRequest
	if r.Method == http.MethodPut {
		if err := decodeJSON(r, &body); err != nil || (body.Role != permission.RoleRead && body.Role != permission.RoleWrite) {
			writeError(w, http.StatusBadRequest, protocol.CodeBadMessage, "folder role must be read or write")
			return
		}
	}
	if err := store.SetFolderPermission(r.Context(), r.PathValue("folderID"), principal.UserID, r.PathValue("userID"), body.Role); err != nil {
		writeFolderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeFolderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrPermissionDenied):
		writeError(w, http.StatusForbidden, protocol.CodePermissionDenied, "owner permission required")
	case errors.Is(err, storage.ErrAlreadyExists):
		writeError(w, http.StatusConflict, protocol.CodeBadMessage, "folder already exists")
	case errors.Is(err, storage.ErrFolderNotEmpty), errors.Is(err, storage.ErrFolderCycle):
		writeError(w, http.StatusConflict, protocol.CodeBadMessage, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, protocol.CodeInternalError, "could not update workspace")
	}
}
