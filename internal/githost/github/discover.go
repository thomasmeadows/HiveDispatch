package github

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DiscoverToken finds a GitHub token without configuration, in order:
// the HIVE_GITHUB_TOKEN environment variable, `gh auth token` (the GitHub
// CLI), then the git credential helper for host. It returns the token and
// a short description of where it came from, or empty strings.
func DiscoverToken(ctx context.Context, host string) (token, source string) {
	if t := strings.TrimSpace(os.Getenv("HIVE_GITHUB_TOKEN")); t != "" {
		return t, "HIVE_GITHUB_TOKEN"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output(); err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t, "gh auth token"
		}
	}
	cmd := exec.CommandContext(ctx, "git", "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "password="); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v), "git credential helper"
			}
		}
	}
	return "", ""
}
