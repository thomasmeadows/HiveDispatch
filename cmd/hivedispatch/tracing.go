package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/trace"
	"github.com/thomasmeadows/hivedispatch/internal/trace/langsmith"
)

// tracerCloseTimeout bounds the final flush to LangSmith on the way out.
const tracerCloseTimeout = 5 * time.Second

// newTracer builds the LangSmith tracer the environment asks for (nil when
// tracing is off) and a func that flushes it. A misconfiguration is a
// warning, never a reason not to run.
func newTracer(getenv func(string) string, log *slog.Logger, stderr io.Writer) (*trace.Tracer, func()) {
	tr, err := langsmith.FromEnv(getenv, log)
	if err != nil {
		fmt.Fprintln(stderr, "warning:", err)
	}
	return tr, func() {
		ctx, cancel := context.WithTimeout(context.Background(), tracerCloseTimeout)
		defer cancel()
		_ = tr.Close(ctx) // the exporter logs what it could not send
	}
}
