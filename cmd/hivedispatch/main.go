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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/dispatch"
	"github.com/thomasmeadows/hivedispatch/internal/githost/github"
	"github.com/thomasmeadows/hivedispatch/internal/statusline"
	"github.com/thomasmeadows/hivedispatch/internal/tracker/ghissues"
	"github.com/thomasmeadows/hivedispatch/internal/tracker/jira"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: hivedispatch <command> [flags]

commands:
  version                     print the version
  check [-config P] [-live]   validate the worker config; -live verifies against the tracker and GitHub
  init  [-config P]           write a commented starter config (never overwrites)
  init  -jira [-config P]     create the claim custom fields in Jira
  init  -github [-config P]   create the hive:* state labels in each GitHub repository
  run   [-config P] [-once] [-executor claude|fake] [-triage claude|passthrough]
        [-placeholder] [-skip-preflight]
                              verify Jira setup, then poll and dispatch; -executor and -triage override the config
                              (-placeholder makes the fake executor write a file so
                              the branch/PR path is exercised)
  once  KEY [same flags as run]
                              handle one ticket by key, ignoring the trigger query and run windows
  status [-config P] [-json]  list run records from the state branch(es)
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
	case "once":
		return runOnce(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	live := fs.Bool("live", false, "also verify against the live tracker and GitHub")
	liveJira := fs.Bool("jira", false, "alias for -live")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	target := cfg.Jira.BaseURL
	if cfg.Tracker == "github" {
		target = "GitHub Issues"
	}
	fmt.Fprintf(stdout, "config ok: agent %s, %d repo(s), tracker %s (%s)\n", cfg.AgentID, len(cfg.Repos), cfg.Tracker, target)
	tok, src := github.DiscoverToken(ctx, "github.com")
	switch {
	case src != "":
		fmt.Fprintf(stdout, "github ok: token from %s\n", src)
		cfg.GitHub.Token = tok
	case cfg.Tracker == "github":
		fmt.Fprintln(stderr, "github: no token found (HIVE_GITHUB_TOKEN, gh auth token, or git credential helper) — required when tracker is github")
		return 1
	default:
		fmt.Fprintln(stdout, "github: no token found (HIVE_GITHUB_TOKEN, gh auth token, or git credential helper) — branches will be pushed but PRs will not be opened")
	}
	if !*live && !*liveJira {
		return 0
	}
	if !trackerPreflight(ctx, cfg, stdout, stderr) {
		return 1
	}
	return 0
}

// trackerPreflight runs the live check for whichever tracker is configured.
func trackerPreflight(ctx context.Context, cfg *config.Config, stdout, stderr io.Writer) bool {
	switch cfg.Tracker {
	case "github":
		client, err := ghissues.New(cfg.GitHub, cfg.Repos)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return false
		}
		return githubPreflight(ctx, client, stdout, stderr)
	default:
		client, err := jira.New(cfg.Jira)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return false
		}
		if err := client.ResolveFields(ctx); err != nil {
			fmt.Fprintln(stderr, err)
			return false
		}
		return jiraPreflight(ctx, client, cfg, stdout, stderr)
	}
}

// githubPreflight runs the live GitHub Issues check and prints the report.
func githubPreflight(ctx context.Context, client *ghissues.Client, stdout, stderr io.Writer) bool {
	rep, err := client.Check(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "github check failed:", err)
		return false
	}
	fmt.Fprintf(stdout, "github issues ok: authenticated as %s; %d repo(s); %d issue(s) labelled ready\n", rep.User, len(rep.Repos), rep.SampleTickets)
	for _, r := range rep.MissingRepos {
		fmt.Fprintf(stderr, "repo problem: %s\n", r)
	}
	for repo, labels := range rep.MissingLabels {
		fmt.Fprintf(stderr, "%s is missing labels %s (run `hivedispatch init -github`)\n", repo, strings.Join(labels, ", "))
	}
	switch {
	case rep.NotWritable != "":
		fmt.Fprintf(stderr, "the token can read but not edit issues (probed on %s).\n  Fine-grained token: https://github.com/settings/personal-access-tokens → Repository permissions → Issues: Read and write.\n  Classic token: the repo scope.\n", rep.NotWritable)
	case rep.SampleIssue != "":
		fmt.Fprintf(stdout, "token can edit issues (probed on %s)\n", rep.SampleIssue)
	default:
		fmt.Fprintln(stdout, "no ready issue to probe write access; add hive:ready to one and run check again")
	}
	return rep.OK()
}

