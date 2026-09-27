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
	"sync"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/discover"
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
  check [-config P] [-live]   validate the worker config and every enrolled repository;
                              -live verifies against each repository's tracker and GitHub
  init  [-config P]           write a commented starter worker config (never overwrites)
  init  -github [-config P] [DIR]
  init  -jira   [-config P] [DIR]
                              enrol the repository at DIR (default: the one you are in): first
                              write .hive-dispatch/repo.yaml and policy.yaml to fill in; once
                              filled in, create its hive:* labels (GitHub) or claim fields (Jira)
  scan  [-config P] [DIR...]  list git repositories under DIR (default: code_dirs, else ~) and
                              which are enrolled
  run   [-config P] [-once] [-executor claude|codex|fake] [-triage claude|passthrough]
        [-placeholder] [-skip-preflight]
                              verify each repository's tracker, then poll and dispatch; -executor and -triage override the config
                              (-placeholder makes the fake executor write a file so
                              the branch/PR path is exercised)
  once  KEY [same flags as run]
                              handle one ticket by key, ignoring the trigger query and run windows
  status [-config P] [-json]  list run records from the state branch(es)
  supervisor [-config P] [-provider anthropic|openai|deepseek|huggingface|ollama] [-model M] [-resume | -session FILE]
                              chat with the built-in assistant that helps configure and run HiveDispatch;
                              piped stdin asks one question and exits
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
	case "scan":
		return runScan(args[1:], stdout, stderr)
	case "run":
		return runRun(args[1:], stdout, stderr)
	case "once":
		return runOnce(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "supervisor":
		return runSupervisor(args[1:], os.Stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	live := fs.Bool("live", false, "also verify against each repository's tracker and GitHub")
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
	fmt.Fprintf(stdout, "config ok: agent %s, %d repo(s)\n", cfg.AgentID, len(cfg.Repos))
	printRepos(stdout, cfg)
	tok, src := github.DiscoverToken(ctx, "github.com")
	switch {
	case src != "":
		fmt.Fprintf(stdout, "github ok: token from %s\n", src)
		cfg.SetGitHubToken(tok)
	case cfg.UsesTracker("github"):
		fmt.Fprintln(stderr, "github: no token found (HIVE_GITHUB_TOKEN, gh auth token, or git credential helper) — required because a repository uses GitHub Issues; run `gh auth login`")
		return 1
	default:
		fmt.Fprintln(stdout, "github: no token found (HIVE_GITHUB_TOKEN, gh auth token, or git credential helper) — branches will be pushed but PRs will not be opened")
	}
	if !*live && !*liveJira {
		return 0
	}
	ok := true
	for _, repo := range cfg.Repos {
		_, repoOK, err := repoTracker(ctx, repo, true, stdout, stderr)
		if err != nil {
			fmt.Fprintln(stderr, err)
			repoOK = false
		}
		ok = ok && repoOK
	}
	if cfg.GitHub.Token != "" && !prPreflight(ctx, cfg, stdout, stderr) {
		ok = false
	}
	if !ok {
		return 1
	}
	return 0
}

// printRepos lists the enrolled repositories and any second checkout that
// was skipped in favour of another.
func printRepos(w io.Writer, cfg *config.Config) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range cfg.Repos {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", r.Project, r.Tracker, r.Name, r.Path)
	}
	_ = tw.Flush() // best-effort listing; the writer is the terminal
	for skipped, kept := range cfg.Shadowed {
		fmt.Fprintf(w, "  skipped %s: another checkout of the same repository as %s\n", skipped, kept)
	}
}

