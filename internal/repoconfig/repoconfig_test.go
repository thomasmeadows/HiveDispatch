package repoconfig

import (
	"os"
	"path/filepath"
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
