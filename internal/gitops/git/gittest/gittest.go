// Package gittest builds throwaway git remotes for tests.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Env isolates git from the user's configuration and sets an identity.
func Env(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
}

// Git runs git in dir and fails the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// NewRemote returns a bare repository whose main branch has one commit.
func NewRemote(t *testing.T) string {
	t.Helper()
	Env(t)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	Git(t, root, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(root, "seed")
	Git(t, root, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Git(t, seed, "add", "README.md")
	Git(t, seed, "commit", "-q", "-m", "init")
	Git(t, seed, "remote", "add", "origin", remote)
	Git(t, seed, "push", "-q", "origin", "main")
	return remote
}

// Clone returns a fresh working clone of remote.
func Clone(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	Git(t, t.TempDir(), "clone", "-q", remote, dir)
	return dir
}
