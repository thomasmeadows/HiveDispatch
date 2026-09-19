package claudecode

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/claudecli/clitest"
	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

func input(t *testing.T) triage.Input {
	t.Helper()
	return triage.Input{
		Ticket:   tracker.Ticket{Key: "HIVE-1", Summary: "Add --version", Description: "Print the version."},
		Repo:     config.RepoConfig{Name: "o/r", DefaultBranch: "main"},
		Branch:   "hive/HIVE-1",
		RepoPath: t.TempDir(),
		Attempts: 1, LastStopCause: "timeout",
	}
}

func setup(t *testing.T, fixture string) (*Triager, string, string) {
	t.Helper()
	bin, args, stdin := clitest.Setup(t, "fixture")
	clitest.Fixture(t, "testdata/"+fixture)
	return New(Config{Binary: bin, Timeout: 5 * time.Second}), args, stdin
}

func TestDecideDispatch(t *testing.T) {
	tr, argsFile, stdinFile := setup(t, "stream_decision_dispatch.jsonl")
	d, err := tr.Decide(context.Background(), input(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != triage.KindDispatch || d.Complexity != 1 || d.Reason != "small, well-specified" {
		t.Errorf("d = %+v", d)
	}
	for _, want := range []string{"HIVE-1", "Add --version", "## Triage notes", "main.go already parses flags", "HIVE_NEEDS_INPUT:"} {
		if !strings.Contains(d.Prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--restricted", "--permission-mode\nplan", "--tools\nRead,Grep,Glob", "--json-schema", "--no-session-persistence", "stream-json"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(string(args), "--bare") || strings.Contains(string(args), "--resume") {
		t.Errorf("args must not include --bare or --resume: %s", args)
	}
	stdin, _ := os.ReadFile(stdinFile)
	for _, want := range []string{"Add --version", "Print the version.", "attempt", "timeout", "dispatch", "needs_info", "reject"} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("triage prompt missing %q", want)
		}
	}
}

func TestDecideNeedsInfo(t *testing.T) {
	tr, _, _ := setup(t, "stream_decision_needsinfo.jsonl")
	d, err := tr.Decide(context.Background(), input(t))
	if err != nil || d.Kind != triage.KindNeedsInfo || !strings.HasPrefix(d.Question, "Should --version") || d.Prompt != "" {
		t.Errorf("d=%+v err=%v", d, err)
	}
}

func TestDecideInvalidIsError(t *testing.T) {
	tr, _, _ := setup(t, "stream_decision_invalid.jsonl")
	if _, err := tr.Decide(context.Background(), input(t)); err == nil {
		t.Error("needs_info without a question must be an error")
	}
}

func TestDecideRequiresRepoPath(t *testing.T) {
	tr, _, _ := setup(t, "stream_decision_dispatch.jsonl")
	in := input(t)
	in.RepoPath = ""
	if _, err := tr.Decide(context.Background(), in); err == nil {
		t.Error("expected error without a checkout")
	}
}

func TestDecideCLIErrorIsError(t *testing.T) {
	bin, _, _ := clitest.Setup(t, "error")
	tr := New(Config{Binary: bin, Timeout: 5 * time.Second})
	if _, err := tr.Decide(context.Background(), input(t)); err == nil {
		t.Error("is_error result must be an error")
	}
}
