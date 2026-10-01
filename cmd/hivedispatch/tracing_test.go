package main

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

func TestNewTracerOffAndMisconfigured(t *testing.T) {
	var stderr bytes.Buffer
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tr, closeTracer := newTracer(func(string) string { return "" }, log, &stderr)
	if tr.Enabled() || stderr.Len() != 0 {
		t.Fatalf("off: enabled %v, stderr %q", tr.Enabled(), stderr.String())
	}
	closeTracer()

	env := map[string]string{"LANGSMITH_TRACING": "true"}
	tr, closeTracer = newTracer(func(k string) string { return env[k] }, log, &stderr)
	defer closeTracer()
	if tr.Enabled() || !strings.Contains(stderr.String(), "LANGSMITH_API_KEY") {
		t.Fatalf("no key: enabled %v, stderr %q", tr.Enabled(), stderr.String())
	}
}

func TestUsesLangGraph(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{{Agents: []config.Agent{{Name: "a", Executor: "claude"}}}}}
	if usesLangGraph(cfg) {
		t.Fatal("no langgraph agent, but usesLangGraph")
	}
	cfg.Repos = append(cfg.Repos, config.RepoConfig{Agents: []config.Agent{{Name: "g", Executor: "langgraph"}}})
	if !usesLangGraph(cfg) {
		t.Fatal("a langgraph agent was missed")
	}
}
