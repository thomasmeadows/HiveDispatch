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

func TestInitWithoutJiraFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"init"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
