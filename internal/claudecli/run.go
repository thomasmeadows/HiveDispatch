package claudecli

import (
	"bufio"
	"bytes"
	"context"
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
	StepBudget int // 0 = unlimited
	MaxLog     int // 0 = defaultMaxLog
}

// Exit describes how the process ended.
type Exit struct {
	CtxErr      error // the caller's context error, if any
	StepTripped bool  // the step budget cancelled the run
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

// Command builds an *exec.Cmd with process-group kill on cancel. Exported so
// callers that want the plain JSON output (Plan) can use cmd.Output.
func Command(ctx context.Context, c Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Binary, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Env = append(os.Environ(), "CLAUDECODE=") // never inherit a parent session marker
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole group so tool subprocesses die with the agent.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = waitDelay
	return cmd
}

// Run executes c, parsing stream-json from stdout. It returns the parsed
// transcript, how the process ended, and the captured log. The error is
// non-nil only when the process could not be started.
func Run(ctx context.Context, c Cmd) (Transcript, Exit, string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var tripped bool
	var mu sync.Mutex
	parser := NewParser(c.Dir, func(count int) {
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
	cmd.Stderr = io.MultiWriter(stderr, logBuf)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Transcript{}, Exit{}, "", err
	}
	if err := cmd.Start(); err != nil {
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
	mu.Lock()
	t := tripped
	mu.Unlock()
	exit := Exit{CtxErr: ctx.Err(), StepTripped: t, ExitErr: waitErr, Stderr: stderr.String()}
	if t {
		exit.CtxErr = nil // the cancellation was ours, not the caller's
	}
	return parser.Transcript(), exit, logBuf.String(), nil
}
