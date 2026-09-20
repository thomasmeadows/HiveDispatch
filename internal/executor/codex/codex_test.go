package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/codexcli/codextest"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func fakeExec(t *testing.T, mode string) (*Executor, string, string) {
	t.Helper()
	bin, args, stdin := codextest.Setup(t, mode)
	return New(Config{Binary: bin}), args, stdin
}

func task(t *testing.T) executor.Task {
	t.Helper()
	return executor.Task{TicketKey: "HIVE-1", Prompt: "do it", Workspace: t.TempDir(), StepBudget: 10}
}

func TestName(t *testing.T) {
	if got := New(Config{}).Name(); got != "codex" {
		t.Errorf("Name = %q", got)
	}
}

func TestRunSuccess(t *testing.T) {
	e, argsFile, stdinFile := fakeExec(t, "success")
	res, err := e.Run(context.Background(), task(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != executor.StatusCompleted || res.Summary != "hello" || res.ResumeToken != "01a0bc3f-24b5-79e1-92de-a5550233345e" {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Log, `"type":"turn.completed"`) {
		t.Error("log should contain the raw stream")
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.HasPrefix(string(args), "exec\n--json\n") || strings.Contains(string(args), "resume") {
		t.Errorf("args = %s", args)
	}
	stdin, _ := os.ReadFile(stdinFile)
	if string(stdin) != "do it" {
		t.Errorf("stdin = %q", stdin)
	}
}

func TestRunPassesResumeAndGuidance(t *testing.T) {
	e, argsFile, stdinFile := fakeExec(t, "success")
	tk := task(t)
	tk.ResumeToken = "old-thread"
	if err := os.WriteFile(filepath.Join(tk.Workspace, ".hivedispatch.yaml"), []byte("guidance: Always run make test.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.HasSuffix(string(args), "resume\nold-thread\n-\n") {
		t.Errorf("args = %s", args)
	}
	stdin, _ := os.ReadFile(stdinFile)
	if !strings.Contains(string(stdin), "Always run make test.") {
		t.Errorf("guidance missing from prompt: %q", stdin)
	}
}

func TestRunHonoursRepoCodexPolicy(t *testing.T) {
	e, argsFile, _ := fakeExec(t, "success")
	tk := task(t)
	body := "executor:\n  model: sonnet\n  codex:\n    model: gpt-5-codex\n    sandbox: danger-full-access\n    network: true\n"
	if err := os.WriteFile(filepath.Join(tk.Workspace, ".hivedispatch.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--sandbox\ndanger-full-access\n", "--model\ngpt-5-codex\n", "sandbox_workspace_write.network_access=true"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if strings.Contains(string(args), "sonnet") {
		t.Errorf("Claude's executor.model must not reach codex: %s", args)
	}
}

func TestRunNeedsInput(t *testing.T) {
	e, _, _ := fakeExec(t, "needsinput")
	res, err := e.Run(context.Background(), task(t))
	if err != nil || res.Status != executor.StatusNeedsInput || !strings.HasPrefix(res.Question, "Should the flag") {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestRunErrorTranscript(t *testing.T) {
	e, _, _ := fakeExec(t, "error")
	res, err := e.Run(context.Background(), task(t))
	if err != nil || res.Status != executor.StatusFailed || res.StopCause != executor.CauseBudget {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if len(res.ChangedFiles) != 2 || res.ChangedFiles[0] != "/work/HIVE-2/cmd/app/main.go" {
		t.Errorf("changed = %v", res.ChangedFiles)
	}
}

func TestRunCrashWithoutResult(t *testing.T) {
	e, _, _ := fakeExec(t, "crash")
	res, err := e.Run(context.Background(), task(t))
	if err != nil || res.Status != executor.StatusFailed || res.StopCause != executor.CauseError || !strings.Contains(res.Summary, "segfault") {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestRunTimeoutMapsToCause(t *testing.T) {
	e, _, _ := fakeExec(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res, err := e.Run(ctx, task(t))
	if err != nil || res.StopCause != executor.CauseTimeout || res.ResumeToken == "" {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestRunStepBudgetMapsToCause(t *testing.T) {
	e, _, _ := fakeExec(t, "manysteps")
	tk := task(t)
	tk.StepBudget = 5
	res, err := e.Run(context.Background(), tk)
	if err != nil || res.StopCause != executor.CauseStepBudget {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestPlanReadsSchemaConstrainedMessage(t *testing.T) {
	e, argsFile, stdinFile := fakeExec(t, "plan")
	fp, err := e.Plan(context.Background(), task(t))
	if err != nil || len(fp.Files) != 2 || fp.Files[1] != "b.go" {
		t.Errorf("fp=%+v err=%v", fp, err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--output-schema\n") || !strings.Contains(string(args), "--sandbox\nread-only\n") || strings.Contains(string(args), "resume") {
		t.Errorf("args = %s", args)
	}
	stdin, _ := os.ReadFile(stdinFile)
	if !strings.Contains(string(stdin), "Do not make changes") {
		t.Errorf("plan prompt = %q", stdin)
	}
	// The schema file is temporary and must not outlive the call.
	for line := range strings.SplitSeq(string(args), "\n") {
		if strings.HasSuffix(line, ".json") {
			if _, err := os.Stat(line); err == nil {
				t.Errorf("schema file %s left behind", line)
			}
		}
	}
}

func TestPlanFailsOnError(t *testing.T) {
	e, _, _ := fakeExec(t, "error")
	if _, err := e.Plan(context.Background(), task(t)); err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Errorf("err = %v", err)
	}
}

func TestRunPrependsRepoPath(t *testing.T) {
	e, _, _ := fakeExec(t, "success")
	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("FAKE_CODEX_ENV_FILE", envFile)
	tk := task(t)
	if err := os.WriteFile(filepath.Join(tk.Workspace, ".hivedispatch.yaml"), []byte("executor:\n  path: [/opt/tools/bin]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(envFile)
	if !strings.Contains(string(env), "PATH=/opt/tools/bin"+string(os.PathListSeparator)) {
		t.Errorf("PATH not prepended:\n%s", env)
	}
}
