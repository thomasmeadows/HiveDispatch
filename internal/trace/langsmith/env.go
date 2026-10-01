package langsmith

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/trace"
)

// env reads LangSmith's variable, falling back to LangChain's older name.
func env(getenv func(string) string, name, legacy string) string {
	if v := getenv(name); v != "" {
		return v
	}
	if legacy == "" {
		return ""
	}
	return getenv(legacy)
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

type settings struct {
	on                      bool
	key, endpoint, project  string
	workspace               string
	hideInputs, hideOutputs bool
}

func read(getenv func(string) string) settings {
	s := settings{
		on:          truthy(env(getenv, "LANGSMITH_TRACING", "LANGCHAIN_TRACING_V2")),
		key:         env(getenv, "LANGSMITH_API_KEY", "LANGCHAIN_API_KEY"),
		endpoint:    env(getenv, "LANGSMITH_ENDPOINT", "LANGCHAIN_ENDPOINT"),
		project:     env(getenv, "LANGSMITH_PROJECT", "LANGCHAIN_PROJECT"),
		workspace:   env(getenv, "LANGSMITH_WORKSPACE_ID", ""),
		hideInputs:  truthy(env(getenv, "LANGSMITH_HIDE_INPUTS", "LANGCHAIN_HIDE_INPUTS")),
		hideOutputs: truthy(env(getenv, "LANGSMITH_HIDE_OUTPUTS", "LANGCHAIN_HIDE_OUTPUTS")),
	}
	if s.endpoint == "" {
		s.endpoint = DefaultEndpoint
	}
	if s.project == "" {
		s.project = DefaultProject
	}
	return s
}

// FromEnv builds the tracer the environment asks for: nil (tracing off)
// unless LANGSMITH_TRACING is true. Tracing on without LANGSMITH_API_KEY is
// an error, and the tracer is nil.
func FromEnv(getenv func(string) string, log *slog.Logger) (*trace.Tracer, error) {
	s := read(getenv)
	if !s.on {
		return nil, nil
	}
	if s.key == "" {
		return nil, errors.New("LANGSMITH_TRACING is on but LANGSMITH_API_KEY is not set; tracing is off")
	}
	exp := New(Config{Endpoint: s.endpoint, APIKey: s.key, WorkspaceID: s.workspace, Project: s.project, Log: log})
	return trace.New(exp, trace.Options{HideInputs: s.hideInputs, HideOutputs: s.hideOutputs}), nil
}

// Describe is one line for `hivedispatch check`. It never includes the key.
func Describe(getenv func(string) string) string {
	s := read(getenv)
	switch {
	case !s.on:
		return "tracing: off (set LANGSMITH_TRACING=true and LANGSMITH_API_KEY to send traces to LangSmith)"
	case s.key == "":
		return "tracing: LANGSMITH_TRACING is on but LANGSMITH_API_KEY is not set"
	}
	var hidden []string
	if s.hideInputs {
		hidden = append(hidden, "inputs")
	}
	if s.hideOutputs {
		hidden = append(hidden, "outputs")
	}
	d := fmt.Sprintf("tracing: LangSmith project %q at %s", s.project, s.endpoint)
	if len(hidden) > 0 {
		d += " (" + strings.Join(hidden, " and ") + " hidden)"
	}
	return d
}
