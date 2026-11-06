package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

const (
	codeAuthenticationRequired = "AUTHENTICATION_REQUIRED"
	codeInvalidCredentials     = "INVALID_CREDENTIALS"
	codeUsernameExists         = "USERNAME_EXISTS"
)

type credentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
	RequestID    string `json:"request_id,omitempty"`
}

func (s *Server) authenticate(writer http.ResponseWriter, request *http.Request) (authentication.Principal, bool) {
	authContext, cancel := context.WithTimeout(request.Context(), s.authenticationTimeout)
	defer cancel()
	principal, err := s.authenticator.AuthenticateHTTP(request.Clone(authContext))
	if err != nil {
		writeError(writer, http.StatusUnauthorized, codeAuthenticationRequired, "valid authentication is required")
		return authentication.Principal{}, false
	}
	requestWithPrincipal := request.WithContext(authentication.WithPrincipal(request.Context(), principal))
	*request = *requestWithPrincipal
	return principal, true
}
func (s *Server) register(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "register", s.loginRateLimit) {
		return
	}
	var body credentialsRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		return
	}
	user, err := s.authService.Register(request.Context(), body.Username, body.Password)
	if err != nil {
		var validation authentication.ValidationError
		switch {
		case errors.As(err, &validation):
			writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		case errors.Is(err, authentication.ErrUsernameExists):
			writeError(writer, http.StatusConflict, codeUsernameExists, "username already exists")
		default:
			writeError(writer, http.StatusInternalServerError, protocol.CodeInternalError, "could not register user")
		}
		return
	}
	writeJSON(writer, http.StatusCreated, user)
}
func (s *Server) login(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "login", s.loginRateLimit) {
		return
	}
	var body credentialsRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		return
	}
	pair, err := s.authService.Login(request.Context(), body.Username, body.Password)
	if err != nil {
		if errors.Is(err, authentication.ErrInvalidCredentials) {
			writeError(writer, http.StatusUnauthorized, codeInvalidCredentials, "invalid username or password")
			return
		}
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeInternalError, "authentication service unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, pair)
}
func (s *Server) refresh(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "refresh", s.refreshRateLimit) {
		return
	}
	var body refreshRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, err.Error())
		return
	}
	var pair authentication.TokenPair
	var err error
	if body.RequestID == "" {
		pair, err = s.authService.Refresh(request.Context(), body.RefreshToken)
	} else {
		pair, err = s.authService.RefreshWithRequestID(request.Context(), body.RefreshToken, body.RequestID)
	}
	if err != nil {
		writeError(writer, http.StatusUnauthorized, codeAuthenticationRequired, "invalid refresh token")
		return
	}
	writeJSON(writer, http.StatusOK, pair)
}
func (s *Server) logout(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if err := s.authService.Logout(request.Context(), principal); err != nil {
		writeError(writer, http.StatusUnauthorized, codeAuthenticationRequired, "session is not active")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
func (s *Server) me(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	user, err := s.authService.Me(request.Context(), principal)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, codeAuthenticationRequired, "session is not active")
		return
	}
	writeJSON(writer, http.StatusOK, user)
}
