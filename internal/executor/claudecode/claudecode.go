package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

const (
	maxLineBytes = 1 << 20 // one stream-json line
	maxLogBytes  = 2 << 20 // kept from stdout+stderr for Result.Log
	waitDelay    = 5 * time.Second
)

// Executor runs Claude Code headless.
type Executor struct {
	cfg Config
}

var _ executor.Executor = (*Executor)(nil)

// New returns an Executor; an empty Binary means "claude" on PATH.
func New(cfg Config) *Executor {
	if cfg.Binary == "" {
		cfg.Binary = "claude"
	}
	return &Executor{cfg: cfg}
}

// Name implements executor.Executor.
func (e *Executor) Name() string { return "claude-code" }

// boundedBuffer keeps the first n bytes written to it.
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

func (e *Executor) command(ctx context.Context, dir string, args []string, stdin string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.cfg.Binary, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "CLAUDECODE=") // never inherit a parent session marker
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole group so tool subprocesses die with the agent.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = waitDelay
	return cmd
}

// Run implements executor.Executor.
func (e *Executor) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	rc, err := repoconfig.Load(t.Workspace)
	if err != nil {
		return executor.Result{}, err
	}
	promptText := t.Prompt
	if g := strings.TrimSpace(rc.Guidance); g != "" {
		promptText += "\n\n## Repository guidance\n\n" + g + "\n"
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stepTripped bool
	var stepMu sync.Mutex
	parser := newStreamParser(t.Workspace, func(count int) {
		if t.StepBudget > 0 && count > t.StepBudget {
			stepMu.Lock()
			stepTripped = true
			stepMu.Unlock()
			cancel()
		}
	})

	cmd := e.command(runCtx, t.Workspace, buildArgs(e.cfg, rc.Executor, t.ResumeToken, false), promptText)
	logBuf := &boundedBuffer{n: maxLogBytes}
	stderr := &boundedBuffer{n: 64 << 10}
	cmd.Stderr = io.MultiWriter(stderr, logBuf)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return executor.Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return executor.Result{}, fmt.Errorf("start %s: %w", e.cfg.Binary, err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		_, _ = logBuf.Write(append(append([]byte(nil), line...), '\n'))
		parser.Line(line)
	}
	waitErr := cmd.Wait()

	stepMu.Lock()
	tripped := stepTripped
	stepMu.Unlock()
	exit := exitInfo{CtxErr: ctx.Err(), StepTripped: tripped, ExitErr: waitErr, Stderr: stderr.String()}
	if tripped {
		exit.CtxErr = nil // the cancellation was ours, not the caller's
	}
	res := mapOutcome(parser.Transcript(), exit)
	res.Log = logBuf.String()
	return res, nil
}

// Plan implements executor.Executor by running in plan mode with a JSON
// schema and reading the declared file list.
func (e *Executor) Plan(ctx context.Context, t executor.Task) (executor.Footprint, error) {
	rc, err := repoconfig.Load(t.Workspace)
	if err != nil {
		return executor.Footprint{}, err
	}
	planPrompt := t.Prompt + "\n\nDo not make changes. List every file you would create or modify to complete this ticket, as repository-relative paths, in the requested JSON shape.\n"
	cmd := e.command(ctx, t.Workspace, buildArgs(e.cfg, rc.Executor, "", true), planPrompt)
	out, err := cmd.Output()
	if err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: %w", err)
	}
	var r resultMsg
	if err := json.Unmarshal(out, &r); err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: parse result: %w", err)
	}
	if r.IsError {
		return executor.Footprint{}, fmt.Errorf("plan: %s", truncate(r.Result, 500))
	}
	var fp struct {
		Files []string `json:"files"`
	}
	raw := []byte(r.Result)
	if len(r.StructuredOutput) > 0 {
		raw = r.StructuredOutput
	}
	if err := json.Unmarshal(raw, &fp); err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: parse footprint: %w", err)
	}
	return executor.Footprint{Files: fp.Files}, nil
}
