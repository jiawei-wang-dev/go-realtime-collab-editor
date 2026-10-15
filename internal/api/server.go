package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	ws "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/websocket"
	"io"
	"net/http"
)

type Server struct {
	store   *storage.Memory
	manager *room.Manager
	handler http.Handler
}

func NewServer() *Server {
	store := storage.NewMemory()
	manager := room.NewManager(store)
	server := &Server{store: store, manager: manager}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/documents", server.createDocument)
	mux.HandleFunc("GET /api/documents/{docID}", server.getDocument)
	mux.HandleFunc("GET /api/documents/{docID}/permissions/{userID}", server.getPermission)
	mux.HandleFunc("PUT /api/documents/{docID}/permissions/{userID}", server.updatePermission)
	mux.Handle("GET /ws", ws.NewHandler(manager))
	server.handler = mux
	return server
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

type createDocumentRequest struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	OwnerID string `json:"owner_id"`
}

func (s *Server) createDocument(writer http.ResponseWriter, request *http.Request) {
	var body createDocumentRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		return
	}
	document, err := s.store.CreateDocument(body.ID, body.Content, body.OwnerID)
	if err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		return
	}
	writeJSON(writer, http.StatusCreated, document)
}

func (s *Server) getDocument(writer http.ResponseWriter, request *http.Request) {
	actorID := request.Header.Get("X-User-ID")
	if actorID == "" {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, "X-User-ID header is required")
		return
	}
	targetRoom, err := s.manager.Room(request.PathValue("docID"))
	if err != nil {
		writeRoomError(writer, err)
		return
	}
	document, err := targetRoom.ReadDocument(actorID)
	if err != nil {
		writeRoomError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, document)
}

func (s *Server) getPermission(writer http.ResponseWriter, request *http.Request) {
	actorID := request.Header.Get("X-User-ID")
	if actorID == "" {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, "X-User-ID header is required")
		return
	}
	docID := request.PathValue("docID")
	userID := request.PathValue("userID")
	targetRoom, err := s.manager.Room(docID)
	if err != nil {
		writeRoomError(writer, err)
		return
	}
	role, err := targetRoom.Permission(actorID, userID)
	if err != nil {
		writeRoomError(writer, err)
		return
	}
	if role == "" {
		writeError(writer, http.StatusNotFound, protocol.CodeBadMessage, "permission not found")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"doc_id":  docID,
		"user_id": userID,
		"role":    role,
	})
}

type updatePermissionRequest struct {
	Role permission.Role `json:"role"`
}

func (s *Server) updatePermission(writer http.ResponseWriter, request *http.Request) {
	actorID := request.Header.Get("X-User-ID")
	if actorID == "" {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, "X-User-ID header is required")
		return
	}

	var body updatePermissionRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		return
	}

	docID := request.PathValue("docID")
	userID := request.PathValue("userID")
	targetRoom, err := s.manager.Room(docID)
	if err != nil {
		writeRoomError(writer, err)
		return
	}
	if err := targetRoom.ChangePermission(actorID, userID, body.Role); err != nil {
		writeRoomError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"doc_id":  docID,
		"user_id": userID,
		"role":    body.Role,
	})
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid JSON: request body must contain exactly one value")
	}
	return nil
}

func writeRoomError(writer http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrDocumentNotFound) {
		writeError(writer, http.StatusNotFound, protocol.CodeBadMessage, "document not found")
		return
	}
	var coded *room.CodedError
	if errors.As(err, &coded) {
		status := http.StatusBadRequest
		if coded.Code == protocol.CodePermissionDenied {
			status = http.StatusForbidden
		}
		writeError(writer, status, coded.Code, coded.Message)
		return
	}
	writeError(writer, http.StatusInternalServerError, protocol.CodeInternalError, "internal server error")
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, protocol.Error(code, message))
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
