package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAllowed(t *testing.T) {
	cases := []struct {
		args     []string
		want     string
		mutating bool
		ok       bool
	}{
		{[]string{"version"}, "version", false, true},
		{[]string{"check", "-live"}, "check -live", false, true},
		{[]string{"init", "-github"}, "init -github", true, true},
		{[]string{"run", "-executor", "fake", "-once"}, "run -once -executor fake", true, true},
		{[]string{"run", "-once", "-placeholder", "-executor", "fake"}, "run -once -executor fake -placeholder", true, true},
		{[]string{"run"}, "", false, false},
		{[]string{"run", "-once"}, "", false, false},
		{[]string{"run", "-once", "-executor", "claude"}, "", false, false},
		{[]string{"once", "X-1"}, "", false, false},
		{[]string{"check", "-config", "/etc/passwd"}, "", false, false},
		{nil, "", false, false},
		{[]string{"run", "-once", "-executor fake"}, "", false, false},
		{[]string{"run", "-once", "-executor", "fake", ""}, "", false, false},
		{[]string{"check ", "-live"}, "", false, false},
		{[]string{"run", "fake", "-once", "-executor"}, "", false, false},
	}
	for _, tc := range cases {
		got, mut, ok := Allowed(tc.args)
		if got != tc.want || mut != tc.mutating || ok != tc.ok {
			t.Errorf("Allowed(%v) = %q %v %v, want %q %v %v", tc.args, got, mut, ok, tc.want, tc.mutating, tc.ok)
		}
	}
}

// fakeExe writes a script that prints its argv and exits with $HIVE_TEST_EXIT.
// When $HIVE_TEST_LOG is set, it also appends its argv to that file, so
// callers can count how many times the binary was invoked (e.g. to check
// that `check` runs at most once per turn).
func fakeExe(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	p := filepath.Join(t.TempDir(), "hivedispatch")
	script := "#!/bin/sh\necho \"args: $*\"\necho \"err line\" >&2\n" +
		"if [ -n \"$HIVE_TEST_LOG\" ]; then echo \"$*\" >> \"$HIVE_TEST_LOG\"; fi\n" +
		"exit ${HIVE_TEST_EXIT:-0}\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunHivedispatchInsertsConfigAndCaptures(t *testing.T) {
	t.Setenv("HIVE_TEST_EXIT", "3")
	tool := NewRunHivedispatch(fakeExe(t), "/c/config.yaml", func(string) bool { t.Error("check must not ask"); return false })
	out, err := call(t, tool, `{"args":["check","-live"]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exit 3", "args: check -config /c/config.yaml -live", "err line"} {
		if !strings.Contains(out, want) {
			t.Errorf("out %q lacks %q", out, want)
		}
	}
}

func TestRunHivedispatchRefusesAndAsks(t *testing.T) {
	tool := NewRunHivedispatch(fakeExe(t), "/c/config.yaml", func(string) bool { return false })
	if _, err := call(t, tool, `{"args":["once","X-1"]}`); err == nil || !strings.Contains(err.Error(), "run -once -executor fake") {
		t.Errorf("refusal must list the allowlist: %v", err)
	}
	out, err := call(t, tool, `{"args":["init","-github"]}`)
	if err != nil || !strings.Contains(out, "declined by user") {
		t.Errorf("declined: %q %v", out, err)
	}
	var prompt string
	tool = NewRunHivedispatch(fakeExe(t), "/c/config.yaml", func(s string) bool { prompt = s; return true })
	if out, err := call(t, tool, `{"args":["run","-once","-executor","fake"]}`); err != nil || !strings.Contains(out, "args: run -config /c/config.yaml -once -executor fake") {
		t.Errorf("approved: %q %v", out, err)
	}
	if !strings.Contains(prompt, "hivedispatch run -once -executor fake") {
		t.Errorf("prompt = %q", prompt)
	}
}

func TestRunHivedispatchMutatingPreCancelledSkipsConfirm(t *testing.T) {
	asked := false
	tool := NewRunHivedispatch(fakeExe(t), "/c/config.yaml", func(string) bool { asked = true; return true })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Call(ctx, json.RawMessage(`{"args":["init","-github"]}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if asked {
		t.Error("confirm was called with a pre-cancelled context")
	}
}

func TestRunHivedispatchCancelDuringRunReportsCancelledNotTimedOut(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hivedispatch")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := NewRunHivedispatch(p, "/c/config.yaml", func(string) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := tool.Call(ctx, json.RawMessage(`{"args":["version"]}`))
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v, want it to say cancelled", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, must not say timed out for an explicit cancel", err)
	}
}
