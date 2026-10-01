// Package clitest provides a fake `claude` binary for tests.
package clitest

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Setup points the fake at mode and returns the binary path plus the files
// where it records argv and stdin.
func Setup(t *testing.T, mode string) (binary, argsFile, stdinFile string) {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller info")
	}
	binary = filepath.Join(filepath.Dir(self), "fakeclaude.sh")
	argsFile = filepath.Join(t.TempDir(), "args")
	stdinFile = filepath.Join(t.TempDir(), "stdin")
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	t.Setenv("FAKE_CLAUDE_ARGS_FILE", argsFile)
	t.Setenv("FAKE_CLAUDE_STDIN_FILE", stdinFile)
	return binary, argsFile, stdinFile
}

// Fixture makes mode "fixture" replay path.
func Fixture(t *testing.T, path string) {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CLAUDE_FIXTURE", abs)
}
