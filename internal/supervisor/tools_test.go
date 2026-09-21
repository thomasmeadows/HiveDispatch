package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func call(t *testing.T, tool Tool, args string) (string, error) {
	t.Helper()
	return tool.Call(context.Background(), json.RawMessage(args))
}

const validWorkerConfig = `agent_id: w
tracker: github
repos:
  - {name: o/r, url: git@github.com:o/r.git, project: X}
`

func TestReadConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	out, err := call(t, NewReadConfig(p), `{}`)
	if err != nil || !strings.Contains(out, "no config at "+p) {
		t.Fatalf("missing: %q %v", out, err)
	}
	if err := os.WriteFile(p, []byte("agent_id: w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _ := call(t, NewReadConfig(p), `{}`); out != "agent_id: w\n" {
		t.Errorf("out = %q", out)
	}
}

func TestWriteConfigRejectsUnparseable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	asked := false
	tool := NewWriteConfig(p, func(string) bool { asked = true; return true })
	_, err := call(t, tool, `{"content":"agent_id: [oops\n"}`)
	if err == nil || asked {
		t.Fatalf("err = %v, asked = %v", err, asked)
	}
	if _, statErr := os.Stat(p); statErr == nil {
		t.Error("file was written")
	}
	if _, statErr := os.Stat(p + ".tmp"); statErr == nil {
		t.Error("temp file left behind")
	}
}

func TestWriteConfigAsksShowsDiffAndBacksUp(t *testing.T) {
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("agent_id: old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var prompt string
	tool := NewWriteConfig(p, func(s string) bool { prompt = s; return true })
	body, _ := json.Marshal(map[string]string{"content": validWorkerConfig})
	out, err := call(t, tool, string(body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "-agent_id: old") || !strings.Contains(prompt, "+agent_id: w") {
		t.Errorf("prompt = %q", prompt)
	}
	if !strings.Contains(out, "wrote "+p) {
		t.Errorf("out = %q", out)
	}
	got, _ := os.ReadFile(p)
	if string(got) != validWorkerConfig {
		t.Errorf("file = %q", got)
	}
	bak, _ := os.ReadFile(p + ".bak")
	if string(bak) != "agent_id: old\n" {
		t.Errorf("bak = %q", bak)
	}
}

func TestWriteConfigPreCancelledSkipsConfirm(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	asked := false
	tool := NewWriteConfig(p, func(string) bool { asked = true; return true })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Call(ctx, json.RawMessage(`{"content":"agent_id: w\n"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if asked {
		t.Error("confirm was called with a pre-cancelled context")
	}
	if _, statErr := os.Stat(p); statErr == nil {
		t.Error("file was written")
	}
}

func TestWriteConfigDeclined(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	tool := NewWriteConfig(p, func(string) bool { return false })
	out, err := call(t, tool, `{"content":"agent_id: w\n"}`)
	if err != nil || !strings.Contains(out, "declined by user") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if _, statErr := os.Stat(p); statErr == nil {
		t.Error("file was written")
	}
}

func TestWriteConfigReportsValidationProblems(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	var prompt string
	tool := NewWriteConfig(p, func(s string) bool { prompt = s; return true })
	out, err := call(t, tool, `{"content":"agent_id: w\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "repos must list") || !strings.Contains(prompt, "repos must list") {
		t.Errorf("problems not reported: out=%q prompt=%q", out, prompt)
	}
}

func TestReadDoc(t *testing.T) {
	out, err := call(t, NewReadDoc(), `{"name":"setup"}`)
	if err != nil || !strings.HasPrefix(out, "# Setup") {
		t.Fatalf("out = %.40q, err = %v", out, err)
	}
	if _, err := call(t, NewReadDoc(), `{"name":"../go.mod"}`); err == nil || !strings.Contains(err.Error(), "setup, config, design, decisions") {
		t.Errorf("err = %v", err)
	}
}

func TestReadRepoFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	root := filepath.Join(dir, "work")
	if err := os.WriteFile(cfg, []byte("workroot: "+root+"\nrepos:\n  - {name: o/r, url: u, project: X}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repos", "o__r", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".hivedispatch.yaml"), []byte("executor: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewReadRepoFile(cfg)
	if out, err := call(t, tool, `{"repo":"o/r","name":".hivedispatch.yaml"}`); err != nil || out != "executor: {}\n" {
		t.Errorf("out = %q, err = %v", out, err)
	}
	if out, err := call(t, tool, `{"repo":"O/R","name":".hivedispatch.yaml"}`); err != nil || out != "executor: {}\n" {
		t.Errorf("case-insensitive repo: out = %q, err = %v", out, err)
	}
	if _, err := call(t, tool, `{"repo":"o/r","name":"AGENTS.md"}`); err == nil || !strings.Contains(err.Error(), "no AGENTS.md") {
		t.Errorf("missing file: %v", err)
	}
	if _, err := call(t, tool, `{"repo":"o/r","name":"../secret"}`); err == nil || !strings.Contains(err.Error(), ".hivedispatch.yaml or AGENTS.md") {
		t.Errorf("bad name: %v", err)
	}
	if _, err := call(t, tool, `{"repo":"x/y","name":"AGENTS.md"}`); err == nil || !strings.Contains(err.Error(), "not in repos") {
		t.Errorf("unknown repo: %v", err)
	}
	if _, err := call(t, NewReadRepoFile(filepath.Join(dir, "nope.yaml")), `{"repo":"o/r","name":"AGENTS.md"}`); err == nil || !strings.Contains(err.Error(), "no config") {
		t.Errorf("no config: %v", err)
	}
}
