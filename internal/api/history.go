package api

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

func (s *Server) documentHistory(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	id := r.PathValue("docID")
	role, err := s.store.GetPermission(ctx, id, principal.UserID)
	if err != nil || !role.CanRead() {
		writeError(w, http.StatusForbidden, protocol.CodePermissionDenied, "read permission required")
		return
	}
	history, ok := s.store.(storage.HistoryStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "history requires MySQL")
		return
	}
	if value := r.URL.Query().Get("version"); value != "" {
		version, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, protocol.CodeBadMessage, "invalid version")
			return
		}
		document, err := history.DocumentAtVersion(ctx, id, version)
		if errors.Is(err, storage.ErrHistoryUnavailable) {
			writeError(w, http.StatusNotFound, protocol.CodeBadMessage, "version is unavailable")
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, protocol.CodeInternalError, "could not read version")
		} else {
			writeJSON(w, http.StatusOK, document)
		}
		return
	}
	before := uint64(math.MaxUint64)
	if value := r.URL.Query().Get("before"); value != "" {
		before, err = strconv.ParseUint(value, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, protocol.CodeBadMessage, "invalid history cursor")
			return
		}
	}
	entries, err := history.ListHistory(ctx, id, before, 30)
	if err != nil {
		writeError(w, http.StatusInternalServerError, protocol.CodeInternalError, "could not list history")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}
