// Package config loads and validates the HiveDispatch configuration.
//
// Configuration comes in two layers. The worker config (by default
// ~/.config/hivedispatch/config.yaml) holds what belongs to this machine:
// the agent id, workroot, schedule, executor, credentials' account names,
// and where to find repositories — explicit paths and code_dirs to scan.
// Each governed repository carries its own settings in
// .hive-dispatch/repo.yaml: its ticket-key prefix, which tracker holds its
// queue (Jira or GitHub Issues) and that tracker's settings. The worker
// reads repo.yaml from the local checkout, so it can start before cloning
// anything. Secrets are never read from YAML; they come from the
// environment.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Config is the worker-level configuration plus the repositories resolved
// from it.
type Config struct {
	MachineID         string        `yaml:"machine_id"` // default: the hostname
	Workroot          string        `yaml:"workroot"`
	CodeDirs          []string      `yaml:"code_dirs"`      // scanned for repositories with .hive-dispatch/repo.yaml
	ScanDepth         int           `yaml:"scan_depth"`     // directory levels below each code dir; default 4
	MaxConcurrent     int           `yaml:"max_concurrent"` // tickets worked at once, each in its own worktree; default 1
	RepoPaths         []RepoRef     `yaml:"repos"`          // explicit repositories, in addition to code_dirs
	PollInterval      time.Duration `yaml:"poll_interval"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	ClaimTimeout      time.Duration `yaml:"claim_timeout"`
	RunTimeout        time.Duration `yaml:"run_timeout"`
	StepBudget        int           `yaml:"step_budget"`
	MaxAttempts       int           `yaml:"max_attempts"`
	PollJitter        time.Duration `yaml:"poll_jitter"`
	RunWindows        RunWindows    `yaml:"run_windows"`
	StateStore        string        `yaml:"state_store"`    // "branch" (default) or "local"
	RetentionDays     int           `yaml:"retention_days"` // prune raw logs and finished runs older than this; 0 disables
	Claude            ClaudeConfig  `yaml:"claude"`
	Codex             CodexConfig   `yaml:"codex"`
	Triage            TriageConfig  `yaml:"triage"`
	// Jira and GitHub hold the worker's accounts: jira.email and
	// github.api_url in YAML, tokens from the environment. Tracker settings
	// live in each repository's repo.yaml.
	Jira   JiraConfig   `yaml:"jira"`
	GitHub GitHubConfig `yaml:"github"`

	// Repos are the enrolled repositories, resolved by Load: explicit
	// RepoPaths first, then those found under CodeDirs.
	Repos []RepoConfig `yaml:"-"`
	// Shadowed maps a repository path that was skipped to the path used
	// instead, when both are checkouts of the same repository.
	Shadowed map[string]string `yaml:"-"`

	problems []string // found while resolving Repos; reported by Validate
}

// RepoRef is one explicit repository in the worker config.
type RepoRef struct {
	Path string `yaml:"path"`
}

// JiraConfig describes the Jira Cloud site and the fields the claim protocol uses.
type JiraConfig struct {
	BaseURL  string       `yaml:"base_url"`
	Email    string       `yaml:"email"`
	Token    string       `yaml:"-"` // from HIVE_JIRA_TOKEN
	JQL      string       `yaml:"jql"`
	Fields   JiraFields   `yaml:"fields"`
	Statuses JiraStatuses `yaml:"statuses"`
}

// JiraFields holds the custom field IDs (customfield_NNNNN) used for claims.
// Both are optional: empty values are resolved by field name at startup
// ("HiveDispatch Agent" and "HiveDispatch Claimed At", as created by
// `hivedispatch init -jira`).
type JiraFields struct {
	AgentID   string `yaml:"agent_id"`
	ClaimedAt string `yaml:"claimed_at"`
}

// JiraStatuses maps HiveDispatch states to Jira workflow status names.
type JiraStatuses struct {
	Ready      string `yaml:"ready"`
	InProgress string `yaml:"in_progress"`
	NeedsInfo  string `yaml:"needs_info"`
	InReview   string `yaml:"in_review"`
	NeedsHuman string `yaml:"needs_human"`
}

// ClaudeConfig configures the Claude Code executor at the worker level;
// per-repo settings live in .hive-dispatch/policy.yaml inside the governed repo.
type ClaudeConfig struct {
	Binary string `yaml:"binary"` // default "claude"
}

// CodexConfig configures the Codex executor at the worker level; per-repo
// settings live under executor.codex in .hivedispatch.yaml.
type CodexConfig struct {
	Binary string `yaml:"binary"` // default "codex"
}

// TriageConfig selects and bounds the triage step.
type TriageConfig struct {
	Kind       string        `yaml:"kind"`        // "claude" (default) or "passthrough"
	StepBudget int           `yaml:"step_budget"` // default 40
	Timeout    time.Duration `yaml:"timeout"`     // default 5m
	Model      string        `yaml:"model"`
}

// GitHubConfig covers the pull-request API and, with tracker: github, the
// issues API; git itself uses the user's own credentials.
type GitHubConfig struct {
	APIURL  string        `yaml:"api_url"` // default https://api.github.com
	Token   string        `yaml:"-"`       // HIVE_GITHUB_TOKEN, or discovered from gh / git credentials at startup
	Labels  GitHubLabels  `yaml:"labels"`  // state labels when tracker is github
	Project GitHubProject `yaml:"project"` // optional Projects (v2) board that mirrors the state labels
}

// GitHubLabels are the issue labels that carry HiveDispatch state when the
// tracker is GitHub Issues. Exactly one is on an issue at a time.
type GitHubLabels struct {
	Ready      string `yaml:"ready"`
	InProgress string `yaml:"in_progress"`
	NeedsInfo  string `yaml:"needs_info"`
	InReview   string `yaml:"in_review"`
	NeedsHuman string `yaml:"needs_human"`
}

// GitHubProject names a GitHub Projects (v2) board. When set, every state
// change also moves the issue's card: the issue is added to the project if
// it is not on it yet, and the single-select Field is set to the column
// configured for the new state. Labels stay the source of truth; the board
// is a mirror of them.
type GitHubProject struct {
	Owner   string               `yaml:"owner"`   // user or organisation login: OWNER in github.com/users/OWNER/projects/N
	Number  int                  `yaml:"number"`  // N in that URL
	Field   string               `yaml:"field"`   // single-select field to set; default "Status"
	Columns GitHubProjectColumns `yaml:"columns"` // option names of that field, one per state
}

// GitHubProjectColumns are the Status options (board columns) per state.
type GitHubProjectColumns struct {
	Ready      string `yaml:"ready"`
	InProgress string `yaml:"in_progress"`
	NeedsInfo  string `yaml:"needs_info"`
	InReview   string `yaml:"in_review"`
	NeedsHuman string `yaml:"needs_human"`
}

// Enabled reports whether a project board is configured at all.
func (p GitHubProject) Enabled() bool { return p.Owner != "" || p.Number != 0 }

// RepoConfig is one enrolled repository: its .hive-dispatch/repo.yaml,
// with origin facts filled in and the worker's accounts merged into Jira
// and GitHub.
type RepoConfig struct {
	Path          string       // local checkout the settings were read from
	Name          string       // owner/repo; default parsed from the origin URL
	URL           string       // clone URL; default the origin remote
	DefaultBranch string       // default origin/HEAD, else "main"
	Project       string       // ticket_prefix, upper-case; default the tracker's name. Keys are PREFIX-BOARD-N
	Tracker       string       // ticket_tracker: "jira" or "github"
	Agents        []Agent      // .hive-dispatch/agents.yaml; DefaultAgents without one
	Jira          JiraConfig   // with tracker: jira
	GitHub        GitHubConfig // PR API always; issues and labels with tracker: github
}

// RunWindows restricts when the poller claims new work. Empty means always.
type RunWindows struct {
	Timezone string         `yaml:"timezone"` // IANA name; default local
	Windows  []WindowConfig `yaml:"windows"`
}

// WindowConfig is one daily window. Start after End spans midnight.
type WindowConfig struct {
	Days  []string `yaml:"days"`  // mon..sun or full names; empty = every day
	Start string   `yaml:"start"` // HH:MM
	End   string   `yaml:"end"`   // HH:MM, exclusive
}

// projectKeyRe matches Jira project keys: letters first, then letters,
// digits or underscores.
var projectKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func def(p *string, v string) {
	if *p == "" {
		*p = v
	}
}

func (c *Config) applyDefaults() {
	if c.PollInterval == 0 {
		c.PollInterval = 60 * time.Second
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = 60 * time.Second
	}
	if c.ClaimTimeout == 0 {
		c.ClaimTimeout = 2 * time.Hour
	}
	if c.RunTimeout == 0 {
		c.RunTimeout = 45 * time.Minute
	}
	if c.StepBudget == 0 {
		c.StepBudget = 200
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 3
	}
	if c.PollJitter == 0 {
		c.PollJitter = 10 * time.Second
	}
	if c.ScanDepth == 0 {
		c.ScanDepth = 4
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 1
	}
	def(&c.GitHub.APIURL, "https://api.github.com")
	def(&c.StateStore, "branch")
	if c.RetentionDays == 0 {
		c.RetentionDays = 30
	}
	def(&c.Claude.Binary, "claude")
	def(&c.Codex.Binary, "codex")
	def(&c.Triage.Kind, "claude")
	if c.Triage.StepBudget == 0 {
		c.Triage.StepBudget = 40
	}
	if c.Triage.Timeout == 0 {
		c.Triage.Timeout = 5 * time.Minute
	}
}

// applyDefaults fills a repository's tracker defaults.
func (r *RepoConfig) applyDefaults() {
	def(&r.DefaultBranch, "main")
	s := &r.Jira.Statuses
	def(&s.Ready, "Ready")
	def(&s.InProgress, "In Progress")
	def(&s.NeedsInfo, "Needs Info")
	def(&s.InReview, "In Review")
	def(&s.NeedsHuman, "Needs Human")
	l := &r.GitHub.Labels
	def(&l.Ready, "hive:ready")
	def(&l.InProgress, "hive:in-progress")
	def(&l.NeedsInfo, "hive:needs-info")
	def(&l.InReview, "hive:in-review")
	def(&l.NeedsHuman, "hive:needs-human")
	p := &r.GitHub.Project
	def(&p.Field, "Status")
	def(&p.Columns.Ready, "Ready")
	def(&p.Columns.InProgress, "In Progress")
	def(&p.Columns.NeedsInfo, "Needs Info")
	def(&p.Columns.InReview, "In Review")
	def(&p.Columns.NeedsHuman, "Needs Human")
}

// SetGitHubToken records a discovered GitHub token on the worker and on
// every repository.
func (c *Config) SetGitHubToken(tok string) {
	c.GitHub.Token = tok
	for i := range c.Repos {
		c.Repos[i].GitHub.Token = tok
	}
}

// RepoByProject returns the repository whose ticket-key prefix is project
// (case-insensitive), or nil.
func (c *Config) RepoByProject(project string) *RepoConfig {
	for i := range c.Repos {
		if strings.EqualFold(c.Repos[i].Project, project) {
			return &c.Repos[i]
		}
	}
	return nil
}

// UsesTracker reports whether any repository uses tracker kind.
func (c *Config) UsesTracker(kind string) bool {
	for _, r := range c.Repos {
		if r.Tracker == kind {
			return true
		}
	}
	return false
}

// Validate returns an error listing every missing or inconsistent field,
// each with a hint about where the value comes from: the worker's own
// settings, every repository's repo.yaml (prefixed with its path), and the
// rules that span repositories.
func (c *Config) Validate() error {
	problems := c.workerProblems()
	problems = append(problems, c.problems...)
	if len(c.Repos) == 0 && len(c.problems) == 0 {
		problems = append(problems, "no enrolled repositories — run `hivedispatch init -github` (or -jira) inside a repository, then list it under repos: or keep it under code_dirs; `hivedispatch scan` shows what is found")
	}
	seen := map[string]string{}
	for _, r := range c.Repos {
		problems = append(problems, c.repoProblems(r)...)
		key := strings.ToUpper(r.Project)
		if other, dup := seen[key]; dup && key != "" {
			problems = append(problems, fmt.Sprintf("%s: ticket_prefix %q is also used by %s — ticket keys must map to one repository; set a different ticket_prefix in one of them", repoFilePath(r.Path), r.Project, other))
		}
		seen[key] = r.Path
	}
	problems = append(problems, c.accountProblems()...)
	return problemsError(problems)
}

func problemsError(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return errors.New("invalid config:\n  - " + strings.Join(problems, "\n  - ") + "\n\nSee docs/setup.md for the full walkthrough.")
}

// accountProblems checks the worker-level credentials that the enrolled
// repositories' trackers need.
func (c *Config) accountProblems() []string {
	if !c.UsesTracker("jira") {
		return nil
	}
	var problems []string
	if strings.TrimSpace(c.Jira.Email) == "" {
		problems = append(problems, "jira.email is required in the worker config (a repository uses ticket_tracker: jira) — the Atlassian account email the API token belongs to")
	} else if ph := placeholderIn(c.Jira.Email); ph != "" {
		problems = append(problems, "jira.email still has the starter placeholder "+ph+" — replace it with your real value")
	}
	if c.Jira.Token == "" {
		problems = append(problems, "HIVE_JIRA_TOKEN environment variable is required (a repository uses ticket_tracker: jira) — create an API token at https://id.atlassian.com/manage-profile/security/api-tokens and `export HIVE_JIRA_TOKEN=...`")
	}
	return problems
}

// placeholderIn returns the starter placeholder v still contains, or "".
func placeholderIn(v string) string {
	for _, ph := range starterPlaceholders {
		if strings.Contains(v, ph) {
			return ph
		}
	}
	return ""
}

// repoProblems checks one repository's settings; every problem names its
// repo.yaml.
func (c *Config) repoProblems(r RepoConfig) []string {
	var problems []string
	file := repoFilePath(r.Path)
	add := func(msg string) { problems = append(problems, file+": "+msg) }
	need := func(v, name, hint string) {
		if strings.TrimSpace(v) == "" {
			add(name + " is required — " + hint)
		}
	}
	placeholder := func(v, name string) {
		if ph := placeholderIn(v); ph != "" {
			add(name + " still has the starter placeholder " + ph + " — replace it with your real value")
		}
	}
	need(r.Name, "name", "owner/repo as shown on GitHub; normally parsed from the origin remote")
	need(r.URL, "url", "the repository has no origin remote — add one (git remote add origin ...) or set url to the clone URL your git credentials can push to")
	if r.Project != "" && !projectKeyRe.MatchString(r.Project) {
		add(fmt.Sprintf("ticket_prefix %q: use letters, digits or underscores, starting with a letter (no dashes) — e.g. GITHUB, JIRA or WEB", r.Project))
	}
	switch r.Tracker {
	case "jira":
		need(r.Jira.BaseURL, "jira.base_url", "your Jira Cloud site, e.g. https://yourteam.atlassian.net")
		need(r.Jira.JQL, "jira.jql", `the query that selects work, e.g. project = KEY AND status = "Ready" AND labels = hive`)
		placeholder(r.Jira.BaseURL, "jira.base_url")
		placeholder(r.Jira.JQL, "jira.jql")
		for _, f := range []struct{ v, name string }{{r.Jira.Fields.AgentID, "jira.fields.agent_id"}, {r.Jira.Fields.ClaimedAt, "jira.fields.claimed_at"}} {
			if f.v != "" && !strings.HasPrefix(f.v, "customfield_") {
				add(f.name + " must be a Jira custom field id like customfield_10042 (or leave it empty to look the field up by name)")
			}
		}
	case "github":
		if p := r.GitHub.Project; p.Enabled() {
			need(p.Owner, "github.project.owner", "the user or organisation that owns the board: OWNER in github.com/users/OWNER/projects/N")
			if p.Number <= 0 {
				add("github.project.number is required — N in github.com/users/OWNER/projects/N")
			}
		}
	case "":
		add("ticket_tracker is required — jira or github")
	default:
		add(fmt.Sprintf("ticket_tracker: want jira or github, got %q", r.Tracker))
	}
	return problems
}

// workerProblems checks the worker's own settings.
func (c *Config) workerProblems() []string {
	var problems []string
	if strings.TrimSpace(c.MachineID) == "" {
		problems = append(problems, "machine_id is required: the hostname is unavailable, so set any short name for this machine, e.g. laptop-1")
	}
	if c.Triage.Kind != "" && c.Triage.Kind != "claude" && c.Triage.Kind != "passthrough" {
		problems = append(problems, fmt.Sprintf("triage.kind: want claude or passthrough, got %q", c.Triage.Kind))
	}
	if c.RetentionDays < -1 {
		problems = append(problems, "retention_days must be -1 (never prune), or a number of days")
	}
	if c.ScanDepth < 0 {
		problems = append(problems, "scan_depth must be 0 (only the code dirs themselves) or more")
	}
	if c.MaxConcurrent < 0 {
		problems = append(problems, "max_concurrent must be 1 or more — how many tickets this worker works at once")
	}
	if c.StateStore != "" && c.StateStore != "branch" && c.StateStore != "local" {
		problems = append(problems, fmt.Sprintf("state_store: want branch or local, got %q", c.StateStore))
	}
	if c.ClaimTimeout > 0 && c.HeartbeatInterval > 0 && c.ClaimTimeout <= c.HeartbeatInterval {
		problems = append(problems, "claim_timeout must be longer than heartbeat_interval")
	}
	if tz := c.RunWindows.Timezone; tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			problems = append(problems, "run_windows.timezone: unknown timezone "+tz)
		}
	}
	for i, w := range c.RunWindows.Windows {
		for _, pair := range [][2]string{{"start", w.Start}, {"end", w.End}} {
			if _, err := time.Parse("15:04", pair[1]); err != nil {
				problems = append(problems, fmt.Sprintf("run_windows.windows[%d].%s: want HH:MM, got %q", i, pair[0], pair[1]))
			}
		}
	}
	return problems
}
