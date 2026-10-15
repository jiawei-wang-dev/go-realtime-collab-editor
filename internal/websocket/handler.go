package websocket

import (
	"errors"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"net/http"

	gorilla "github.com/gorilla/websocket"
)

type Handler struct {
	manager  *room.Manager
	upgrader gorilla.Upgrader
}

func NewHandler(manager *room.Manager) *Handler {
	return &Handler{
		manager: manager,
		upgrader: gorilla.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	docID := request.URL.Query().Get("doc_id")
	userID := request.URL.Query().Get("user_id")
	if docID == "" || userID == "" {
		http.Error(writer, "doc_id and user_id are required", http.StatusBadRequest)
		return
	}

	targetRoom, err := h.manager.Room(docID)
	if err != nil {
		if errors.Is(err, storage.ErrDocumentNotFound) {
			http.Error(writer, "document not found", http.StatusNotFound)
			return
		}
		http.Error(writer, "could not open document", http.StatusInternalServerError)
		return
	}

	connection, err := h.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	client := newClient(connection, targetRoom, docID, userID)
	go client.writePump()
	client.readPump()
}
