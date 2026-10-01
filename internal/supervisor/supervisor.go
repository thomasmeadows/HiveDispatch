package supervisor

import (
	"context"
	"io"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/trace"
)

// Options wires a terminal supervisor session.
type Options struct {
	WorkerConfigPath string
	Provider, Model  string // flag overrides
	Resume           bool   // resume the newest session
	Session          string // resume this session file
	Exe              string // the hivedispatch binary to re-exec
	Stdin            io.Reader
	Stdout, Stderr   io.Writer
	Interactive      bool // stdin is a terminal: prompts, confirmations, spinner
	Getenv           func(string) string
	Now              func() time.Time
	Tracer           *trace.Tracer // nil = no tracing
}

// New builds a Session with the terminal as its front end.
func New(ctx context.Context, o Options) (*REPL, error) {
	r := &REPL{stdin: o.Stdin, stdout: o.Stdout, stderr: o.Stderr, interactive: o.Interactive}
	r.lines = newLineReader(o.Stdin)
	s, err := NewSession(ctx, SessionOptions{
		WorkerConfigPath: o.WorkerConfigPath, Provider: o.Provider, Model: o.Model,
		Resume: o.Resume, Session: o.Session, Exe: o.Exe, Getenv: o.Getenv, Now: o.Now, Tracer: o.Tracer,
		Confirm: r.Confirm, Events: r.onEvent,
	})
	if err != nil {
		return nil, err
	}
	r.Session = s
	return r, nil
}
