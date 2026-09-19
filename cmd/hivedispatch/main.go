// Command hivedispatch is the HiveDispatch worker CLI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/dispatch"
	exfake "github.com/thomasmeadows/hivedispatch/internal/executor/fake"
	"github.com/thomasmeadows/hivedispatch/internal/githost/github"
	gitws "github.com/thomasmeadows/hivedispatch/internal/gitops/git"
	"github.com/thomasmeadows/hivedispatch/internal/schedule"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/state/gitbranch"
	"github.com/thomasmeadows/hivedispatch/internal/state/localdir"
	"github.com/thomasmeadows/hivedispatch/internal/state/router"
	"github.com/thomasmeadows/hivedispatch/internal/tracker/jira"
	"github.com/thomasmeadows/hivedispatch/internal/triage/passthrough"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: hivedispatch <command> [flags]

commands:
  version                     print the version
  check [-config P] [-jira]   validate the worker config; -jira verifies against the live site
  init  -jira [-config P]     create the claim custom fields in Jira and print their IDs
  run   [-config P] [-once] [-placeholder]
                              poll and dispatch (fake executor until Phase 4; -placeholder
                              makes it write a file so the branch/PR path is exercised)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "hivedispatch %s\n", version)
		return 0
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "run":
		return runRun(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	live := fs.Bool("jira", false, "also verify against the live Jira site")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "config ok: agent %s, %d repo(s), jira %s\n", cfg.AgentID, len(cfg.Repos), cfg.Jira.BaseURL)
	if !*live {
		return 0
	}
	client, err := jira.New(cfg.Jira)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rep, err := client.Check(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "jira check failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "jira ok: authenticated as %s; trigger JQL matches %d ticket(s)\n", rep.User, rep.SampleTickets)
	for _, f := range rep.MissingFields {
		fmt.Fprintf(stderr, "missing custom field: %s (run `hivedispatch init -jira`)\n", f)
	}
	for _, s := range rep.MissingStatuses {
		fmt.Fprintf(stderr, "missing workflow status: %q (see docs/setup.md)\n", s)
	}
	if !rep.OK() {
		return 1
	}
	return 0
}

func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	doJira := fs.Bool("jira", false, "create the claim custom fields in Jira")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*doJira {
		fmt.Fprintln(stderr, "init: nothing to do (pass -jira)")
		return 2
	}
	// The claim field IDs are what init produces, so they are not required yet.
	if err := os.Setenv("HIVE_INIT", "1"); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client, err := jira.New(cfg.Jira)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ids, err := client.EnsureFields(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "init failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "jira fields ready. Put this in your config:\n\njira:\n  fields:\n    agent_id: %s\n    claimed_at: %s\n", ids.AgentID, ids.ClaimedAt)
	return 0
}

func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	once := fs.Bool("once", false, "poll once and exit")
	placeholder := fs.Bool("placeholder", false, "fake executor writes a placeholder file so the branch/PR path is exercised")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	tr, err := jira.New(cfg.Jira)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sched, err := schedule.Parse(cfg.RunWindows)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	ws := gitws.New(filepath.Join(cfg.Workroot, "repos"))
	stores := map[string]state.RunStore{}
	for _, repo := range cfg.Repos {
		base, err := ws.EnsureBase(ctx, repo)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var st state.RunStore
		if cfg.StateStore == "local" {
			st = localdir.New(filepath.Join(cfg.Workroot, "state", strings.ReplaceAll(repo.Name, "/", "__")))
		} else {
			st, err = gitbranch.Open(ctx, base, filepath.Join(ws.RepoDir(repo), ".state"))
			if err != nil {
				fmt.Fprintln(stderr, "state branch:", err)
				return 1
			}
		}
		stores[strings.ToUpper(repo.JiraProject)] = st
	}
	host, err := github.New(cfg.GitHub)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ex := exfake.New()
	ex.Placeholder = *placeholder
	d := &dispatch.Dispatcher{
		Cfg:        dispatch.ConfigFrom(cfg),
		Tracker:    tr,
		Triager:    passthrough.Triager{},
		Executor:   ex, // Phase 4 replaces this with the Claude Code adapter
		Workspaces: ws,
		Host:       host,
		Store:      &router.Store{Stores: stores},
		Schedule:   sched,
		Log:        logger,
	}
	logger.Info("starting", "agent", cfg.AgentID, "executor", d.Executor.Name(), "once", *once)

	// First signal drains: stop polling, let the current run finish.
	// Second signal cancels the run; cleanup still posts comments.
	loopCtx, stopLoop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopLoop()
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	go func() {
		<-loopCtx.Done()
		logger.Info("draining; press Ctrl-C again to interrupt the current run")
		stopLoop()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		cancelRun()
	}()

	if *once {
		n, err := d.Once(runCtx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "handled %d ticket(s)\n", n)
		return 0
	}
	if err := d.Run(loopCtx, runCtx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
