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
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/executor/claudecode"
	exfake "github.com/thomasmeadows/hivedispatch/internal/executor/fake"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	"github.com/thomasmeadows/hivedispatch/internal/githost/github"
	"github.com/thomasmeadows/hivedispatch/internal/githost/none"
	gitws "github.com/thomasmeadows/hivedispatch/internal/gitops/git"
	"github.com/thomasmeadows/hivedispatch/internal/schedule"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/state/gitbranch"
	"github.com/thomasmeadows/hivedispatch/internal/state/localdir"
	"github.com/thomasmeadows/hivedispatch/internal/state/router"
	"github.com/thomasmeadows/hivedispatch/internal/tracker/jira"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
	triclaude "github.com/thomasmeadows/hivedispatch/internal/triage/claudecode"
	"github.com/thomasmeadows/hivedispatch/internal/triage/passthrough"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: hivedispatch <command> [flags]

commands:
  version                     print the version
  check [-config P] [-jira]   validate the worker config; -jira verifies against the live site
  init  [-config P]           write a commented starter config (never overwrites)
  init  -jira [-config P]     create the claim custom fields in Jira and print their IDs
  run   [-config P] [-once] [-executor claude|fake] [-triage claude|passthrough]
        [-placeholder] [-skip-preflight]
                              verify Jira setup, then poll and dispatch; -executor and -triage override the config
                              (-placeholder makes the fake executor write a file so
                              the branch/PR path is exercised)
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
	if _, src := github.DiscoverToken(context.Background(), "github.com"); src != "" {
		fmt.Fprintf(stdout, "github ok: token from %s\n", src)
	} else {
		fmt.Fprintln(stdout, "github: no token found (HIVE_GITHUB_TOKEN, gh auth token, or git credential helper) — branches will be pushed but PRs will not be opened")
	}
	if !*live {
		return 0
	}
	client, err := jira.New(cfg.Jira)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !jiraPreflight(context.Background(), client, cfg, stdout, stderr) {
		return 1
	}
	return 0
}

// jiraPreflight runs the live Jira check and prints the report. It returns
// false when something would prevent a run.
func jiraPreflight(ctx context.Context, client *jira.Client, cfg *config.Config, stdout, stderr io.Writer) bool {
	var projects []string
	for _, r := range cfg.Repos {
		projects = append(projects, r.JiraProject)
	}
	rep, err := client.Check(ctx, projects)
	if err != nil {
		fmt.Fprintln(stderr, "jira check failed:", err)
		return false
	}
	fmt.Fprintf(stdout, "jira ok: authenticated as %s; trigger JQL matches %d ticket(s)\n", rep.User, rep.SampleTickets)
	for _, p := range rep.UnknownProjects {
		fmt.Fprintf(stderr, "repos: jira_project %q does not exist on this site; projects here: %s\n", p, strings.Join(rep.Projects, ", "))
	}
	for _, f := range rep.MissingFields {
		fmt.Fprintf(stderr, "missing custom field: %s (run `hivedispatch init -jira`)\n", f)
	}
	for _, d := range rep.DuplicateFields {
		fmt.Fprintf(stderr, "warning: several custom fields share a claim-field name (%s); using the lowest id — delete the others in Jira: Settings → Issues → Custom fields\n", d)
	}
	for _, s := range rep.MissingStatuses {
		fmt.Fprintf(stderr, "missing workflow status: %q (see docs/setup.md)\n", s)
	}
	switch {
	case rep.NotEditableOn != "":
		fmt.Fprintf(stderr, "claim fields exist but cannot be set on %s.\n  Team-managed project: Project settings → Issue types → each type → Fields panel → search \"HiveDispatch\", add both fields, then Save changes.\n  Company-managed project: add both fields to the project's edit screen.\n", rep.NotEditableOn)
	case rep.SampleIssue != "":
		fmt.Fprintf(stdout, "claim fields are editable (checked on %s)\n", rep.SampleIssue)
	default:
		fmt.Fprintln(stdout, "no issue found to verify the claim fields are editable; create one in the project and run check again")
	}
	return rep.OK()
}

func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	doJira := fs.Bool("jira", false, "create the claim custom fields in Jira")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	written, err := config.WriteStarter(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if written {
		fmt.Fprintf(stdout, "wrote starter config to %s\n\nEdit it (every field says where its value comes from), export HIVE_JIRA_TOKEN, then run:\n  hivedispatch init -jira\n", *cfgPath)
		if !*doJira {
			return 0
		}
		fmt.Fprintln(stderr, "\ninit -jira: fill in the config first, then run this again.")
		return 1
	}
	if !*doJira {
		fmt.Fprintf(stdout, "config already exists at %s\nNext: export HIVE_JIRA_TOKEN and run `hivedispatch init -jira`\n", *cfgPath)
		return 0
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n\nEdit %s and run `hivedispatch init -jira` again.\n", err, *cfgPath)
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
	fmt.Fprintf(stdout, "jira claim fields ready:\n  %s = %s  (which worker holds a ticket)\n  %s = %s  (that worker's last heartbeat)\n\nThe worker finds them by name, so nothing needs pasting into %s.\nNext: hivedispatch check -jira\n", jira.FieldNameAgent, ids.AgentID, jira.FieldNameClaimedAt, ids.ClaimedAt, *cfgPath)
	return 0
}

func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	once := fs.Bool("once", false, "poll once and exit")
	executorFlag := fs.String("executor", "", "override config executor: claude or fake")
	triageFlag := fs.String("triage", "", "override config triage: claude or passthrough")
	skipPreflight := fs.Bool("skip-preflight", false, "start without verifying Jira fields, statuses, and projects")
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
	if err := tr.ResolveFields(context.Background()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !*skipPreflight && !jiraPreflight(context.Background(), tr, cfg, stdout, stderr) {
		fmt.Fprintln(stderr, "not starting: fix the above, or pass -skip-preflight")
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
	var host githost.GitHost = none.Host{}
	if tok, src := github.DiscoverToken(ctx, "github.com"); src != "" {
		cfg.GitHub.Token = tok
		host, err = github.New(cfg.GitHub)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		logger.Info("github token", "source", src)
	} else {
		logger.Warn("no GitHub token found; branches will be pushed but pull requests will not be opened")
	}
	name := cfg.Executor
	if *executorFlag != "" {
		name = *executorFlag
	}
	var ex executor.Executor
	switch name {
	case "fake":
		f := exfake.New()
		f.Placeholder = *placeholder
		ex = f
	case "claude":
		ex = claudecode.New(claudecode.Config{Binary: cfg.Claude.Binary, Model: cfg.Claude.Model})
	default:
		fmt.Fprintf(stderr, "unknown executor %q\n", name)
		return 2
	}
	triKind := cfg.Triage.Kind
	if *triageFlag != "" {
		triKind = *triageFlag
	}
	var tri triage.Triager
	switch triKind {
	case "passthrough":
		tri = passthrough.Triager{}
	case "claude":
		tri = triclaude.New(triclaude.Config{Binary: cfg.Claude.Binary, Model: cfg.Triage.Model, StepBudget: cfg.Triage.StepBudget, Timeout: cfg.Triage.Timeout})
	default:
		fmt.Fprintf(stderr, "unknown triage %q\n", triKind)
		return 2
	}
	d := &dispatch.Dispatcher{
		Cfg:        dispatch.ConfigFrom(cfg),
		Tracker:    tr,
		Triager:    tri,
		Executor:   ex,
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
