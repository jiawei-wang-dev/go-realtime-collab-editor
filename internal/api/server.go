package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

type Config struct {
	Store                 storage.Store
	Authenticator         authentication.Authenticator
	RoomIdleTimeout       time.Duration
	AuthService           *authentication.Service
	SnapshotInterval      int
	MaxHistoryOperations  int
	SnapshotTimeout       time.Duration
	Ownership             cluster.OwnershipCoordinator
	OwnerResolver         cluster.OwnerResolver
	LocalNode             cluster.Node
	NodeDirectory         cluster.NodeDirectory
	PermissionForwarder   cluster.PermissionForwarder
	RoomObserver          room.Observer
	AuthenticationTimeout time.Duration
	RateLimiter           RateLimiter
	AllowedOrigins        []string
	LoginRateLimit        int
	RefreshRateLimit      int
	InternalRPCRateLimit  int
	RateLimitWindow       time.Duration
}

type Server struct {
	store                 storage.Store
	manager               *room.Manager
	authenticator         authentication.Authenticator
	authService           *authentication.Service
	handler               http.Handler
	internal              http.Handler
	ownerResolver         cluster.OwnerResolver
	localNode             cluster.Node
	nodeDirectory         cluster.NodeDirectory
	forwarder             cluster.PermissionForwarder
	authenticationTimeout time.Duration
	rateLimiter           RateLimiter
	allowedOrigins        map[string]struct{}
	loginRateLimit        int
	refreshRateLimit      int
	internalRPCRateLimit  int
	rateLimitWindow       time.Duration
}

func NewServer() *Server {
	return NewServerWithConfig(Config{})
}

func NewServerWithConfig(config Config) *Server {
	config = withDefaults(config)
	options := []room.ManagerOption{
		room.WithIdleTimeout(config.RoomIdleTimeout),
		room.WithSnapshotPolicy(config.SnapshotInterval, config.MaxHistoryOperations, config.SnapshotTimeout),
	}
	if config.Ownership != nil {
		options = append(options, room.WithOwnership(config.Ownership))
	}
	if config.RoomObserver != nil {
		options = append(options, room.WithObserver(config.RoomObserver))
	}
	manager := room.NewManager(config.Store, options...)
	server := &Server{
		store: config.Store, manager: manager, authenticator: config.Authenticator, authService: config.AuthService,
		ownerResolver: config.OwnerResolver, localNode: config.LocalNode, nodeDirectory: config.NodeDirectory,
		forwarder:             config.PermissionForwarder,
		authenticationTimeout: config.AuthenticationTimeout,
		rateLimiter:           config.RateLimiter, loginRateLimit: config.LoginRateLimit,
		refreshRateLimit: config.RefreshRateLimit, internalRPCRateLimit: config.InternalRPCRateLimit,
		rateLimitWindow: config.RateLimitWindow, allowedOrigins: make(map[string]struct{}, len(config.AllowedOrigins)),
	}
	for _, origin := range config.AllowedOrigins {
		server.allowedOrigins[origin] = struct{}{}
	}
	server.handler = server.routes()
	server.internal = server.internalRoutes()
	return server
}

func withDefaults(config Config) Config {
	if config.Store == nil {
		config.Store = storage.NewMemory()
	}
	if config.Authenticator == nil {
		config.Authenticator = authentication.DevelopmentAuthenticator{}
	}
	if config.RoomIdleTimeout <= 0 {
		config.RoomIdleTimeout = 30 * time.Second
	}
	if config.AuthenticationTimeout <= 0 {
		config.AuthenticationTimeout = 5 * time.Second
	}
	if config.LoginRateLimit <= 0 {
		config.LoginRateLimit = 20
	}
	if config.RefreshRateLimit <= 0 {
		config.RefreshRateLimit = 60
	}
	if config.InternalRPCRateLimit <= 0 {
		config.InternalRPCRateLimit = 300
	}
	if config.RateLimitWindow <= 0 {
		config.RateLimitWindow = time.Minute
	}
	return config
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	if s.authService != nil {
		mux.HandleFunc("POST /api/auth/register", s.register)
		mux.HandleFunc("POST /api/auth/login", s.login)
		mux.HandleFunc("POST /api/auth/refresh", s.refresh)
		mux.HandleFunc("POST /api/auth/logout", s.logout)
		mux.HandleFunc("GET /api/auth/me", s.me)
	}
	mux.HandleFunc("POST /api/documents", s.createDocument)
	mux.HandleFunc("GET /api/documents/{docID}", s.getDocument)
	mux.HandleFunc("GET /api/documents/{docID}/permissions/{userID}", s.getPermission)
	mux.HandleFunc("PUT /api/documents/{docID}/permissions/{userID}", s.updatePermission)
	return mux
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !s.allowOrigin(writer, request) {
		return
	}
	s.handler.ServeHTTP(writer, request)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.manager.Shutdown(ctx)
}

func (s *Server) RoomManager() *room.Manager { return s.manager }

func (s *Server) InternalHandler() http.Handler { return s.internal }

type createDocumentRequest struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	OwnerID string `json:"owner_id,omitempty"`
}

