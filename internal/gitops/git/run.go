package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// run executes git in dir and returns trimmed stdout.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	return runEnv(ctx, dir, nil, args...)
}

// identityEnv makes commits by the orchestrator distinguishable from the
// agent's. It is set on the environment because GIT_AUTHOR_* variables
// override -c user.* settings.
func identityEnv(name, email string) []string {
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
	}
}

// runEnv is run with extra environment variables.
func runEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// refExists reports whether a fully qualified ref exists in dir.
func refExists(ctx context.Context, dir, ref string) bool {
	_, err := run(ctx, dir, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}
