package test

import (
	"context"
	"log/slog"
)

type testHandler struct {
	output *[]string
}

func (h *testHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *testHandler) Handle(ctx context.Context, record slog.Record) error {
	*h.output = append(*h.output, record.Message)
	return nil
}

func (h *testHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *testHandler) WithGroup(name string) slog.Handler {
	return h
}
