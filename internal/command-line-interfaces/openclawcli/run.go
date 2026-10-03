// Package openclawcli runs OpenClaw's headless agent exec command.
package openclawcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const maxOutput = 2 << 20
const waitDelay = 5 * time.Second

// Cmd describes an invocation. Args are passed without a shell.
type Cmd struct {
	Binary, Dir, Stdin string
	Args, Env          []string
}

// Result is the stable agent exec JSON envelope. Session IDs are ephemeral.
type Result struct {
	OK       bool   `json:"ok"`
	Status   string `json:"status"`
	Final    string `json:"final"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Usage    struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"usage"`
	CostUSD float64 `json:"costUsd"`
	Error   *struct {
		Message string `json:"message"`
		Kind    string `json:"kind"`
	} `json:"error"`
}

// Exit records process and parsing failures separately from start errors.
type Exit struct {
	CtxErr, ExitErr, ParseErr error
	Stderr                    string
}

type boundedBuffer struct{ buf bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := (64 << 10) - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		_, _ = b.buf.Write(p)
	}
	return n, nil
}

// Run drains both pipes, caps output, and kills tool subprocesses on exit or
// cancellation. Only a failure to start returns a non-nil final error.
func Run(ctx context.Context, c Cmd) (Result, Exit, string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, c.Binary, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	stdout, outWrite, err := os.Pipe()
	if err != nil {
		return Result{}, Exit{}, "", err
	}
	defer func() { _ = stdout.Close(); _ = outWrite.Close() }()
	stderr, errWrite, err := os.Pipe()
	if err != nil {
		return Result{}, Exit{}, "", err
	}
	defer func() { _ = stderr.Close(); _ = errWrite.Close() }()
	cmd.Stdout, cmd.Stderr = outWrite, errWrite
	if err := cmd.Start(); err != nil {
		return Result{}, Exit{}, "", fmt.Errorf("start %s: %w", c.Binary, err)
	}
	_ = outWrite.Close()
	_ = errWrite.Close()
	var output []byte
	var result Result
	outDone := make(chan error, 1)
	go func() {
		raw, readErr := io.ReadAll(io.LimitReader(stdout, maxOutput+1))
		if len(raw) > maxOutput {
			raw = raw[:maxOutput]
			readErr = errors.New("OpenClaw output exceeds 2 MiB")
		}
		output = raw
		if readErr == nil {
			readErr = json.Unmarshal(raw, &result)
		}
		if readErr != nil {
			cancel()
		}
		outDone <- readErr
	}()
	var diagnostic boundedBuffer
	errDone := make(chan error, 1)
	go func() { _, err := io.Copy(&diagnostic, stderr); errDone <- err }()
	waitErr := cmd.Wait()
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		waitErr = errors.Join(waitErr, fmt.Errorf("stop OpenClaw process group: %w", err))
	}
	timer := time.AfterFunc(waitDelay, func() { _ = stdout.Close(); _ = stderr.Close() })
	parseErr := <-outDone
	stderrErr := <-errDone
	timer.Stop()
	if stderrErr != nil {
		waitErr = errors.Join(waitErr, fmt.Errorf("read OpenClaw stderr: %w", stderrErr))
	}
	exit := Exit{CtxErr: ctx.Err(), ExitErr: waitErr, ParseErr: parseErr, Stderr: diagnostic.buf.String()}
	return result, exit, string(output) + "\n" + exit.Stderr, nil
}
