package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestLoggerRedactsSecretsAndDocumentContent(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger(LogConfig{Writer: &output, Level: slog.LevelDebug, Service: "test", NodeID: "node-a", Incarnation: "process-a"})
	logger.Info("request",
		"authorization", "Bearer access-secret",
		"refresh_token", "refresh-secret",
		"mysql_dsn", "user:password@tcp(database)",
		"document_content", "private document",
		slog.Group("nested", "password", "password-secret", "safe", "visible"),
	)
	encoded := output.String()
	for _, secret := range []string{"access-secret", "refresh-secret", "user:password", "private document", "password-secret"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("log leaked %q: %s", secret, encoded)
		}
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["service"] != "test" || entry["node_id"] != "node-a" {
		t.Fatalf("missing structured context: %#v", entry)
	}
	if entry["timestamp"] == nil || entry["level"] != "INFO" {
		t.Fatalf("unexpected log envelope: %#v", entry)
	}
}

func TestMetricsUseOnlyBoundedLabelsAndInstrumentHTTP(t *testing.T) {
	metrics := NewMetrics()
	handler := metrics.InstrumentHTTP("public", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusCreated)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/documents/private-doc-id", nil))
	families, err := metrics.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				for _, forbidden := range []string{"user_id", "doc_id", "document_id", "request_id"} {
					if label.GetName() == forbidden {
						t.Fatalf("metric %s contains high-cardinality label %s", family.GetName(), forbidden)
					}
				}
			}
		}
	}
}

func TestHealthReadinessAndConcurrentAccess(t *testing.T) {
	health := NewHealth()
	health.Register("mysql", func(context.Context) error { return nil })
	health.Register("redis", func(context.Context) error { return errors.New("unavailable") })
	health.SetReady(true)
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := httptest.NewRecorder()
			health.Handler(http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Errorf("readiness status = %d", response.Code)
			}
		}()
	}
	wait.Wait()
	health.Stop()
	response := httptest.NewRecorder()
	health.Handler(http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped liveness status = %d", response.Code)
	}
}

func TestTracingDefaultsToNoopAndPropagatesW3CContext(t *testing.T) {
	shutdown, err := ConfigureTracing(context.Background(), TraceConfig{ServiceName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	carrier := propagation.MapCarrier{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	if !trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("W3C trace context was not extracted")
	}
}
