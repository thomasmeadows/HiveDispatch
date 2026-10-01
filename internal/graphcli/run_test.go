package graphcli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fake(t *testing.T, mode string) Cmd {
	t.Helper()
	bin, err := filepath.Abs("graphtest/fakegraph.sh")
	if err != nil {
		t.Fatal(err)
	}
	return Cmd{Binary: bin, Dir: t.TempDir(), Args: []string{"run"}, Stdin: "{}", Env: []string{"FAKEGRAPH_MODE=" + mode}, Grace: 300 * time.Millisecond}
}

func TestRunOK(t *testing.T) {
	tr, exit, log, err := Run(context.Background(), fake(t, "ok"))
	if err != nil || exit.ExitErr != nil || tr.Result == nil || tr.Result.Summary != "done" || len(tr.Steps) != 1 || !strings.Contains(log, `"result"`) {
		t.Fatalf("tr %+v exit %+v err %v", tr, exit, err)
	}
}

func TestRunNoResult(t *testing.T) {
	tr, exit, _, err := Run(context.Background(), fake(t, "noresult"))
	if err != nil || tr.Result != nil || exit.ExitErr == nil || !strings.Contains(exit.Stderr, "boom") {
		t.Fatalf("tr %+v exit %+v err %v", tr, exit, err)
	}
}

func TestRunMissingBinary(t *testing.T) {
	c := fake(t, "ok")
	c.Binary = filepath.Join(t.TempDir(), "nope")
	if _, _, _, err := Run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "pip install") {
		t.Fatalf("err %v", err)
	}
}

func TestRunStepBudget(t *testing.T) {
	c := fake(t, "steps")
	c.StepBudget = 3
	start := time.Now()
	_, exit, _, _ := Run(context.Background(), c)
	if !exit.StepTripped || exit.CtxErr != nil || time.Since(start) > 10*time.Second {
		t.Fatalf("exit %+v after %v", exit, time.Since(start))
	}
}

func TestCancelSendsTermFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := fake(t, "trap")
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	tr, exit, _, _ := Run(ctx, c)
	if len(tr.Nodes) < 2 || tr.Nodes[len(tr.Nodes)-1] != "got-term" {
		t.Fatalf("SIGTERM never reached the graph: nodes %v exit %+v", tr.Nodes, exit)
	}
}

func TestCancelKillsAfterGrace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, exit, _, _ := Run(ctx, fake(t, "ignore"))
	if time.Since(start) > 5*time.Second || exit.CtxErr == nil {
		t.Fatalf("a TERM-ignoring graph was not killed: %v %+v", time.Since(start), exit)
	}
}
