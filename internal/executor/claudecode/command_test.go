package claudecode

import (
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

func TestBuildArgsRun(t *testing.T) {
	rc := repoconfig.ExecutorConfig{PermissionMode: "dontAsk", Tools: []string{"default"}, AllowedTools: []string{"Bash(go test:*)", "Edit"}, MaxBudgetUSD: 2.5, Model: "sonnet"}
	got := strings.Join(buildArgs(rc, "", "sess-1", false), " ")
	for _, want := range []string{"-p", "--output-format stream-json", "--verbose", "--permission-mode dontAsk", "--tools default", "--allowedTools Bash(go test:*) Edit", "--max-budget-usd 2.5", "--model sonnet", "--resume sess-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "--bare") {
		t.Error("--bare skips credentials and must never be passed")
	}
}

func TestBuildArgsPlan(t *testing.T) {
	got := strings.Join(buildArgs(repoconfig.ExecutorConfig{PermissionMode: "dontAsk", Tools: []string{"default"}}, "opus", "", true), " ")
	for _, want := range []string{"--permission-mode plan", "--json-schema", "--no-session-persistence", "--model opus", "--output-format json"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "--resume") || strings.Contains(got, "stream-json") {
		t.Errorf("plan must not resume or stream: %q", got)
	}
}

func TestBuildArgsAgentModelOverridesPolicy(t *testing.T) {
	rc := repoconfig.ExecutorConfig{PermissionMode: "dontAsk", Model: "sonnet"}
	got := strings.Join(buildArgs(rc, "haiku", "", false), " ")
	if !strings.Contains(got, "--model haiku") || strings.Contains(got, "sonnet") {
		t.Errorf("args = %q, want the agent's model instead of the policy's", got)
	}
}
