// Package discover finds git repositories on disk and reads the facts about
// them the worker needs before it has cloned anything: whether a repository
// is enrolled (it has .hive-dispatch/repo.yaml) and where its origin is.
package discover

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Found is one git repository under a scanned root.
type Found struct {
	Path     string
	Enrolled bool // has .hive-dispatch/repo.yaml
	Legacy   bool // has the pre-.hive-dispatch .hivedispatch.yaml but is not enrolled
}

// skipDirs are never descended into: dependency trees that can hold
// thousands of nested checkouts.
var skipDirs = map[string]bool{"node_modules": true, "vendor": true}

// Scan walks each root up to depth directory levels below it and returns
// every git repository found, sorted by path. It does not descend into a
// repository, a hidden directory, node_modules, vendor, or any path under
// exclude (the worker's own workroot holds clones of enrolled repos).
// Duplicate and overlapping roots are reported once.
func Scan(roots []string, depth int, exclude []string) ([]Found, error) {
	seen := map[string]bool{}
	var out []Found
	for _, root := range roots {
		root = filepath.Clean(root)
		if _, err := os.Stat(root); err != nil {
			return nil, fmt.Errorf("scan %s: %w", root, err)
		}
		base := strings.Count(root, string(filepath.Separator))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root {
					return err
				}
				return fs.SkipDir // unreadable directory: skip, keep scanning
			}
			if !d.IsDir() {
				return nil
			}
			if p != root && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] || under(p, exclude)) {
				return fs.SkipDir
			}
			if exists(filepath.Join(p, ".git")) {
				if !seen[p] {
					seen[p] = true
					out = append(out, inspect(p))
				}
				return fs.SkipDir
			}
			if strings.Count(p, string(filepath.Separator))-base >= depth {
				return fs.SkipDir
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", root, err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Inspect reports on the repository at dir, which is assumed to be one.
func Inspect(dir string) Found { return inspect(filepath.Clean(dir)) }

func inspect(p string) Found {
	f := Found{Path: p, Enrolled: exists(filepath.Join(p, ".hive-dispatch", "repo.yaml"))}
	f.Legacy = !f.Enrolled && exists(filepath.Join(p, ".hivedispatch.yaml"))
	return f
}

func under(p string, dirs []string) bool {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if rel, err := filepath.Rel(filepath.Clean(d), p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Origin returns the fetch URL of dir's origin remote and, when git knows
// it (origin/HEAD is set by clone), the remote's default branch; branch is
// empty otherwise.
func Origin(ctx context.Context, dir string) (url, branch string, err error) {
	url, err = gitOut(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return "", "", fmt.Errorf("%s: no origin remote: %w", dir, err)
	}
	if ref, err := gitOut(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		branch = strings.TrimPrefix(ref, "origin/")
	}
	return url, branch, nil
}

// GitRoot returns the top of the working tree containing dir.
func GitRoot(ctx context.Context, dir string) (string, error) {
	root, err := gitOut(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", dir)
	}
	return root, nil
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ParseRepoName returns owner/repo from a GitHub-style clone URL (ssh,
// scp-like or https), or "" when the URL has no host part.
func ParseRepoName(url string) string {
	s := strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	switch {
	case strings.Contains(s, "://"):
		_, s, _ = strings.Cut(s, "://")
		_, s, _ = strings.Cut(s, "/") // drop user@host[:port]
	case strings.Contains(s, ":") && !strings.HasPrefix(s, "/"):
		_, s, _ = strings.Cut(s, ":") // git@host:owner/repo
	default:
		return ""
	}
	parts := strings.Split(s, "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}
