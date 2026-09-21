package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor"
)

// runSupervisor starts the interactive assistant, or answers one piped
// message when stdin is not a terminal.
func runSupervisor(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("supervisor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	provider := fs.String("provider", "", "override the supervisor config: "+supervisor.Providers)
	modelName := fs.String("model", "", "override the model name")
	resume := fs.Bool("resume", false, "continue the most recent session")
	session := fs.String("session", "", "continue this session file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	interactive := false
	if f, ok := stdin.(*os.File); ok {
		interactive = isTerminal(f)
	}
	ctx := context.Background()
	r, err := supervisor.New(ctx, supervisor.Options{
		WorkerConfigPath: *cfgPath, Provider: *provider, Model: *modelName,
		Resume: *resume, Session: *session, Exe: exe,
		Stdin: stdin, Stdout: stdout, Stderr: stderr, Interactive: interactive,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := r.Run(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
