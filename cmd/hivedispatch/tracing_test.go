package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

func TestGraphInstallCommand(t *testing.T) {
	if got := graphInstallCommand("v0.3.0"); got != `pipx install "git+https://github.com/thomasmeadows/HiveDispatch@v0.3.0#subdirectory=graph"` {
		t.Errorf("tagged build: %s", got)
	}
	if got := graphInstallCommand("dev"); got != `pipx install ./graph` {
		t.Errorf("dev build: %s", got)
	}
}

func TestUsesDeepCode(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{{Agents: []config.Agent{{Name: "a", Executor: "claude"}}}}}
	if usesDeepCode(cfg) {
		t.Fatal("no deepcode agent")
	}
	cfg.Repos[0].Agents = append(cfg.Repos[0].Agents, config.Agent{Name: "g", Executor: "langgraph", CodeWith: "deepcode"})
	if !usesDeepCode(cfg) {
		t.Fatal("code_with deepcode missed")
	}
}

func TestDeepCodeSettingsCheck(t *testing.T) {
	home := t.TempDir()
	if got := deepCodeSettings(home); !strings.Contains(got, "missing") {
		t.Fatalf("no settings: %q", got)
	}
	dir := filepath.Join(home, ".deepcode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"env":{"MODEL":"deepseek-flash","API_KEY":"sk-secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := deepCodeSettings(home)
	if !strings.Contains(got, "deepseek-flash") || strings.Contains(got, "sk-secret") {
		t.Fatalf("settings: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"env":{"MODEL":"m"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := deepCodeSettings(home); !strings.Contains(got, "API_KEY") {
		t.Fatalf("no key: %q", got)
	}
}

func TestGrokCheck(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{{Agents: []config.Agent{{Executor: "claude"}}}}}
	if usesGrok(cfg) {
		t.Fatal("unused grok checked")
	}
	for _, a := range []config.Agent{{Executor: "grok"}, {Executor: "langgraph", CodeWith: "grok"}} {
		cfg.Repos[0].Agents = []config.Agent{a}
		if !usesGrok(cfg) {
			t.Fatalf("missed agent %+v", a)
		}
	}
	var out bytes.Buffer
	printGrokCheck(&out, filepath.Join(t.TempDir(), "missing-grok"))
	if !strings.Contains(out.String(), "not found") || !strings.Contains(out.String(), "https://github.com/xai-org/grok-build") {
		t.Fatalf("check: %s", out.String())
	}
	out.Reset()
	bin := filepath.Join(t.TempDir(), "grok")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	printGrokCheck(&out, bin)
	if !strings.Contains(out.String(), bin) || !strings.Contains(out.String(), "XAI_API_KEY") {
		t.Fatalf("check: %s", out.String())
	}
}

func TestAntigravityCheck(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{{Agents: []config.Agent{{Executor: "claude"}}}}}
	if usesAntigravity(cfg) {
		t.Fatal("unused antigravity checked")
	}
	for _, a := range []config.Agent{{Executor: "antigravity"}, {Executor: "langgraph", CodeWith: "antigravity"}} {
		cfg.Repos[0].Agents = []config.Agent{a}
		if !usesAntigravity(cfg) {
			t.Fatalf("missed agent %+v", a)
		}
	}
	var out bytes.Buffer
	printAntigravityCheck(&out, filepath.Join(t.TempDir(), "missing-antigravity"))
	if !strings.Contains(out.String(), "not found") || !strings.Contains(out.String(), "https://antigravity.google") {
		t.Fatalf("check: %s", out.String())
	}
	out.Reset()
	bin := filepath.Join(t.TempDir(), "antigravity")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	printAntigravityCheck(&out, bin)
	if !strings.Contains(out.String(), bin) || !strings.Contains(out.String(), "agy") {
		t.Fatalf("check: %s", out.String())
	}
}
