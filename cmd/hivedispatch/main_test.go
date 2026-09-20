package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "hivedispatch ") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestCheckValidConfig(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(`
agent_id: w
jira:
  base_url: https://x.atlassian.net
  email: a@b.c
  jql: project = X
  fields: {agent_id: customfield_1, claimed_at: customfield_2}
repos:
  - {name: o/r, url: git@github.com:o/r.git, jira_project: X}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"check", "-config", p}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "config ok") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestCheckInvalidConfig(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "")
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("agent_id: w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"check", "-config", p}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "invalid config") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestInitOnExistingConfigIsANoop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(home, ".config", "hivedispatch", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("agent_id: keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "already exists") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestRunRequiresConfig(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"run", "-config", filepath.Join(t.TempDir(), "missing.yaml")}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errb.String())
	}
}

func TestInitWritesStarterConfigWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out, errb bytes.Buffer
	if code := run([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	p := filepath.Join(home, ".config", "hivedispatch", "config.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("starter config not written: %v", err)
	}
	for _, want := range []string{"agent_id:", "base_url:", "jql:", "repos:", "HIVE_JIRA_TOKEN", "HIVE_GITHUB_TOKEN", "id.atlassian.com"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("starter config missing %q", want)
		}
	}
	if !strings.Contains(out.String(), p) || !strings.Contains(out.String(), "init -jira") {
		t.Errorf("stdout should name the file and the next step: %q", out.String())
	}
	// Second run must not overwrite.
	if err := os.WriteFile(p, []byte("agent_id: keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if raw, _ := os.ReadFile(p); string(raw) != "agent_id: keep-me\n" {
		t.Error("init overwrote an existing config")
	}
}

func TestInitJiraOnFreshMachineWritesStarterAndStops(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HIVE_JIRA_TOKEN", "")
	var out, errb bytes.Buffer
	if code := run([]string{"init", "-jira"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1 (config needs editing first): %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "wrote starter config") || !strings.Contains(errb.String(), "fill in the config") {
		t.Errorf("out=%q err=%q", out.String(), errb.String())
	}
}

func TestInitReplacesEmptyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(home, ".config", "hivedispatch", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if raw, _ := os.ReadFile(p); len(raw) == 0 {
		t.Error("empty file should be replaced by the starter")
	}
}

func TestInitJiraOnIncompleteConfigExplainsEachValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HIVE_JIRA_TOKEN", "")
	t.Setenv("HIVE_GITHUB_TOKEN", "")
	p := filepath.Join(home, ".config", "hivedispatch", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("agent_id: w\n"), 0o600); err != nil { // hand-made, incomplete
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"init", "-jira"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errb.String())
	}
	for _, want := range []string{"id.atlassian.com", "atlassian.net", "config.yaml", "docs/setup.md"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr should mention %q:\n%s", want, errb.String())
		}
	}
	if strings.Contains(errb.String(), "HIVE_GITHUB_TOKEN") || strings.Contains(errb.String(), "customfield") {
		t.Errorf("init -jira must not demand the GitHub token or the field ids it creates:\n%s", errb.String())
	}
}
