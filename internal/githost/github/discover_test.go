package github

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeBin puts a shell script named name on PATH for the test.
func fakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverTokenPrefersEnv(t *testing.T) {
	t.Setenv("HIVE_GITHUB_TOKEN", "from-env")
	tok, src := DiscoverToken(context.Background(), "github.com")
	if tok != "from-env" || src != "HIVE_GITHUB_TOKEN" {
		t.Errorf("tok=%q src=%q", tok, src)
	}
}

func TestDiscoverTokenFallsBackToGhThenGitCredential(t *testing.T) {
	t.Setenv("HIVE_GITHUB_TOKEN", "")
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	fakeBin(t, bin, "gh", `[ "$1 $2" = "auth token" ] && echo gho_fromgh`)
	tok, src := DiscoverToken(context.Background(), "github.com")
	if tok != "gho_fromgh" || src != "gh auth token" {
		t.Errorf("tok=%q src=%q", tok, src)
	}
	// Without gh, the git credential helper answers.
	if err := os.Remove(filepath.Join(bin, "gh")); err != nil {
		t.Fatal(err)
	}
	fakeBin(t, bin, "git", `[ "$1 $2" = "credential fill" ] && printf 'protocol=https\nhost=github.com\nusername=x\npassword=ghp_fromgit\n'`)
	tok, src = DiscoverToken(context.Background(), "github.com")
	if tok != "ghp_fromgit" || src != "git credential helper" {
		t.Errorf("tok=%q src=%q", tok, src)
	}
	// Nothing available.
	if err := os.Remove(filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	if tok, src = DiscoverToken(context.Background(), "github.com"); tok != "" || src != "" {
		t.Errorf("expected nothing, got tok=%q src=%q", tok, src)
	}
}
