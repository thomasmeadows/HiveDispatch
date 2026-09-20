// Package codextest provides a fake `codex` binary for tests.
package codextest

import (
	"os"
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
	dir := filepath.Dir(self)
	binary = executable(t, filepath.Join(dir, "fakecodex.sh"))
	argsFile = filepath.Join(t.TempDir(), "args")
	stdinFile = filepath.Join(t.TempDir(), "stdin")
	t.Setenv("FAKE_CODEX_DIR", dir)
	t.Setenv("FAKE_CODEX_MODE", mode)
	t.Setenv("FAKE_CODEX_ARGS_FILE", argsFile)
	t.Setenv("FAKE_CODEX_STDIN_FILE", stdinFile)
	return binary, argsFile, stdinFile
}

// executable returns script, or a 0755 copy of it when the checkout dropped
// the mode bit (some filesystems and archive tools do).
func executable(t *testing.T, script string) string {
	t.Helper()
	fi, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 != 0 {
		return script
	}
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), filepath.Base(script))
	if err := os.WriteFile(copyPath, body, 0o755); err != nil {
		t.Fatal(err)
	}
	return copyPath
}

// Fixture makes mode "fixture" replay path.
func Fixture(t *testing.T, path string) {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CODEX_FIXTURE", abs)
}
