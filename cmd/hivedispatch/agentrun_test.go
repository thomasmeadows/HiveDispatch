package main

import (
	"bytes"
	"encoding/json"
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
