package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	clusterrpc "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster/rpc"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

func (s *Server) internalRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /internal/v1/permissions", s.internalUpdatePermission)
	mux.HandleFunc("GET /internal/v1/documents/{docID}", s.internalGetDocument)
	mux.HandleFunc("DELETE /internal/v1/documents/{docID}", s.internalDeleteDocument)
	mux.HandleFunc("GET /internal/v1/documents/{docID}/permissions/{userID}", s.internalGetPermission)
	return mux
}

func (s *Server) internalDeleteDocument(writer http.ResponseWriter, request *http.Request) {
	principal, release, ok := s.authenticateInternalRequest(writer, request)
	if !ok {
		return
	}
	defer release()
	documentID := request.PathValue("docID")
	if !s.requireLocalRoute(writer, request, documentID) {
		return
	}
	if err := s.deleteLocalDocument(request.Context(), principal.UserID, documentID); err != nil {
		writeRoomError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) internalUpdatePermission(writer http.ResponseWriter, request *http.Request) {
	if !s.authenticateCallingNode(writer, request) {
		return
	}
	if !s.allowRequest(writer, request, "internal:"+request.Header.Get(clusterrpc.NodeIDHeader), s.internalRPCRateLimit) {
		return
	}
	principal, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	release, ok := s.acquirePrincipal(writer, request.Context(), principal)
	if !ok {
		return
	}
	defer release()

	var body clusterrpc.PermissionRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		return
	}
	owner, err := s.ownerResolver.EnsureOwner(request.Context(), body.DocumentID)
	if err != nil || !s.isLocalOwner(owner) {
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
		return
	}
	if err := s.changeLocalPermission(request.Context(), principal.UserID, body.DocumentID, body.UserID, body.Role); err != nil {
		writeRoomError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, permissionResponse{DocID: body.DocumentID, UserID: body.UserID, Role: body.Role})
}

func (s *Server) authenticateCallingNode(writer http.ResponseWriter, request *http.Request) bool {
	if s.nodeDirectory == nil || request.TLS == nil || len(request.TLS.VerifiedChains) == 0 || len(request.TLS.PeerCertificates) == 0 {
		writeError(writer, http.StatusForbidden, protocol.CodePermissionDenied, "trusted cluster node certificate is required")
		return false
	}
	certificate := request.TLS.PeerCertificates[0]
	identity, err := clusterrpc.IdentityFromCertificate(certificate)
	if err != nil {
		writeError(writer, http.StatusForbidden, protocol.CodePermissionDenied, "cluster node certificate does not contain a valid process identity")
		return false
	}
	node, exists := s.nodeDirectory.Lookup(identity.NodeID, identity.Incarnation)
	if !exists || !node.IsReady() {
		writeError(writer, http.StatusForbidden, protocol.CodePermissionDenied, "cluster node is not present in trusted membership")
		return false
	}
	return true
}

func (s *Server) internalGetDocument(writer http.ResponseWriter, request *http.Request) {
	principal, release, ok := s.authenticateInternalRequest(writer, request)
	if !ok {
		return
	}
	defer release()
	documentID := request.PathValue("docID")
	if !s.requireLocalRoute(writer, request, documentID) {
		return
	}
	document, err := s.readLocalDocument(request.Context(), principal.UserID, documentID)
	if err != nil {
		writeRoomError(writer, err)
		return
	}
	if !s.requireActiveLocalOwner(writer, request, documentID) {
		return
	}
	writeJSON(writer, http.StatusOK, document)
}

func (s *Server) internalGetPermission(writer http.ResponseWriter, request *http.Request) {
	principal, release, ok := s.authenticateInternalRequest(writer, request)
	if !ok {
		return
	}
	defer release()
	documentID := request.PathValue("docID")
	if !s.requireLocalRoute(writer, request, documentID) {
		return
	}
	userID := request.PathValue("userID")
	role, err := s.readLocalPermission(request.Context(), principal.UserID, documentID, userID)
	if err != nil {
		writeRoomError(writer, err)
		return
	}
	if !s.requireActiveLocalOwner(writer, request, documentID) {
		return
	}
	if role == "" {
		writeError(writer, http.StatusNotFound, protocol.CodeBadMessage, "permission not found")
		return
	}
	writeJSON(writer, http.StatusOK, permissionResponse{DocID: documentID, UserID: userID, Role: role})
}

func (s *Server) authenticateInternalRequest(writer http.ResponseWriter, request *http.Request) (authentication.Principal, func(), bool) {
	if !s.authenticateCallingNode(writer, request) {
		return authentication.Principal{}, nil, false
	}
	if !s.allowRequest(writer, request, "internal:"+request.Header.Get(clusterrpc.NodeIDHeader), s.internalRPCRateLimit) {
		return authentication.Principal{}, nil, false
	}
	principal, ok := s.authenticate(writer, request)
	if !ok {
		return authentication.Principal{}, nil, false
	}
	release, ok := s.acquirePrincipal(writer, request.Context(), principal)
	if !ok {
		return authentication.Principal{}, nil, false
	}
	return principal, release, true
}

func (s *Server) requireLocalRoute(writer http.ResponseWriter, request *http.Request, documentID string) bool {
	if s.ownerResolver == nil {
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
		return false
	}
	owner, err := s.ownerResolver.EnsureOwner(request.Context(), documentID)
	if err != nil || !s.isLocalOwner(owner) {
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
		return false
	}
	return true
}

func (s *Server) requireActiveLocalOwner(writer http.ResponseWriter, request *http.Request, documentID string) bool {
	owner, err := s.ownerResolver.Resolve(request.Context(), documentID)
	if err != nil || owner.State != cluster.OwnerActive || !s.isLocalOwner(owner) {
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
		return false
	}
	return true
}

func (s *Server) acquirePrincipal(writer http.ResponseWriter, ctx context.Context, principal authentication.Principal) (func(), bool) {
	guarded, ok := s.authenticator.(authentication.PrincipalUseAuthenticator)
	if !ok {
		if err := s.authenticator.ValidatePrincipal(ctx, principal); err != nil {
			writeError(writer, http.StatusUnauthorized, codeAuthenticationRequired, "session is not active")
			return nil, false
		}
		return func() {}, true
	}
	release, err := guarded.AcquirePrincipal(ctx, principal)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, codeAuthenticationRequired, "session is not active")
		return nil, false
	}
	return release, true
}

func (s *Server) isLocalOwner(owner cluster.Owner) bool {
	return owner.NodeID == s.localNode.NodeID && owner.Incarnation == s.localNode.Incarnation
}

func (s *Server) writeForwardError(writer http.ResponseWriter, err error) {
	var remote *cluster.RemoteError
	if errors.As(err, &remote) {
		status := remote.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		writeError(writer, status, remote.Code, remote.Message)
		return
	}
	writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "document owner is unavailable")
}
