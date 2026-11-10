package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/execution"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

const codeExecutionLimit = "EXECUTION_LIMIT"

func (s *Server) runDocument(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	role, err := s.store.GetPermission(request.Context(), request.PathValue("docID"), principal.UserID)
	if err != nil || !role.CanWrite() {
		writeError(writer, http.StatusForbidden, protocol.CodePermissionDenied, "write permission required")
		return
	}
	if s.executionRunner == nil {
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "code execution is unavailable")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, s.executionLimits.MaxSourceBytes+4096)
	var body execution.Request
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, "invalid execution request")
		return
	}
	if body.Language != execution.LanguageGo || body.Source == "" || int64(len(body.Source)) > s.executionLimits.MaxSourceBytes {
		writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, "a non-empty Go source file within the configured size limit is required")
		return
	}

	release, acquired := s.executionGate.acquire(time.Now(), principal.UserID)
	if !acquired {
		writeError(writer, http.StatusTooManyRequests, codeExecutionLimit, "execution concurrency or rate limit exceeded")
		return
	}
	defer release()

	runContext, cancel := context.WithTimeout(request.Context(), s.executionLimits.Timeout+2*time.Second)
	defer cancel()
	result, err := s.executionRunner.Run(runContext, body)
	if err != nil {
		if errors.Is(err, execution.ErrInvalidRequest) {
			writeError(writer, http.StatusBadRequest, protocol.CodeBadMessage, "invalid execution request")
			return
		}
		writeError(writer, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, "code execution is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

type executionRate struct {
	windowStart time.Time
	count       int
}

type executionGate struct {
	mu          sync.Mutex
	active      map[string]int
	global      int
	globalLimit int
	userLimit   int
	rateLimit   int
	window      time.Duration
	rates       map[string]executionRate
}

func newExecutionGate(globalLimit, userLimit, rateLimit int, window time.Duration) *executionGate {
	return &executionGate{
		active: make(map[string]int), globalLimit: globalLimit, userLimit: userLimit,
		rateLimit: rateLimit, window: window, rates: make(map[string]executionRate),
	}
}

func (gate *executionGate) acquire(now time.Time, userID string) (func(), bool) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	rate := gate.rates[userID]
	if rate.windowStart.IsZero() || now.Sub(rate.windowStart) >= gate.window {
		rate = executionRate{windowStart: now}
	}
	if gate.global >= gate.globalLimit || gate.active[userID] >= gate.userLimit || rate.count >= gate.rateLimit {
		return nil, false
	}
	rate.count++
	gate.rates[userID] = rate
	gate.global++
	gate.active[userID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			defer gate.mu.Unlock()
			gate.global--
			gate.active[userID]--
			if gate.active[userID] == 0 {
				delete(gate.active, userID)
			}
		})
	}, true
}
