package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/dispatch"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/executor/claudecode"
	"github.com/thomasmeadows/hivedispatch/internal/executor/codex"
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
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	"github.com/thomasmeadows/hivedispatch/internal/tracker/ghissues"
	"github.com/thomasmeadows/hivedispatch/internal/tracker/jira"
	trackerrouter "github.com/thomasmeadows/hivedispatch/internal/tracker/router"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
	triclaude "github.com/thomasmeadows/hivedispatch/internal/triage/claudecode"
	"github.com/thomasmeadows/hivedispatch/internal/triage/passthrough"
)

// wireOptions are the command-line overrides that shape a worker.
type wireOptions struct {
	executor    string // "" = config
	triage      string // "" = config
	placeholder bool
	preflight   bool
}

// worker is everything a command needs to act on tickets.
type worker struct {
	cfg     *config.Config
	tracker tracker.Tracker
	ws      *gitws.Workspaces
	store   state.RunStore
	host    githost.GitHost
	d       *dispatch.Dispatcher
}

// repoTracker builds repo's tracker client. With preflight it also runs the
// live check and reports ok=false when something would prevent a run.
func repoTracker(ctx context.Context, repo config.RepoConfig, preflight bool, stdout, stderr io.Writer) (tracker.Tracker, bool, error) {
	if preflight {
		fmt.Fprintf(stdout, "%s — %s (%s)\n", repo.Project, repo.Name, repo.Tracker)
	}
	switch repo.Tracker {
	case "github":
		gh, err := ghissues.New(repo.GitHub, []config.RepoConfig{repo})
		if err != nil {
			return nil, false, err
		}
		if preflight {
			return gh, githubPreflight(ctx, gh, stdout, stderr), nil
		}
		return gh, true, nil
	case "jira":
		jc, err := jira.New(repo.Jira)
		if err != nil {
			return nil, false, err
		}
		if err := jc.ResolveFields(ctx); err != nil {
			return nil, false, fmt.Errorf("%s: %w", repo.Project, err)
		}
		if preflight {
			return jc, jiraPreflight(ctx, jc, repo, stdout, stderr), nil
		}
		return jc, true, nil
	default:
		return nil, false, fmt.Errorf("%s: unknown tracker %q", repo.Project, repo.Tracker)
	}
}

// openStores prepares the base clones and opens one run store per repo.
// It needs git but not Jira.
func openStores(ctx context.Context, cfg *config.Config) (*gitws.Workspaces, state.RunStore, error) {
	ws := gitws.New(filepath.Join(cfg.Workroot, "repos"))
	stores := map[string]state.RunStore{}
	for _, repo := range cfg.Repos {
		if cfg.StateStore == "local" {
			stores[strings.ToUpper(repo.Project)] = localdir.New(filepath.Join(cfg.Workroot, "state", strings.ReplaceAll(repo.Name, "/", "__")))
			continue
		}
		base, err := ws.EnsureBase(ctx, repo)
		if err != nil {
			return nil, nil, err
		}
		st, err := gitbranch.Open(ctx, base, filepath.Join(ws.RepoDir(repo), ".state"))
		if err != nil {
			return nil, nil, fmt.Errorf("state branch: %w", err)
		}
		stores[strings.ToUpper(repo.Project)] = st
	}
	return ws, &router.Store{Stores: stores}, nil
}

// newWorker discovers a GitHub token, builds one tracker per repository
// (verifying each when opts.preflight), opens stores, and builds the
// dispatcher.
func newWorker(ctx context.Context, cfg *config.Config, opts wireOptions, logger *slog.Logger, stdout, stderr io.Writer) (*worker, error) {
	// The GitHub token serves the PR API and, for repositories on GitHub
	// Issues, the issues API — so it is resolved before the trackers.
	var host githost.GitHost = none.Host{}
	tok, src := github.DiscoverToken(ctx, "github.com")
	switch {
	case src != "":
		cfg.SetGitHubToken(tok)
		var err error
		host, err = github.New(cfg.GitHub)
		if err != nil {
			return nil, err
		}
		logger.Info("github token", "source", src)
	case cfg.UsesTracker("github"):
		return nil, fmt.Errorf("a repository uses GitHub Issues, which needs a GitHub token — export HIVE_GITHUB_TOKEN or run `gh auth login`")
	default:
		logger.Warn("no GitHub token found; branches will be pushed but pull requests will not be opened")
	}

	trackers := map[string]tracker.Tracker{}
	for _, repo := range cfg.Repos {
		tr, ok, err := repoTracker(ctx, repo, opts.preflight, stdout, stderr)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("not starting: fix the above, or pass -skip-preflight")
		}
		trackers[strings.ToUpper(repo.Project)] = tr
	}
	tr := &trackerrouter.Router{ByProject: trackers, Log: logger}
	if opts.preflight && cfg.GitHub.Token != "" && !prPreflight(ctx, cfg, stdout, stderr) {
		return nil, fmt.Errorf("not starting: fix the above, or pass -skip-preflight")
	}
	sched, err := schedule.Parse(cfg.RunWindows)
	if err != nil {
		return nil, err
	}
	ws, store, err := openStores(ctx, cfg)
	if err != nil {
		return nil, err
	}

	name := cfg.Executor
	if opts.executor != "" {
		name = opts.executor
	}
	var ex executor.Executor
	switch name {
	case "fake":
		f := exfake.New()
		f.Placeholder = opts.placeholder
		ex = f
	case "claude":
		ex = claudecode.New(claudecode.Config{Binary: cfg.Claude.Binary, Model: cfg.Claude.Model})
	case "codex":
		ex = codex.New(codex.Config{Binary: cfg.Codex.Binary, Model: cfg.Codex.Model})
	default:
		return nil, fmt.Errorf("unknown executor %q", name)
	}
	triKind := cfg.Triage.Kind
	if opts.triage != "" {
		triKind = opts.triage
	}
	var tri triage.Triager
	switch triKind {
	case "passthrough":
		tri = passthrough.Triager{}
	case "claude":
		tri = triclaude.New(triclaude.Config{Binary: cfg.Claude.Binary, Model: cfg.Triage.Model, StepBudget: cfg.Triage.StepBudget, Timeout: cfg.Triage.Timeout})
	default:
		return nil, fmt.Errorf("unknown triage %q", triKind)
	}
	d := &dispatch.Dispatcher{
		Cfg:        dispatch.ConfigFrom(cfg),
		Tracker:    tr,
		Triager:    tri,
		Executor:   ex,
		Workspaces: ws,
		Host:       host,
		Store:      store,
		Schedule:   sched,
		Log:        logger,
	}
	return &worker{cfg: cfg, tracker: tr, ws: ws, store: store, host: host, d: d}, nil
}
