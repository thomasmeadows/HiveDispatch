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
