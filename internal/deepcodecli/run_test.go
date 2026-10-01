package deepcodecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fake returns a Cmd running the fake deepcode in mode, with HOME (and so
// ~/.deepcode) in a temporary directory.
func fake(t *testing.T, mode string) (Cmd, string) {
	t.Helper()
	bin, err := filepath.Abs("deepcodetest/fakedeepcode.sh")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_DEEPCODE_MODE", mode)
	t.Setenv("FAKE_DEEPCODE_ARGS_FILE", args)
	return Cmd{Binary: bin, Dir: t.TempDir(), Prompt: "-starts with a dash", Env: []string{"HOME=" + home},
		Home: filepath.Join(home, ".deepcode"), Poll: 20 * time.Millisecond}, args
}

func TestRunOK(t *testing.T) {
	c, args := fake(t, "ok")
	tr, exit, log, err := Run(context.Background(), c)
	if err != nil || exit.ExitErr != nil {
		t.Fatalf("err %v exit %+v", err, exit)
	}
	if tr.Status != "completed" || tr.Reply != "Added the function." || strings.TrimSpace(exit.Stdout) != "Added the function." {
		t.Fatalf("tr %+v stdout %q", tr, exit.Stdout)
	}
	if tr.SessionID == "" || len(tr.Steps) != 2 || len(tr.EditedFiles) != 1 || tr.EditedFiles[0] != "main.go" {
		t.Fatalf("tr %+v", tr)
	}
	if !strings.Contains(log, "Added the function.") {
		t.Fatalf("log %q", log)
	}
	raw, _ := os.ReadFile(args)
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(argv) != 2 || argv[0] != "-x" || argv[1] != "--prompt=-starts with a dash" {
		t.Fatalf("argv %q", argv)
	}
}

func TestRunResumePassesTheSession(t *testing.T) {
	c, args := fake(t, "ok")
	c.Resume = "99999999-0000-4000-8000-000000000000"
	tr, _, _, err := Run(context.Background(), c)
	if err != nil || tr.SessionID != c.Resume {
		t.Fatalf("tr %+v err %v", tr, err)
	}
	raw, _ := os.ReadFile(args)
	if !strings.Contains(string(raw), "-r\n"+c.Resume) {
		t.Fatalf("argv %q", raw)
	}
}

func TestRunFailures(t *testing.T) {
	for mode, want := range map[string]string{"permission": "ask_permission", "ratelimit": "failed"} {
		c, _ := fake(t, mode)
		tr, exit, _, err := Run(context.Background(), c)
		if err != nil || exit.ExitErr == nil || tr.Status != want || exit.Stderr == "" {
			t.Errorf("%s: tr %+v exit %+v err %v", mode, tr, exit, err)
		}
	}
	c, _ := fake(t, "nosession")
	tr, exit, _, err := Run(context.Background(), c)
	if err != nil || exit.ExitErr == nil || tr.SessionID != "" || !strings.Contains(exit.Stderr, "API key") {
		t.Errorf("nosession: tr %+v exit %+v err %v", tr, exit, err)
	}
}

func TestRunStepBudget(t *testing.T) {
	c, _ := fake(t, "steps")
	c.StepBudget = 3
	start := time.Now()
	tr, exit, _, _ := Run(context.Background(), c)
	if !exit.StepTripped || exit.CtxErr != nil || time.Since(start) > 10*time.Second {
		t.Fatalf("exit %+v after %v", exit, time.Since(start))
	}
	if tr.ToolCalls < 4 {
		t.Fatalf("tool calls %d", tr.ToolCalls)
	}
}

func TestRunMissingBinary(t *testing.T) {
	c, _ := fake(t, "ok")
	c.Binary = filepath.Join(t.TempDir(), "nope")
	if _, _, _, err := Run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "npm i -g @vegamo/deepcode-cli") {
		t.Fatalf("err %v", err)
	}
}
