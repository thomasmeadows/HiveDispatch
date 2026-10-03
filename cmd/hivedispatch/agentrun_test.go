package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentRunFake(t *testing.T) {
	var out, errb bytes.Buffer
	code := runAgentRun([]string{"-executor", "fake", "-workspace", t.TempDir()}, strings.NewReader("do it"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var r agentRunResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if r.Status != "completed" || r.Summary == "" || r.Steps == nil || r.ChangedFiles == nil {
		t.Fatalf("result %+v", r)
	}
}

func TestAgentRunBadExecutor(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runAgentRun([]string{"-executor", "gemini", "-workspace", "."}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("printed %q on a usage error", out.String())
	}
}

func TestAgentRunAcceptsDeepCode(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "") // agent-run must not need repository secrets to find its CLI
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("deepcode:\n  binary: "+filepath.Join(t.TempDir(), "no-deepcode")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runAgentRun([]string{"-executor", "deepcode", "-config", cfg, "-workspace", t.TempDir()}, strings.NewReader("x"), &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "deepcode") {
		t.Fatalf("exit %d (want 1: accepted, but the binary is missing): %s", code, errb.String())
	}
}

func TestAgentRunGrok(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "") // agent-run must not need repository secrets to find its CLI
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-grok")
	script := "#!/bin/sh\ncat <<'JSON'\n{\"type\":\"result\",\"subtype\":\"success\",\"stop_reason\":\"end_turn\",\"result\":\"Grok completed\",\"session_id\":\"s1\"}\nJSON\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := writeValidConfigWith(t, "grok:\n  binary: "+bin+"\n")
	var out, errb bytes.Buffer
	code := runAgentRun([]string{"-executor", "grok", "-config", cfg, "-workspace", dir}, strings.NewReader("task"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var r agentRunResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "completed" || r.Summary != "Grok completed" {
		t.Fatalf("result: %+v", r)
	}
}

func TestAgentRunAntigravity(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "") // agent-run must not need repository secrets to find its CLI
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-antigravity")
	script := "#!/bin/sh\ncat >/dev/null\ncat <<'JSON'\n{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"Antigravity completed\",\"conversation_id\":\"s1\"}}\nJSON\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := writeValidConfigWith(t, "antigravity:\n  binary: "+bin+"\n")
	var out, errb bytes.Buffer
	code := runAgentRun([]string{"-executor", "antigravity", "-config", cfg, "-workspace", dir}, strings.NewReader("task"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var r agentRunResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "completed" || r.Summary != "Antigravity completed" {
		t.Fatalf("result: %+v", r)
	}
}

func TestAgentRunOpenClaw(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "") // agent-run must not need repository secrets to find its CLI
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-openclaw")
	script := "#!/bin/sh\ncat >/dev/null\ncat <<'JSON'\n{\"ok\":true,\"status\":\"ok\",\"final\":\"OpenClaw completed\"}\nJSON\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := writeValidConfigWith(t, "openclaw:\n  binary: "+bin+"\n")
	var out, errb bytes.Buffer
	code := runAgentRun([]string{"-executor", "openclaw", "-config", cfg, "-workspace", dir}, strings.NewReader("task"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var r agentRunResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "completed" || r.Summary != "OpenClaw completed" {
		t.Fatalf("result: %+v", r)
	}
}
