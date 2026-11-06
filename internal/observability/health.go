package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type ReadinessCheck func(context.Context) error

type Health struct {
	live  atomic.Bool
	ready atomic.Bool

	mu     sync.RWMutex
	checks map[string]ReadinessCheck
}

func NewHealth() *Health {
	health := &Health{checks: make(map[string]ReadinessCheck)}
	health.live.Store(true)
	return health
}

func (health *Health) Register(name string, check ReadinessCheck) {
	health.mu.Lock()
	health.checks[name] = check
	health.mu.Unlock()
}

func (health *Health) SetReady(ready bool) { health.ready.Store(ready) }

func (health *Health) Stop() {
	health.ready.Store(false)
	health.live.Store(false)
}

func (health *Health) Handler(metrics http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", health.liveness)
	mux.HandleFunc("GET /readyz", health.readiness)
	mux.Handle("GET /metrics", metrics)
	return mux
}

func (health *Health) liveness(writer http.ResponseWriter, _ *http.Request) {
	if !health.live.Load() {
		writeHealth(writer, http.StatusServiceUnavailable, map[string]string{"process": "stopping"})
		return
	}
	writeHealth(writer, http.StatusOK, map[string]string{"process": "live"})
}

func (health *Health) readiness(writer http.ResponseWriter, request *http.Request) {
	if !health.ready.Load() {
		writeHealth(writer, http.StatusServiceUnavailable, map[string]string{"node": "not_ready"})
		return
	}
	health.mu.RLock()
	names := make([]string, 0, len(health.checks))
	for name := range health.checks {
		names = append(names, name)
	}
	sort.Strings(names)
	checks := make(map[string]ReadinessCheck, len(health.checks))
	for name, check := range health.checks {
		checks[name] = check
	}
	health.mu.RUnlock()

	results := make(map[string]string, len(names)+1)
	results["node"] = "ready"
	status := http.StatusOK
	for _, name := range names {
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		err := checks[name](ctx)
		cancel()
		if err != nil {
			results[name] = "unavailable"
			status = http.StatusServiceUnavailable
		} else {
			results[name] = "ready"
		}
	}
	writeHealth(writer, status, results)
}

func writeHealth(writer http.ResponseWriter, status int, result map[string]string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(result)
}
