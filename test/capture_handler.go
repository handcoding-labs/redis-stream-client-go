package test

import (
	"context"
	"log/slog"
)

// captureHandler is a custom slog.Handler that captures log messages in a slice for testing purposes.
type captureHandler struct {
	output *[]string
}

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *captureHandler) Handle(ctx context.Context, record slog.Record) error {
	*h.output = append(*h.output, record.Message)
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	return h
}