// prPreflight verifies the token can open pull requests on every repo.
func prPreflight(ctx context.Context, cfg *config.Config, stdout, stderr io.Writer) bool {
	host, err := github.New(cfg.GitHub)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return false
	}
	ok := true
	for _, repo := range cfg.Repos {
		if err := host.ProbePRWrite(ctx, repo.Name, repo.DefaultBranch); err != nil {
			fmt.Fprintln(stderr, err)
			ok = false
			continue
		}
		fmt.Fprintf(stdout, "token can open pull requests on %s\n", repo.Name)
	}
	return ok
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
	case rep.ProjectError != "":
		fmt.Fprintf(stderr, "project board: %s\n  Check github.project.owner and .number against the board's URL, and that the token has the project scope.\n", rep.ProjectError)
	case len(rep.MissingColumns) > 0:
		fmt.Fprintf(stderr, "project board %q has no %s option(s) %s — add the column(s) on the board or change github.project.columns\n", rep.Project, rep.ProjectField, strings.Join(rep.MissingColumns, ", "))
	case rep.Project != "":
		fmt.Fprintf(stdout, "project board %q ok: every state has a column\n", rep.Project)
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
func jiraPreflight(ctx context.Context, client *jira.Client, repo config.RepoConfig, stdout, stderr io.Writer) bool {
	rep, err := client.Check(ctx, []string{repo.Project})
	if err != nil {
		fmt.Fprintln(stderr, "jira check failed:", err)
		return false
	}
	fmt.Fprintf(stdout, "jira ok: authenticated as %s; trigger JQL matches %d ticket(s)\n", rep.User, rep.SampleTickets)
	for _, p := range rep.UnknownProjects {
		fmt.Fprintf(stderr, "%s: project %q does not exist on this site; projects here: %s\n", repo.Path, p, strings.Join(rep.Projects, ", "))
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
	doJira := fs.Bool("jira", false, "enrol a repository whose queue is in Jira, and create the claim custom fields")
	doGitHub := fs.Bool("github", false, "enrol a repository whose queue is its GitHub Issues, and create the state labels")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	written, err := config.WriteStarter(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !*doJira && !*doGitHub {
		if written {
			fmt.Fprintf(stdout, "wrote starter config to %s\n\nSet agent_id and code_dirs (every field says where its value comes from), then in each repository run:\n  hivedispatch init -github    (queue in GitHub Issues)\n  hivedispatch init -jira      (queue in Jira)\n", *cfgPath)
		} else {
			fmt.Fprintf(stdout, "config already exists at %s\nNext: `hivedispatch init -github` or `hivedispatch init -jira` inside a repository, then `hivedispatch check -live`\n", *cfgPath)
		}
		return 0
	}
	if *doJira && *doGitHub {
		fmt.Fprintln(stderr, "init: choose one of -github or -jira")
		return 2
	}
	if written {
		fmt.Fprintf(stdout, "wrote starter config to %s\n", *cfgPath)
	}
	kind := "github"
	if *doJira {
		kind = "jira"
	}
	return initRepo(fs.Arg(0), kind, *cfgPath, stdout, stderr)
}

// initRepo enrols the repository at dir (default: the one containing the
// working directory) for tracker kind: it writes the .hive-dispatch starters
// the first time, and once they are filled in creates the tracker's labels
// or claim fields.
func initRepo(dir, kind, cfgPath string, stdout, stderr io.Writer) int {
	ctx := context.Background()
	cfg, err := config.LoadWorker(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n\nEdit %s and run `hivedispatch init -%s` again.\n", err, cfgPath, kind)
		return 1
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if dir, err = discover.GitRoot(ctx, wd); err != nil {
			fmt.Fprintf(stderr, "%v — run init -%s inside a repository, or pass its path\n", err, kind)
			return 1
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	file := filepath.Join(dir, config.RepoDir, config.RepoFileName)
	written, err := config.WriteRepoStarter(dir, kind)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if written {
		what := "project"
		if kind == "jira" {
			what = "project, jira.base_url and jira.jql (and jira.email in " + cfgPath + ")"
		}
		fmt.Fprintf(stdout, "wrote %s and policy.yaml beside it.\nFill in %s, commit both, then run `hivedispatch init -%s %s` again.\n", file, what, kind, dir)
		enrolHint(stdout, cfg, dir, cfgPath)
		return 0
	}
	repo, err := cfg.LoadRepo(dir)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n\nEdit %s and run `hivedispatch init -%s` again.\n", err, file, kind)
		return 1
	}
	if repo.Tracker != kind {
		fmt.Fprintf(stderr, "%s says tracker: %s — run `hivedispatch init -%s`, or change the tracker there\n", file, repo.Tracker, repo.Tracker)
		return 1
	}
	if kind == "github" {
		tok, src := github.DiscoverToken(ctx, "github.com")
		if src == "" {
			fmt.Fprintln(stderr, "no GitHub token found: export HIVE_GITHUB_TOKEN (Issues read/write) or run `gh auth login`")
			return 1
		}
		repo.GitHub.Token = tok
		client, err := ghissues.New(repo.GitHub, []config.RepoConfig{repo})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		n, err := client.EnsureLabels(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "init failed:", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: github labels ready (%d created). Put %q on an issue to queue it; it becomes %s-<number>.\n", repo.Name, n, repo.GitHub.Labels.Ready, repo.Project)
	} else {
		client, err := jira.New(repo.Jira)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ids, err := client.EnsureFields(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "init failed:", err)
			return 1
		}
		fmt.Fprintf(stdout, "jira claim fields ready on %s:\n  %s = %s  (which worker holds a ticket)\n  %s = %s  (that worker's last heartbeat)\n\nThe worker finds them by name, so nothing needs pasting into %s.\n", repo.Jira.BaseURL, jira.FieldNameAgent, ids.AgentID, jira.FieldNameClaimedAt, ids.ClaimedAt, file)
	}
	enrolHint(stdout, cfg, dir, cfgPath)
	fmt.Fprintln(stdout, "Next: hivedispatch check -live")
	return 0
}

// enrolHint tells the operator how to make the worker pick up dir when it
// is neither listed under repos: nor under a code dir.
func enrolHint(w io.Writer, cfg *config.Config, dir, cfgPath string) {
	if cfg.Enrolled(dir) {
		return
	}
	fmt.Fprintf(w, "\nThe worker will not pick up %s yet: it is not under code_dirs and not listed under repos:.\nAdd to %s either\n  code_dirs: [%s]\nor\n  repos:\n    - path: %s\n", dir, cfgPath, filepath.Dir(dir), dir)
}

// runScan lists the git repositories under the given directories (default:
// code_dirs, else the home directory) and which of them are enrolled.
func runScan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.LoadWorker(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n(run `hivedispatch init` to write a starter config)\n", err)
		return 1
	}
	roots, home, err := cfg.ScanRoots(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if home {
		fmt.Fprintf(stdout, "code_dirs is not set; scanning %s. Set code_dirs in %s to scan your code folder instead.\n", roots[0], *cfgPath)
	}
	rows, err := cfg.ScanRows(roots)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no git repositories found")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tSTATUS\tPROJECT\tTRACKER\tREPO")
	enrolled := 0
	for _, r := range rows {
		status := "-"
		switch {
		case r.Enrolled:
			enrolled++
			status = "enrolled"
			if r.Problem != "" {
				status = "enrolled, invalid (see check)"
			}
			if !r.PickedUp {
				status += ", outside code_dirs"
			}
		case r.Legacy:
			status = "legacy .hivedispatch.yaml"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Path, status, r.Project, r.Tracker, r.Name)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "\n%d repositories, %d enrolled. Enrol one with `hivedispatch init -github DIR` (or -jira).\n", len(rows), enrolled)
	return 0
}

func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	once := fs.Bool("once", false, "poll once and exit")
	executorFlag := fs.String("executor", "", "override config executor: claude, codex or fake")
	triageFlag := fs.String("triage", "", "override config triage: claude or passthrough")
	skipPreflight := fs.Bool("skip-preflight", false, "start without verifying each repository's tracker setup")
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
	logger.Info("starting", "agent", cfg.AgentID, "executor", d.Executor.Name(), "max_concurrent", cfg.MaxConcurrent, "once", *once)

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
	executorFlag := fs.String("executor", "", "override config executor: claude, codex or fake")
	triageFlag := fs.String("triage", "", "override config triage: claude or passthrough")
	skipPreflight := fs.Bool("skip-preflight", false, "start without verifying each repository's tracker setup")
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
	if cfg.RepoByProject(project) == nil {
		fmt.Fprintf(stderr, "%s: no enrolled repository has project %q in its .hive-dispatch/repo.yaml\n", key, project)
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