func (s *Server) createDocument(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	var body createDocumentRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		return
	}
	if body.ID == "" {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, "document id is required")
		return
	}
	document, err := s.store.CreateDocument(request.Context(), body.ID, body.Content, principal.UserID)
	if err != nil {
		if errors.Is(err, storage.ErrAlreadyExists) {
			writeError(writer, http.StatusConflict, protocol.CodeBadMessage, "document already exists")
			return
		}
		writeError(writer, http.StatusInternalServerError, protocol.CodeInternalError, "could not create document")
		return
	}
	writeJSON(writer, http.StatusCreated, document)
}

func (s *Server) getDocument(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	documentID := request.PathValue("docID")
	if s.ownerResolver != nil {
		owner, err := s.ownerResolver.EnsureOwner(request.Context(), documentID)
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
			return
		}
		if !s.isLocalOwner(owner) {
			if s.forwarder == nil {
				writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
				return
			}
			document, err := s.forwarder.ForwardDocument(request.Context(), owner, request.Header.Get("Authorization"), documentID)
			if err != nil {
				s.writeForwardError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, document)
			return
		}
	}
	document, err := s.readLocalDocument(request.Context(), principal.UserID, documentID)
	if err != nil {
		writeRoomError(writer, err)
		return
	}
	if s.ownerResolver != nil && !s.localOwnerIsActive(request.Context(), documentID) {
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, document)
}

func (s *Server) readLocalDocument(ctx context.Context, userID, documentID string) (collab.Document, error) {
	targetRoom, err := s.manager.Room(documentID)
	if err != nil {
		return collab.Document{}, err
	}
	return targetRoom.ReadDocumentContext(ctx, userID)
}

func (s *Server) getPermission(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	docID := request.PathValue("docID")
	userID := request.PathValue("userID")
	if s.ownerResolver != nil {
		owner, err := s.ownerResolver.EnsureOwner(request.Context(), docID)
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
			return
		}
		if !s.isLocalOwner(owner) {
			if s.forwarder == nil {
				writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
				return
			}
			role, err := s.forwarder.ForwardGetPermission(request.Context(), owner, request.Header.Get("Authorization"), docID, userID)
			if err != nil {
				s.writeForwardError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, permissionResponse{DocID: docID, UserID: userID, Role: role})
			return
		}
	}
	role, err := s.readLocalPermission(request.Context(), principal.UserID, docID, userID)
	if err != nil {
		writeRoomError(writer, err)
		return
	}
	if s.ownerResolver != nil && !s.localOwnerIsActive(request.Context(), docID) {
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
		return
	}
	if role == "" {
		writeError(writer, http.StatusNotFound, protocol.CodeBadMessage, "permission not found")
		return
	}
	writeJSON(writer, http.StatusOK, permissionResponse{DocID: docID, UserID: userID, Role: role})
}

func (s *Server) readLocalPermission(ctx context.Context, actorID, documentID, userID string) (permission.Role, error) {
	targetRoom, err := s.manager.Room(documentID)
	if err != nil {
		return "", err
	}
	return targetRoom.PermissionContext(ctx, actorID, userID)
}

func (s *Server) localOwnerIsActive(ctx context.Context, documentID string) bool {
	owner, err := s.ownerResolver.Resolve(ctx, documentID)
	return err == nil && owner.State == cluster.OwnerActive && s.isLocalOwner(owner)
}

type updatePermissionRequest struct {
	Role permission.Role `json:"role"`
}

type permissionResponse struct {
	DocID  string          `json:"doc_id"`
	UserID string          `json:"user_id"`
	Role   permission.Role `json:"role"`
}

func (s *Server) updatePermission(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.authenticate(writer, request)
	if !ok {
		return
	}

	var body updatePermissionRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		return
	}

	docID := request.PathValue("docID")
	userID := request.PathValue("userID")
	if s.ownerResolver != nil {
		owner, err := s.ownerResolver.EnsureOwner(request.Context(), docID)
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
			return
		}
		if !s.isLocalOwner(owner) {
			if s.forwarder == nil {
				writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
				return
			}
			err := s.forwarder.ForwardPermission(request.Context(), owner, request.Header.Get("Authorization"), docID, userID, body.Role)
			if err != nil {
				s.writeForwardError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, permissionResponse{DocID: docID, UserID: userID, Role: body.Role})
			return
		}
	}
	if err := s.changeLocalPermission(request.Context(), principal.UserID, docID, userID, body.Role); err != nil {
		writeRoomError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, permissionResponse{DocID: docID, UserID: userID, Role: body.Role})
}

func (s *Server) changeLocalPermission(ctx context.Context, actorID, docID, userID string, role permission.Role) error {
	targetRoom, err := s.manager.Room(docID)
	if err != nil {
		return err
	}
	if s.ownerResolver != nil {
		owner, err := s.ownerResolver.Resolve(ctx, docID)
		if err != nil || owner.State != cluster.OwnerActive || !s.isLocalOwner(owner) {
			return &room.CodedError{Code: protocol.CodeServiceUnavailable, Message: "document owner is not active"}
		}
	}
	return targetRoom.ChangePermissionContext(ctx, actorID, userID, role)
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
		switch coded.Code {
		case protocol.CodePermissionDenied:
			status = http.StatusForbidden
		case protocol.CodeVersionConflict, protocol.CodeDuplicateRequest:
			status = http.StatusConflict
		case protocol.CodeInternalError:
			status = http.StatusInternalServerError
		case protocol.CodeServiceUnavailable:
			status = http.StatusServiceUnavailable
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
