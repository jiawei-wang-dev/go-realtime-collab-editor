package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

const codeRateLimited = "RATE_LIMITED"

type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

func (s *Server) allowRequest(writer http.ResponseWriter, request *http.Request, scope string, limit int) bool {
	if s.rateLimiter == nil {
		return true
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || host == "" {
		host = "unknown"
	}
	allowed, err := s.rateLimiter.Allow(request.Context(), scope+":"+host, limit, s.rateLimitWindow)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "rate limit service unavailable")
		return false
	}
	if !allowed {
		writer.Header().Set("Retry-After", retryAfterSeconds(s.rateLimitWindow))
		writeError(writer, http.StatusTooManyRequests, codeRateLimited, "request rate limit exceeded")
		return false
	}
	return true
}

func retryAfterSeconds(window time.Duration) string {
	seconds := int64(window / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return strconv.FormatInt(seconds, 10)
}

func (s *Server) allowOrigin(writer http.ResponseWriter, request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if _, allowed := s.allowedOrigins[origin]; !allowed {
		writeError(writer, http.StatusForbidden, protocol.CodePermissionDenied, "origin is not allowed")
		return false
	}
	writer.Header().Set("Access-Control-Allow-Origin", origin)
	writer.Header().Set("Access-Control-Allow-Credentials", "true")
	writer.Header().Set("Vary", "Origin")
	if request.Method == http.MethodOptions {
		writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		writer.Header().Set("Access-Control-Max-Age", "600")
		writer.WriteHeader(http.StatusNoContent)
		return false
	}
	return true
}
