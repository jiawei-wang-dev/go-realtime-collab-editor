package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

const redactedValue = "[REDACTED]"

type LogConfig struct {
	Writer      io.Writer
	Level       slog.Level
	Service     string
	NodeID      string
	Incarnation string
}

func NewLogger(config LogConfig) *slog.Logger {
	handler := slog.NewJSONHandler(config.Writer, &slog.HandlerOptions{
		Level: config.Level,
		ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
			switch attribute.Key {
			case slog.TimeKey:
				attribute.Key = "timestamp"
			case slog.LevelKey:
				attribute.Value = slog.StringValue(strings.ToUpper(attribute.Value.String()))
			}
			return attribute
		},
	})
	redacting := &redactingHandler{next: handler}
	return slog.New(redacting).With(
		"service", config.Service,
		"node_id", config.NodeID,
		"incarnation", config.Incarnation,
	)
}

type redactingHandler struct {
	next slog.Handler
}

func (handler *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	copy := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		copy.AddAttrs(redactAttribute(attribute))
		return true
	})
	span := trace.SpanContextFromContext(ctx)
	if span.IsValid() {
		copy.AddAttrs(slog.String("trace_id", span.TraceID().String()))
	}
	return handler.next.Handle(ctx, copy)
}

func (handler *redactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		redacted = append(redacted, redactAttribute(attribute))
	}
	return &redactingHandler{next: handler.next.WithAttrs(redacted)}
}

func (handler *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: handler.next.WithGroup(name)}
}

func redactAttribute(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if sensitiveLogKey(attribute.Key) {
		return slog.String(attribute.Key, redactedValue)
	}
	if attribute.Value.Kind() != slog.KindGroup {
		return attribute
	}
	group := attribute.Value.Group()
	redacted := make([]slog.Attr, 0, len(group))
	for _, child := range group {
		redacted = append(redacted, redactAttribute(child))
	}
	return slog.Group(attribute.Key, attrsToAny(redacted)...)
}

func attrsToAny(attributes []slog.Attr) []any {
	values := make([]any, len(attributes))
	for index := range attributes {
		values[index] = attributes[index]
	}
	return values
}

func sensitiveLogKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	for _, fragment := range []string{
		"authorization", "password", "passwd", "secret", "token", "private_key", "dsn", "document_content", "refresh",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}
