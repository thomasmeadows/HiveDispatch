package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
)

// REPL is the terminal front end of the agent.
type REPL struct {
	agent       *Agent
	mem         *Memory
	session     string
	modelName   string
	configPath  string
	exe         string
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	interactive bool
	now         func() time.Time
	lines       *lineReader
}

// lineReader reads stdin on a goroutine so the loop can select between a
// line and a signal. Confirm and the prompt share it.
type lineReader struct {
	once sync.Once
	ch   chan string
	done chan struct{}
	r    io.Reader
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{ch: make(chan string), done: make(chan struct{}), r: r}
}

// start launches the scanning goroutine once; later calls are no-ops.
func (l *lineReader) start() {
	l.once.Do(func() {
		go func() {
			sc := bufio.NewScanner(l.r)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				l.ch <- sc.Text()
			}
			close(l.done)
		}()
	})
}

// next returns the next line, or ok=false at EOF.
func (l *lineReader) next() (string, bool) {
	l.start()
	select {
	case s := <-l.ch:
		return s, true
	case <-l.done:
		return "", false
	}
}

// Banner is the first line printed.
func (r *REPL) Banner() string {
	state := "missing"
	if _, err := os.Stat(r.configPath); err == nil {
		state = "exists"
	}
	_, n := r.mem.NotesForPrompt()
	return fmt.Sprintf("supervisor: %s · config %s (%s) · notes %d lines", r.modelName, r.configPath, state, n)
}

// Confirm asks the operator; a non-interactive session always declines.
func (r *REPL) Confirm(prompt string) bool {
	if !r.interactive {
		return false
	}
	fmt.Fprint(r.stderr, "\n"+prompt+" [y/N] ")
	line, ok := r.lines.next()
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// system builds the system prompt fresh from the current config, notes and
// check output.
func (r *REPL) system() string {
	notes, n := r.mem.NotesForPrompt()
	_, statErr := os.Stat(r.configPath)
	return BuildSystem(PromptInput{
		ConfigPath: r.configPath, ConfigExists: statErr == nil, ModelName: r.modelName,
		Notes: notes, NoteLines: n, CheckOutput: CheckOutput(context.Background(), r.exe, r.configPath),
	})
}

// onEvent prints agent progress (tool calls, budget) to stderr as it runs.
func (r *REPL) onEvent(e Event) {
	switch e.Kind {
	case "tool_start":
		args := string(e.Args)
		if len(args) > 60 {
			args = args[:57] + "…"
		}
		fmt.Fprintf(r.stderr, "\r\033[K⋯ %s(%s)\n", e.Tool, args)
	case "budget":
		fmt.Fprintf(r.stderr, "\r\033[K(step budget reached)\n")
	}
}

const help = `commands:
  /help    this text
  /notes   print the notes file
  /model   which provider and model is answering
  /reset   start a new session (notes are kept)
  /quit    exit (Ctrl-D also exits)
Ctrl-C during a reply cancels it; at the prompt, exits.`

// Run drives the session until exit.
func (r *REPL) Run(ctx context.Context) error {
	if !r.interactive {
		raw, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			return errors.New("nothing to ask: pass a message on stdin")
		}
		return r.turn(ctx, msg)
	}
	fmt.Fprintln(r.stdout, r.Banner())
	fmt.Fprintln(r.stdout, "Type a message; /help for commands, Ctrl-D or /quit to exit.")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	for {
		fmt.Fprint(r.stdout, "\n> ")
		var line string
		var ok bool
		select {
		case <-sig:
			fmt.Fprintln(r.stdout)
			return nil
		case <-ctx.Done():
			return nil
		case res := <-r.lineOrEOF():
			line, ok = res.s, res.ok
		}
		if !ok {
			fmt.Fprintln(r.stdout)
			return nil
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case line == "/quit" || line == "/exit":
			return nil
		case line == "/help":
			fmt.Fprintln(r.stdout, help)
			continue
		case line == "/model":
			fmt.Fprintln(r.stdout, r.modelName)
			continue
		case line == "/notes":
			n, err := r.mem.Notes()
			switch {
			case err != nil:
				fmt.Fprintln(r.stderr, "error:", err)
			case n == "":
				fmt.Fprintln(r.stdout, "(no notes yet)")
			default:
				fmt.Fprint(r.stdout, n)
			}
			continue
		case line == "/reset":
			r.agent.SetHistory(nil)
			r.session = r.mem.NewSessionName(r.now())
			fmt.Fprintln(r.stdout, "new session")
			continue
		case strings.HasPrefix(line, "/"):
			fmt.Fprintln(r.stdout, "unknown command; /help lists them")
			continue
		}
		turnCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			select {
			case <-sig:
				cancel()
			case <-done:
			}
		}()
		err := r.turn(turnCtx, line)
		close(done)
		cancel()
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(r.stderr, "\r\033[Kinterrupted")
		} else if err != nil {
			fmt.Fprintln(r.stderr, "\r\033[Kerror:", err)
		}
	}
}

// lineOrEOF adapts the line reader to a channel of (line, ok) for select.
func (r *REPL) lineOrEOF() <-chan lineResult {
	ch := make(chan lineResult, 1)
	go func() {
		s, ok := r.lines.next()
		ch <- lineResult{s, ok}
	}()
	return ch
}

type lineResult struct {
	s  string
	ok bool
}

// turn runs one user message through the agent, prints the reply and saves
// the session.
func (r *REPL) turn(ctx context.Context, msg string) error {
	stop := r.spinner()
	reply, err := r.agent.Turn(ctx, msg)
	stop()
	if err != nil {
		return err
	}
	fmt.Fprintln(r.stdout, reply)
	if err := r.mem.SaveSession(r.session, r.agent.History()); err != nil {
		fmt.Fprintln(r.stderr, "warning: session not saved:", err)
	}
	return nil
}

// spinner shows "thinking…" on stderr until the returned stop is called.
func (r *REPL) spinner() (stop func()) {
	if !r.interactive {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		dots := 0
		fmt.Fprint(r.stderr, "thinking")
		for {
			select {
			case <-done:
				fmt.Fprint(r.stderr, "\r\033[K")
				return
			case <-t.C:
				dots = (dots + 1) % 4
				fmt.Fprintf(r.stderr, "\r\033[Kthinking%s", strings.Repeat(".", dots))
			}
		}
	}()
	return func() { close(done); <-finished }
}
