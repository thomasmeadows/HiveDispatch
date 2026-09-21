package supervisor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	}
	for _, tc := range cases {
		got, mut, ok := Allowed(tc.args)
		if got != tc.want || mut != tc.mutating || ok != tc.ok {
			t.Errorf("Allowed(%v) = %q %v %v, want %q %v %v", tc.args, got, mut, ok, tc.want, tc.mutating, tc.ok)
		}
	}
}

// fakeExe writes a script that prints its argv and exits with $HIVE_TEST_EXIT.
func fakeExe(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	p := filepath.Join(t.TempDir(), "hivedispatch")
	script := "#!/bin/sh\necho \"args: $*\"\necho \"err line\" >&2\nexit ${HIVE_TEST_EXIT:-0}\n"
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
