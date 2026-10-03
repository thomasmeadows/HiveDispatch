package openclawcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "openclaw")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunBoundsOutput(t *testing.T) {
	bin := fakeCLI(t, "head -c 2200000 /dev/zero | tr '\\000' 'x'\nsleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, exit, log, err := Run(ctx, Cmd{Binary: bin})
	if err != nil {
		t.Fatal(err)
	}
	if exit.ParseErr == nil || !strings.Contains(exit.ParseErr.Error(), "exceeds") || ctx.Err() != nil || len(log) > 2200000 {
		t.Fatalf("exit %+v, log bytes %d", exit, len(log))
	}
}

func TestRunDrainsDiagnosticsAndBackgroundPipes(t *testing.T) {
	bin := fakeCLI(t, `head -c 100000 /dev/zero | tr '\000' 'x' >&2
sleep 30 &
printf '%s' '{"ok":true,"status":"ok","final":"done"}'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tr, exit, log, err := Run(ctx, Cmd{Binary: bin})
	if err != nil {
		t.Fatal(err)
	}
	if exit.CtxErr != nil || exit.ExitErr != nil || exit.ParseErr != nil || !tr.OK || len(exit.Stderr) != 65536 || len(log) > 66000 {
		t.Fatalf("result %+v, ctx %v, exit %v, parse %v, stderr bytes %d, log bytes %d", tr, exit.CtxErr, exit.ExitErr, exit.ParseErr, len(exit.Stderr), len(log))
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := Run(ctx, Cmd{Binary: "unused"})
	if err == nil {
		t.Fatal("started with canceled context")
	}
	bin := fakeCLI(t, "sleep 30\n")
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	_, exit, _, err := Run(ctx, Cmd{Binary: bin})
	if err != nil || !errors.Is(exit.CtxErr, context.Canceled) {
		t.Fatalf("exit %+v, error %v", exit, err)
	}
}
