package codex

import (
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

func TestBuildArgsRun(t *testing.T) {
	rc := repoconfig.ExecutorConfig{
		Model: "sonnet", PermissionMode: "dontAsk", AllowedTools: []string{"Edit"},
		Codex: repoconfig.CodexConfig{Model: "gpt-5-codex", Sandbox: "workspace-write", Network: true},
	}
	got := strings.Join(buildArgs(Config{Model: "o3"}, rc, "thread-1", false, ""), " ")
	for _, want := range []string{"exec --json", "--skip-git-repo-check", "--sandbox workspace-write", "-c approval_policy=never", "-c sandbox_workspace_write.network_access=true", "--model gpt-5-codex", "resume thread-1 -"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if !strings.HasSuffix(got, "resume thread-1 -") {
		t.Errorf("resume must be the trailing subcommand: %q", got)
	}
	for _, bad := range []string{"sonnet", "dontAsk", "Edit", "--ephemeral", "--output-schema"} {
		if strings.Contains(got, bad) {
			t.Errorf("Claude-only or plan-only setting %q leaked into %q", bad, got)
		}
	}
}

func TestBuildArgsRunFreshUsesWorkerModel(t *testing.T) {
	rc := repoconfig.ExecutorConfig{Codex: repoconfig.CodexConfig{Sandbox: "read-only"}}
	got := strings.Join(buildArgs(Config{Model: "o3"}, rc, "", false, ""), " ")
	if !strings.Contains(got, "--sandbox read-only") || !strings.Contains(got, "--model o3") || strings.Contains(got, "network_access") {
		t.Errorf("args = %q", got)
	}
	if strings.Contains(got, "resume") || !strings.HasSuffix(got, " -") {
		t.Errorf("a fresh run reads the prompt from stdin without resume: %q", got)
	}
}

func TestBuildArgsPlan(t *testing.T) {
	rc := repoconfig.ExecutorConfig{Codex: repoconfig.CodexConfig{Sandbox: "danger-full-access", Network: true}}
	got := strings.Join(buildArgs(Config{}, rc, "thread-1", true, "/tmp/schema.json"), " ")
	for _, want := range []string{"--sandbox read-only", "--ephemeral", "--output-schema /tmp/schema.json", "-c approval_policy=never"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "resume") || strings.Contains(got, "network_access") || strings.Contains(got, "danger") {
		t.Errorf("plan must be read-only, fresh and offline: %q", got)
	}
}
