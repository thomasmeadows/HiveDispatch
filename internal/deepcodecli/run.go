package deepcodecli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	waitDelay   = 5 * time.Second
	maxOutBytes = 256 << 10
	defaultPoll = time.Second
)

// Cmd describes one headless DeepCode run.
type Cmd struct {
	Binary     string
	Dir        string   // the workspace
	Prompt     string   // passed as --prompt=...
	Resume     string   // a session id to continue with -r
	StepBudget int      // tool calls before the run is stopped; 0 = unlimited
	Env        []string // extra KEY=VALUE entries (later wins)
	Home       string   // DeepCode's data directory; "" = ~/.deepcode
	Poll       time.Duration
}

// Exit describes how the process ended.
type Exit struct {
	CtxErr      error // the caller's context error, if any
	StepTripped bool  // the step budget stopped the run
	ExitErr     error // from Wait
	Stdout      string
	Stderr      string
}

type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := maxOutBytes - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Args is the argv for a run: exec mode, the prompt (as --prompt= so a
// prompt starting with "-" is not read as a flag) and the session to resume.
func Args(prompt, resume string) []string {
	args := []string{"-x", "--prompt=" + prompt}
	if resume != "" {
		args = append(args, "-r", resume)
	}
	return args
}

// Run executes c and reads its session. The error is non-nil only when
// DeepCode could not be started.
func Run(ctx context.Context, c Cmd) (Transcript, Exit, string, error) {
	home := c.Home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Transcript{}, Exit{}, "", err
		}
		home = filepath.Join(h, ".deepcode")
	}
	poll := c.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	before := Sessions(home)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(runCtx, c.Binary, Args(c.Prompt, c.Resume)...)
	cmd.Dir = c.Dir
	cmd.Stdin = nil // piped stdin would become extra context; there is none
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole group so the commands DeepCode started die with it.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = waitDelay
	stdout, stderr := &boundedBuffer{}, &boundedBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return Transcript{}, Exit{}, "", fmt.Errorf("start %s: %w (install DeepCode with `npm i -g @vegamo/deepcode-cli`, or set deepcode.binary)", c.Binary, err)
		}
		return Transcript{}, Exit{}, "", fmt.Errorf("start %s: %w", c.Binary, err)
	}

	// DeepCode prints nothing until it is done, so the step budget is
	// enforced by watching its session file.
	var mu sync.Mutex
	tripped := false
	session := ""
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
			}
			mu.Lock()
			if session == "" {
				session = FindSession(home, c.Dir, c.Resume, before)
			}
			f := session
			mu.Unlock()
			if f != "" && c.StepBudget > 0 && CountToolCalls(f) > c.StepBudget {
				mu.Lock()
				tripped = true
				mu.Unlock()
				cancel()
				return
			}
		}
	}()
	waitErr := cmd.Wait()
	close(done)

	mu.Lock()
	t, f := tripped, session
	mu.Unlock()
	if f == "" {
		f = FindSession(home, c.Dir, c.Resume, before)
	}
	exit := Exit{CtxErr: ctx.Err(), StepTripped: t, ExitErr: waitErr, Stdout: stdout.String(), Stderr: stderr.String()}
	if t {
		exit.CtxErr = nil // the cancellation was ours, not the caller's
	}
	var tr Transcript
	if f != "" {
		parsed, err := ParseSession(f, c.Dir)
		if err == nil {
			tr = parsed
		}
	}
	var log strings.Builder
	fmt.Fprintf(&log, "deepcode %s\nsession: %s\n--- stdout\n%s\n--- stderr\n%s\n", strings.Join(Args("…", c.Resume), " "), f, exit.Stdout, exit.Stderr)
	return tr, exit, log.String(), nil
}
