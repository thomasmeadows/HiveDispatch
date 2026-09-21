package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Options wires a supervisor session.
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
}

// New loads the supervisor config, builds the model, tools and agent, and
// resumes a session when asked.
func New(ctx context.Context, o Options) (*REPL, error) {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	cfg, err := LoadConfig(ConfigPath(o.WorkerConfigPath), o.Getenv)
	if err != nil {
		return nil, err
	}
	cfg.Override(o.Provider, o.Model)
	if cfg.Provider == "ollama" && cfg.FromEnv {
		if err := ProbeOllama(ctx, cfg.BaseURL); err != nil {
			return nil, fmt.Errorf("no model configured: set ANTHROPIC_API_KEY, OPENAI_API_KEY, DEEPSEEK_API_KEY or HF_TOKEN, run Ollama (tried %s: %w), or write %s", cfg.BaseURL, err, ConfigPath(o.WorkerConfigPath))
		}
	}
	m, err := NewModel(cfg, o.Getenv)
	if err != nil {
		return nil, err
	}
	mem := NewMemory(Dir(o.WorkerConfigPath))
	r := &REPL{mem: mem, modelName: m.Name(), configPath: o.WorkerConfigPath, exe: o.Exe,
		stdin: o.Stdin, stdout: o.Stdout, stderr: o.Stderr, interactive: o.Interactive, now: o.Now}
	r.lines = newLineReader(o.Stdin)
	tools := []Tool{
		NewReadConfig(o.WorkerConfigPath),
		NewWriteConfig(o.WorkerConfigPath, r.Confirm),
		NewReadDoc(),
		NewReadRepoFile(o.WorkerConfigPath),
		NewRunHivedispatch(o.Exe, o.WorkerConfigPath, r.Confirm),
		NewRemember(mem, o.Now),
	}
	r.agent = &Agent{Model: m, Tools: tools, System: r.system, StepBudget: cfg.StepBudget, MaxTokens: cfg.MaxTokens, Events: r.onEvent}
	switch {
	case o.Session != "":
		h, err := mem.LoadSession(o.Session)
		if err != nil {
			return nil, err
		}
		r.agent.SetHistory(h)
		r.session = o.Session
	case o.Resume:
		name, err := mem.NewestSession()
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, errors.New("no earlier session to resume")
		}
		h, err := mem.LoadSession(name)
		if err != nil {
			return nil, err
		}
		r.agent.SetHistory(h)
		r.session = name
	default:
		r.session = mem.NewSessionName(o.Now())
	}
	return r, nil
}
