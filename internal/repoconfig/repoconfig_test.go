package repoconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Executor.PermissionMode != "dontAsk" || len(c.Executor.Tools) != 1 || c.Executor.Tools[0] != "default" {
		t.Errorf("defaults = %+v", c.Executor)
	}
}

func TestLoadParsesAndValidates(t *testing.T) {
	dir := t.TempDir()
	body := "executor:\n  model: sonnet\n  permission_mode: acceptEdits\n  allowed_tools: [\"Bash(go test:*)\", Edit]\n  max_budget_usd: 2.5\nguidance: Run go test before finishing.\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Executor.Model != "sonnet" || c.Executor.PermissionMode != "acceptEdits" || len(c.Executor.AllowedTools) != 2 || c.Executor.MaxBudgetUSD != 2.5 || c.Guidance == "" {
		t.Errorf("c = %+v", c)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("executor:\n  permission_mode: yolo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("invalid permission mode should error")
	}
}

func TestLoadCodexSection(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Executor.Codex.Sandbox != "workspace-write" || c.Executor.Codex.Network || c.Executor.Codex.Model != "" {
		t.Errorf("codex defaults = %+v", c.Executor.Codex)
	}
	dir := t.TempDir()
	body := "executor:\n  model: sonnet\n  codex:\n    model: gpt-5-codex\n    sandbox: danger-full-access\n    network: true\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Executor.Model != "sonnet" || c.Executor.Codex.Model != "gpt-5-codex" || c.Executor.Codex.Sandbox != "danger-full-access" || !c.Executor.Codex.Network {
		t.Errorf("c = %+v", c.Executor)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("executor:\n  codex:\n    sandbox: yolo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "executor.codex.sandbox") {
		t.Errorf("invalid sandbox should error, got %v", err)
	}
}

func TestExtraPathExpands(t *testing.T) {
	t.Setenv("HOME", "/home/x")
	t.Setenv("GOBIN_TEST", "/opt/go/bin")
	e := ExecutorConfig{Path: []string{"~/go/bin", "$GOBIN_TEST", ""}}
	got := e.ExtraPath()
	if len(got) != 2 || got[0] != "/home/x/go/bin" || got[1] != "/opt/go/bin" {
		t.Errorf("ExtraPath = %v", got)
	}
}
