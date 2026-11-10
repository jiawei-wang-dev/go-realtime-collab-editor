package execution

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

func NewHandler(runner Runner, token string, limits Limits) (http.Handler, error) {
	if runner == nil || token == "" {
		return nil, errors.New("runner and authentication token are required")
	}
	if limits.MaxSourceBytes <= 0 {
		return nil, errors.New("positive source limit is required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/run", func(writer http.ResponseWriter, request *http.Request) {
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, limits.MaxSourceBytes+4096)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var body Request
		if err := decoder.Decode(&body); err != nil {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		if body.Language != LanguageGo || strings.TrimSpace(body.Source) == "" || int64(len(body.Source)) > limits.MaxSourceBytes {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		result, err := runner.Run(request.Context(), body)
		if err != nil {
			http.Error(writer, "runner unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(result)
	})
	return mux, nil
}
