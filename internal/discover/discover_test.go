package discover

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func mkdir(t *testing.T, parts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(parts...), 0o755); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, parts ...string) {
	t.Helper()
	p := filepath.Join(parts...)
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanFindsReposAndMarksEnrolment(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, "plain", ".git")
	mkdir(t, root, "enrolled", ".git")
	touch(t, root, "enrolled", ".hive-dispatch", "repo.yaml")
	mkdir(t, root, "legacy", ".git")
	touch(t, root, "legacy", ".hivedispatch.yaml")
	mkdir(t, root, "group", "nested", ".git")
	touch(t, root, "worktree", ".git") // a linked worktree has a .git file
	// Never descended into: inside a repo, hidden, node_modules, vendor, too deep.
	mkdir(t, root, "plain", "sub", ".git")
	mkdir(t, root, ".hidden", "r", ".git")
	mkdir(t, root, "node_modules", "r", ".git")
	mkdir(t, root, "vendor", "r", ".git")
	mkdir(t, root, "a", "b", "c", "d", ".git")

	got, err := Scan([]string{root}, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Found{
		{Path: filepath.Join(root, "enrolled"), Enrolled: true},
		{Path: filepath.Join(root, "group", "nested")},
		{Path: filepath.Join(root, "legacy"), Legacy: true},
		{Path: filepath.Join(root, "plain")},
		{Path: filepath.Join(root, "worktree")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan =\n%+v\nwant\n%+v", got, want)
	}
}

func TestScanSkipsExcludedAndDedupesRoots(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, "keep", ".git")
	mkdir(t, root, "work", "repos", "x", ".git")
	got, err := Scan([]string{root, root, filepath.Join(root, "keep")}, 4, []string{filepath.Join(root, "work")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != filepath.Join(root, "keep") {
		t.Errorf("Scan = %+v", got)
	}
}

func TestScanRootIsARepo(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, ".git")
	got, err := Scan([]string{root}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != root {
		t.Errorf("Scan = %+v", got)
	}
}

func TestScanMissingRootErrors(t *testing.T) {
	if _, err := Scan([]string{filepath.Join(t.TempDir(), "nope")}, 2, nil); err == nil {
		t.Error("missing root should error")
	}
}

func TestParseRepoName(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:owner/repo.git":         "owner/repo",
		"https://github.com/owner/repo.git":     "owner/repo",
		"https://github.com/owner/repo":         "owner/repo",
		"ssh://git@github.com/owner/repo.git":   "owner/repo",
		"https://user@github.com/owner/repo/":   "owner/repo",
		"https://ghe.example.com/org/team-repo": "org/team-repo",
		"/srv/git/repo.git":                     "",
		"":                                      "",
	} {
		if got := ParseRepoName(in); got != want {
			t.Errorf("ParseRepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestOrigin(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "trunk")
	url, branch, err := Origin(context.Background(), dir)
	if err == nil {
		t.Errorf("no origin should error, got %q %q", url, branch)
	}
	git(t, dir, "remote", "add", "origin", "git@github.com:o/r.git")
	url, branch, err = Origin(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if url != "git@github.com:o/r.git" || branch != "" {
		t.Errorf("Origin = %q %q", url, branch)
	}
	git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	if _, branch, err = Origin(context.Background(), dir); err != nil || branch != "trunk" {
		t.Errorf("branch = %q, err %v", branch, err)
	}
}

func TestScanFindsSymlinkedRepos(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	mkdir(t, elsewhere, "real", ".git")
	mkdir(t, elsewhere, "group", "inner", ".git")
	for name, target := range map[string]string{"linked": "real", "linkedgroup": "group"} {
		if err := os.Symlink(filepath.Join(elsewhere, target), filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	got, err := Scan([]string{root}, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A symlink to a repository is found; symlinks are never descended into.
	if len(got) != 1 || got[0].Path != filepath.Join(root, "linked") {
		t.Errorf("Scan = %+v", got)
	}
}

func TestScanFollowsASymlinkedRoot(t *testing.T) {
	real, link := t.TempDir(), filepath.Join(t.TempDir(), "code")
	mkdir(t, real, "r", ".git")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := Scan([]string{link}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Base(got[0].Path) != "r" {
		t.Errorf("Scan = %+v", got)
	}
}
