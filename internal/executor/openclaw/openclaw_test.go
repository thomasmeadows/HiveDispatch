package openclaw

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "openclaw")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunWorktreeAndPrompt(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".hive-dispatch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hive-dispatch", "policy.yaml"), []byte("guidance: Follow repository conventions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := fakeCLI(t, `printf '%s\n' "$@" > args
pwd > cwd
cat > prompt
printf '%s\n' '{"ok":true,"status":"ok","final":"Implemented","sessionId":"ephemeral","usage":{"input":120,"output":8,"total":128},"costUsd":0.0021,"model":"test","provider":"vendor","toolSummary":{"calls":2}}'
`)
	r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: dir, Prompt: "Do work\nwith care", Model: "vendor/test", StepBudget: 10, ResumeToken: "old-session"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != executor.StatusCompleted || r.Summary != "Implemented" || r.ResumeToken != "" || r.Usage.InputTokens != 120 || r.Usage.OutputTokens != 8 || r.Usage.CostUSD != 0.0021 {
		t.Fatalf("result: %+v", r)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	want := "agent\nexec\n--message-file\n-\n--cwd\n" + dir + "\n--json\n--timeout\n0\n--model\nvendor/test\n"
	if string(args) != want {
		t.Fatalf("args %q, want %q", args, want)
	}
	cwd, err := os.ReadFile(filepath.Join(dir, "cwd"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(cwd)) != dir {
		t.Fatalf("cwd %q", cwd)
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "prompt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "Do work\nwith care") || !strings.Contains(string(prompt), "Follow repository conventions") {
		t.Fatalf("prompt %q", prompt)
	}
}

func TestRunOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		code         int
		status       executor.Status
		cause        executor.Cause
		question     string
	}{
		{"success", `{"ok":true,"status":"ok","final":"done"}`, 0, executor.StatusCompleted, executor.CauseNone, ""},
		{"input", `{"ok":true,"status":"ok","final":"HIVE_NEEDS_INPUT: Which version?"}`, 0, executor.StatusNeedsInput, executor.CauseNone, "Which version?"},
		{"error", `{"ok":false,"status":"error","error":{"kind":"model","message":"unavailable"}}`, 0, executor.StatusFailed, executor.CauseError, ""},
		{"timeout", `{"ok":false,"status":"timeout","error":{"message":"deadline"}}`, 2, executor.StatusFailed, executor.CauseTimeout, ""},
		{"budget", `{"ok":false,"status":"error","error":{"message":"rate limit reached"}}`, 1, executor.StatusFailed, executor.CauseBudget, ""},
		{"nonzero", `{"ok":true,"status":"ok","final":"done"}`, 1, executor.StatusFailed, executor.CauseError, ""},
		{"malformed", `not json`, 0, executor.StatusFailed, executor.CauseError, ""},
		{"missing", `{}`, 0, executor.StatusFailed, executor.CauseError, ""},
		{"empty", ``, 0, executor.StatusFailed, executor.CauseError, ""},
		{"trailing", `{"ok":true,"status":"ok"} garbage`, 0, executor.StatusFailed, executor.CauseError, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Controlled fixtures contain no shell quoting characters.
			bin := fakeCLI(t, "cat >/dev/null\nprintf '%s' '"+tc.output+"'\nexit "+string(rune('0'+tc.code))+"\n")
			r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: t.TempDir(), Prompt: "task"})
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != tc.status || r.StopCause != tc.cause || r.Question != tc.question || r.Summary == "" {
				t.Fatalf("result: %+v", r)
			}
		})
	}
}

func TestRunCancellation(t *testing.T) {
	bin := fakeCLI(t, "cat >/dev/null\nsleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err := New(Config{Binary: bin}).Run(ctx, executor.Task{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if r.StopCause != executor.CauseTimeout || time.Since(start) > 3*time.Second {
		t.Fatalf("result %+v after %v", r, time.Since(start))
	}
}

func TestCodingOnlyAndMissingBinary(t *testing.T) {
	e := New(Config{Binary: filepath.Join(t.TempDir(), "missing")})
	if _, err := e.Plan(context.Background(), executor.Task{}); err == nil {
		t.Fatal("accepted planning")
	}
	if _, err := e.Advise(context.Background(), executor.Advice{}); err == nil {
		t.Fatal("accepted advice")
	}
	if _, err := e.Run(context.Background(), executor.Task{Workspace: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "openclaw.binary") {
		t.Fatalf("error: %v", err)
	}
}

func TestRelativeWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir("repo", 0o700); err != nil {
		t.Fatal(err)
	}
	bin := fakeCLI(t, `cat >/dev/null
while [ "$#" -gt 0 ]; do
 if [ "$1" = '--cwd' ]; then shift; test "$1" = "$PWD" || exit 1; fi
 shift
done
printf '%s' '{"ok":true,"status":"ok","final":"done"}'
`)
	r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: "repo"})
	if err != nil || r.Status != executor.StatusCompleted {
		t.Fatalf("result %+v, error %v", r, err)
	}
}

func TestRepositoryToolPath(t *testing.T) {
	dir := t.TempDir()
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "hive-openclaw-test-helper"), []byte("#!/bin/sh\nprintf '%s' '{\"ok\":true,\"status\":\"ok\",\"final\":\"tool found\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".hive-dispatch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hive-dispatch", "policy.yaml"), []byte("executor:\n  path:\n    - "+tools+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := fakeCLI(t, "cat >/dev/null\nhive-openclaw-test-helper\n")
	r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: dir})
	if err != nil || r.Status != executor.StatusCompleted || r.Summary != "tool found" {
		t.Fatalf("result %+v, error %v", r, err)
	}
}
