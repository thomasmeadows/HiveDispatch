package antigravitycli

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
	waitDelay      = 5 * time.Second
	maxStderrBytes = 64 << 10
)

// Cmd describes one headless invocation.
type Cmd struct {
	Binary     string
	Dir        string
	Args       []string
	Stdin      string
	StepBudget int      // 0 = unlimited
	MaxLog     int      // 0 = defaultMaxLog
	Env        []string // extra KEY=VALUE entries appended to the environment (later wins)
}

// Exit describes how the process ended.
type Exit struct {
	CtxErr      error // the caller's context error, if any
	StepTripped bool  // the step budget cancelled the run
	ExitErr     error // from Wait
	ParseErr    error // malformed or oversized output
	Stderr      string
}

type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	n   int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	size := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.n - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return size, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Command builds an *exec.Cmd with process-group kill on cancel.
func Command(ctx context.Context, c Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Binary, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole group so tool subprocesses die with the agent.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = waitDelay
	return cmd
}

// Run executes c, parsing the JSONL event stream from stdout. It returns the
// parsed transcript, how the process ended, and the captured log. The error
// is non-nil only when the process could not be started.
func Run(ctx context.Context, c Cmd) (Transcript, Exit, string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var tripped bool
	var mu sync.Mutex
	parser := NewParser(func(count int) {
		if c.StepBudget > 0 && count > c.StepBudget {
			mu.Lock()
			tripped = true
			mu.Unlock()
			cancel()
		}
	})
	maxLog := c.MaxLog
	if maxLog == 0 {
		maxLog = defaultMaxLog
	}
	cmd := Command(runCtx, c)
	logBuf := &boundedBuffer{n: maxLog}
	stderr := &boundedBuffer{n: maxStderrBytes}
	// Own both pipes so Wait can observe the CLI exit without waiting for
	// descendants to close their inherited descriptors. StdoutPipe cannot
	// safely be read concurrently with Wait, which closes it internally.
	stdout, stdoutWrite, err := os.Pipe()
	if err != nil {
		return Transcript{}, Exit{}, "", err
	}
	defer func() { _ = stdout.Close(); _ = stdoutWrite.Close() }()
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		return Transcript{}, Exit{}, "", err
	}
	defer func() { _ = stderrRead.Close(); _ = stderrWrite.Close() }()
	cmd.Stdout, cmd.Stderr = stdoutWrite, stderrWrite
	if err := cmd.Start(); err != nil {
		return Transcript{}, Exit{}, "", fmt.Errorf("start %s: %w", c.Binary, err)
	}
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()

	outputDone := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), maxLineBytes)
		for sc.Scan() {
			line := sc.Bytes()
			_, _ = logBuf.Write(append(append([]byte(nil), line...), '\n'))
			if err := parser.Line(line); err != nil {
				cancel()
				outputDone <- err
				return
			}
		}
		if sc.Err() != nil {
			cancel()
		}
		outputDone <- sc.Err()
	}()
	stderrDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.MultiWriter(stderr, logBuf), stderrRead)
		stderrDone <- err
	}()

	waitErr := cmd.Wait()
	// A completed ticket must not leave tools running. This also releases
	// the inherited pipe ends held by background commands after CLI exit.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		waitErr = errors.Join(waitErr, fmt.Errorf("stop antigravity process group: %w", err))
	}
	// A descendant can escape the group; never let its descriptors hang us.
	drainTimer := time.AfterFunc(waitDelay, func() { _ = stdout.Close(); _ = stderrRead.Close() })
	parseErr := <-outputDone
	stderrErr := <-stderrDone
	drainTimer.Stop()
	if stderrErr != nil {
		waitErr = errors.Join(waitErr, fmt.Errorf("read antigravity stderr: %w", stderrErr))
	}
	mu.Lock()
	t := tripped
	mu.Unlock()
	exit := Exit{ParseErr: parseErr, CtxErr: ctx.Err(), StepTripped: t, ExitErr: waitErr, Stderr: stderr.String()}
	if t {
		exit.CtxErr = nil // the cancellation was ours, not the caller's
	}
	return parser.Transcript(), exit, logBuf.String(), nil
}
