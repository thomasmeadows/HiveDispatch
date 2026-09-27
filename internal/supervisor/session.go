package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

// Session is one conversation with the supervisor, independent of its
// front end: the terminal REPL and the web UI each drive one. It is not
// safe for concurrent turns; the front end serialises them.
type Session struct {
	agent      *Agent
	mem        *Memory
	session    string
	modelName  string
	configPath string
	exe        string
	now        func() time.Time

	checkOutput string // hivedispatch check output, captured once per turn (see RefreshCheck)
}

// SessionOptions wires a Session.
type SessionOptions struct {
	WorkerConfigPath string
	Provider, Model  string // flag overrides
	Resume           bool   // resume the newest session
	Session          string // resume this session file
	Exe              string // the hivedispatch binary to re-exec
	Getenv           func(string) string
	Now              func() time.Time
	// Confirm is asked before any tool changes something; false declines.
	Confirm func(prompt string) bool
	// Events receives the agent's progress; may be nil.
	Events func(Event)
	// ChatModel, when set, answers instead of the configured provider.
	ChatModel model.Model
}

// NewSession loads the supervisor config, builds the model, tools and
// agent, and resumes a conversation when asked.
func NewSession(ctx context.Context, o SessionOptions) (*Session, error) {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Confirm == nil {
		o.Confirm = func(string) bool { return false }
	}
	stepBudget, maxTokens := defaultStepBudget, defaultMaxTokens
	m := o.ChatModel
	if m == nil {
		cfg, err := LoadConfig(o.WorkerConfigPath, o.Getenv)
		if err != nil {
			return nil, err
		}
		cfg.Override(o.Provider, o.Model)
		if cfg.Provider == "ollama" && cfg.FromEnv {
			if err := ProbeOllama(ctx, cfg.BaseURL); err != nil {
				return nil, fmt.Errorf("no model configured: set ANTHROPIC_API_KEY, OPENAI_API_KEY, DEEPSEEK_API_KEY or HF_TOKEN, run Ollama (tried %s: %w), or set supervisor.provider in %s", cfg.BaseURL, err, o.WorkerConfigPath)
			}
		}
		if m, err = NewModel(cfg, o.Getenv); err != nil {
			return nil, err
		}
		stepBudget, maxTokens = cfg.StepBudget, cfg.MaxTokens
	}
	mem := NewMemory(Dir(o.WorkerConfigPath))
	s := &Session{mem: mem, modelName: m.Name(), configPath: o.WorkerConfigPath, exe: o.Exe, now: o.Now}
	tools := []Tool{
		NewReadConfig(o.WorkerConfigPath),
		NewWriteConfig(o.WorkerConfigPath, o.Confirm),
		NewReadDoc(),
		NewReadRepoFile(o.WorkerConfigPath),
		NewRunHivedispatch(o.Exe, o.WorkerConfigPath, o.Confirm),
		NewRemember(mem, o.Now),
	}
	s.agent = &Agent{Model: m, Tools: tools, System: s.system, StepBudget: stepBudget, MaxTokens: maxTokens, Events: o.Events}
	switch {
	case o.Session != "":
		h, err := mem.LoadSession(o.Session)
		if err != nil {
			return nil, err
		}
		s.agent.SetHistory(h)
		s.session = o.Session
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
		s.agent.SetHistory(h)
		s.session = name
	default:
		s.session = mem.NewSessionName(o.Now())
	}
	return s, nil
}

// ModelName is the provider and model answering.
func (s *Session) ModelName() string { return s.modelName }

// Name is the session file the conversation is saved to.
func (s *Session) Name() string { return s.session }

// Notes returns the supervisor's notes file.
func (s *Session) Notes() (string, error) { return s.mem.Notes() }

// History returns the conversation so far.
func (s *Session) History() []model.Message { return s.agent.History() }

// RefreshCheck captures `hivedispatch check` for the system prompt. Call it
// once per turn, with the turn's context, rather than before every model
// call: a 20-step turn would otherwise re-run the binary 21 times.
func (s *Session) RefreshCheck(ctx context.Context) {
	s.checkOutput = CheckOutput(ctx, s.exe, s.configPath)
}

// Turn sends one user message through the agent and returns its reply.
func (s *Session) Turn(ctx context.Context, msg string) (string, error) {
	return s.agent.Turn(ctx, msg)
}

// Save writes the conversation to its session file.
func (s *Session) Save() error {
	return s.mem.SaveSession(s.session, s.agent.History())
}

// Reset starts a new conversation; the notes are kept.
func (s *Session) Reset() {
	s.agent.SetHistory(nil)
	s.session = s.mem.NewSessionName(s.now())
}

// system builds the system prompt fresh from the current config, notes and
// the check output captured by RefreshCheck.
func (s *Session) system() string {
	notes, n := s.mem.NotesForPrompt()
	_, statErr := os.Stat(s.configPath)
	return BuildSystem(PromptInput{
		ConfigPath: s.configPath, ConfigExists: statErr == nil, ModelName: s.modelName,
		Notes: notes, NoteLines: n, CheckOutput: s.checkOutput,
	})
}