// jiraPreflight runs the live Jira check and prints the report. It returns
// false when something would prevent a run.
func jiraPreflight(ctx context.Context, client *jira.Client, cfg *config.Config, stdout, stderr io.Writer) bool {
	var projects []string
	for _, r := range cfg.Repos {
		projects = append(projects, r.Project)
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
	doGitHub := fs.Bool("github", false, "create the state labels in each GitHub repository")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	written, err := config.WriteStarter(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if written {
		fmt.Fprintf(stdout, "wrote starter config to %s\n\nEdit it (every field says where its value comes from), then run:\n  hivedispatch init -jira      (Jira: creates the claim fields)\n  hivedispatch init -github    (GitHub Issues: creates the state labels)\n", *cfgPath)
		if !*doJira && !*doGitHub {
			return 0
		}
		fmt.Fprintln(stderr, "\ninit: fill in the config first, then run this again.")
		return 1
	}
	if !*doJira && !*doGitHub {
		fmt.Fprintf(stdout, "config already exists at %s\nNext: `hivedispatch init -jira` or `hivedispatch init -github`, then `hivedispatch check -live`\n", *cfgPath)
		return 0
	}
	if *doGitHub {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n\nEdit %s and run `hivedispatch init -github` again.\n", err, *cfgPath)
			return 1
		}
		tok, src := github.DiscoverToken(context.Background(), "github.com")
		if src == "" {
			fmt.Fprintln(stderr, "no GitHub token found: export HIVE_GITHUB_TOKEN (Issues read/write) or run `gh auth login`")
			return 1
		}
		cfg.GitHub.Token = tok
		client, err := ghissues.New(cfg.GitHub, cfg.Repos)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		n, err := client.EnsureLabels(context.Background())
		if err != nil {
			fmt.Fprintln(stderr, "init failed:", err)
			return 1
		}
		fmt.Fprintf(stdout, "github labels ready (%d created). Put %q on an issue to queue it.\nNext: hivedispatch check -live\n", n, cfg.GitHub.Labels.Ready)
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
	// On a terminal, logs scroll above a status line showing the last poll.
	// -once has no loop to report on, and a pipe has no line to rewrite.
	var status *statusline.Line
	logOut := stderr
	if !*once && isTerminal(stderr) {
		status = statusline.New(stderr)
		logOut = status
	}
	logger := slog.New(slog.NewTextHandler(logOut, nil))
	ctx := context.Background()
	w, err := newWorker(ctx, cfg, wireOptions{executor: *executorFlag, triage: *triageFlag, placeholder: *placeholder, preflight: !*skipPreflight}, logger, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	d := w.d
	if cfg.RetentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
		if n, err := w.store.Prune(ctx, cutoff); err != nil {
			logger.Warn("retention prune failed", "err", err)
		} else if n > 0 {
			logger.Info("retention", "removed", n, "before", cutoff.Format("2006-01-02"))
		}
	}
	logger.Info("starting", "agent", cfg.AgentID, "executor", d.Executor.Name(), "once", *once)

	// First signal drains: stop polling, let the current run finish.
	// Second signal cancels the run; cleanup still posts comments.
	loopCtx, stopLoop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopLoop()
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	var exiting atomic.Bool
	defer exiting.Store(true)
	go func() {
		<-loopCtx.Done()
		if exiting.Load() {
			return // normal exit, not a signal
		}
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
	if status != nil {
		stop := showLastPoll(d, status)
		defer stop()
	}
	if err := d.Run(loopCtx, runCtx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(logOut, err)
		return 1
	}
	return 0
}

// showLastPoll keeps the status line reading "last poll <time> (<ago>)",
// refreshed every second, until the returned stop function is called.
func showLastPoll(d *dispatch.Dispatcher, line *statusline.Line) (stop func()) {
	var mu sync.Mutex
	var last time.Time
	redraw := func() {
		mu.Lock()
		defer mu.Unlock()
		line.Set(pollStatus(last, time.Now()))
	}
	d.Polled = func(at time.Time) {
		mu.Lock()
		last = at
		mu.Unlock()
		redraw()
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		redraw()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				redraw()
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		line.Clear()
	}
}

// pollStatus is the status line text: when the tracker was last polled and
// how long ago that was, so a quiet worker still visibly has a pulse.
func pollStatus(last, now time.Time) string {
	if last.IsZero() {
		return "waiting for first poll"
	}
	return fmt.Sprintf("last poll %s (%s ago)", last.Format("2006-01-02 15:04:05"), now.Sub(last).Truncate(time.Second))
}

// isTerminal reports whether w is a character device, i.e. an interactive
// terminal rather than a file or pipe.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// runOnce handles a single ticket by key, bypassing the trigger query and
// the run windows. It is the operator's "do this one now".
func runOnce(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: hivedispatch once KEY [flags]")
		return 2
	}
	key := args[0]
	fs := flag.NewFlagSet("once", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	executorFlag := fs.String("executor", "", "override config executor: claude or fake")
	triageFlag := fs.String("triage", "", "override config triage: claude or passthrough")
	skipPreflight := fs.Bool("skip-preflight", false, "start without verifying Jira fields, statuses, and projects")
	placeholder := fs.Bool("placeholder", false, "fake executor writes a placeholder file so the branch/PR path is exercised")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	project, _, _ := strings.Cut(key, "-")
	known := false
	for _, r := range cfg.Repos {
		if strings.EqualFold(r.Project, project) {
			known = true
		}
	}
	if !known {
		fmt.Fprintf(stderr, "%s: no repo has jira_project %q configured\n", key, project)
		return 1
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	ctx := context.Background()
	w, err := newWorker(ctx, cfg, wireOptions{executor: *executorFlag, triage: *triageFlag, placeholder: *placeholder, preflight: !*skipPreflight}, logger, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ticket, err := w.tracker.Get(ctx, key)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	out, err := w.d.Handle(runCtx, ticket)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %s: %v\n", key, out, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: %s\n", key, out)
	return 0
}
