package graphcli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxLineBytes   = 1 << 20
	defaultMaxLog  = 2 << 20
	defaultGrace   = 15 * time.Second
	maxStderrBytes = 64 << 10
)

// Cmd describes one hivegraph invocation.
type Cmd struct {
	Binary     string
	Dir        string
	Args       []string
	Stdin      string
	StepBudget int           // tool steps before the run is stopped; 0 = unlimited
	Env        []string      // extra KEY=VALUE entries (later wins)
	Grace      time.Duration // between SIGTERM and SIGKILL; 0 = 15s
	MaxLog     int           // 0 = defaultMaxLog
}

// Exit describes how the process ended.
type Exit struct {
	CtxErr      error // the caller's context error, if any
	StepTripped bool  // the step budget stopped the run
	ExitErr     error // from Wait
	Stderr      string
}

type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	n   int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.n - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Run executes c and parses its events. It returns the transcript, how the
// process ended and the captured log. The error is non-nil only when
// hivegraph could not be started.
func Run(ctx context.Context, c Cmd) (Transcript, Exit, string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	tripped := false
	parser := NewParser(func(n int) {
		if c.StepBudget > 0 && n > c.StepBudget {
			mu.Lock()
			tripped = true
			mu.Unlock()
			cancel()
		}
	})
	grace := c.Grace
	if grace == 0 {
		grace = defaultGrace
	}
	maxLog := c.MaxLog
	if maxLog == 0 {
		maxLog = defaultMaxLog
	}
	cmd := exec.CommandContext(runCtx, c.Binary, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// SIGTERM first: agent-run turns it into killing the CLI's own
		// process group. WaitDelay then escalates to SIGKILL.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = grace
	logBuf := &boundedBuffer{n: maxLog}
	stderr := &boundedBuffer{n: maxStderrBytes}
	cmd.Stderr = io.MultiWriter(stderr, logBuf)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Transcript{}, Exit{}, "", err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return Transcript{}, Exit{}, "", fmt.Errorf("start %s: %w (install hivegraph — `hivedispatch check` prints the command for this version — or set graph.binary)", c.Binary, err)
		}
		return Transcript{}, Exit{}, "", fmt.Errorf("start %s: %w", c.Binary, err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		_, _ = logBuf.Write(append(append([]byte(nil), line...), '\n'))
		parser.Line(line)
	}
	waitErr := cmd.Wait()
	if runCtx.Err() != nil {
		// WaitDelay killed only the leader; take the rest of the group too.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	mu.Lock()
	t := tripped
	mu.Unlock()
	exit := Exit{CtxErr: ctx.Err(), StepTripped: t, ExitErr: waitErr, Stderr: stderr.String()}
	if t {
		exit.CtxErr = nil // the cancellation was ours, not the caller's
	}
	return parser.Transcript(), exit, logBuf.String(), nil
}
