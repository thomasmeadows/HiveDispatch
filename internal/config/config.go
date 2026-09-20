// Package config loads and validates the HiveDispatch worker configuration.
//
// The worker config lives outside any governed repository (by default
// ~/.config/hivedispatch/config.yaml) because the worker needs it before it
// can clone anything. Secrets are never read from YAML; they come from the
// environment.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Config is the worker-level configuration.
type Config struct {
	Tracker           string        `yaml:"tracker"` // "jira" (default) or "github"
	AgentID           string        `yaml:"agent_id"`
	Workroot          string        `yaml:"workroot"`
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
	Executor          string        `yaml:"executor"`       // "claude" (default), "codex" or "fake"
	Claude            ClaudeConfig  `yaml:"claude"`
	Codex             CodexConfig   `yaml:"codex"`
	Triage            TriageConfig  `yaml:"triage"`
	Jira              JiraConfig    `yaml:"jira"`
	GitHub            GitHubConfig  `yaml:"github"`
	Repos             []RepoConfig  `yaml:"repos"`
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
// per-repo settings live in .hivedispatch.yaml inside the governed repo.
type ClaudeConfig struct {
	Binary string `yaml:"binary"` // default "claude"
	Model  string `yaml:"model"`  // default model when the repo sets none
}

// CodexConfig configures the Codex executor at the worker level; per-repo
// settings live under executor.codex in .hivedispatch.yaml.
type CodexConfig struct {
	Binary string `yaml:"binary"` // default "codex"
	Model  string `yaml:"model"`  // default model when the repo sets none
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

// RepoConfig is one repository the worker may dispatch work into.
type RepoConfig struct {
	Name          string `yaml:"name"`           // owner/repo
	URL           string `yaml:"url"`            // clone URL
	DefaultBranch string `yaml:"default_branch"` // default "main"
	Project       string `yaml:"project"`        // ticket key prefix; tickets in this project map to this repo
	JiraProject   string `yaml:"jira_project"`   // deprecated alias for project
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
	s := &c.Jira.Statuses
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&s.Ready, "Ready")
	def(&s.InProgress, "In Progress")
	def(&s.NeedsInfo, "Needs Info")
	def(&s.InReview, "In Review")
	def(&s.NeedsHuman, "Needs Human")
	def(&c.GitHub.APIURL, "https://api.github.com")
	def(&c.StateStore, "branch")
	if c.RetentionDays == 0 {
		c.RetentionDays = 30
	}
	def(&c.Executor, "claude")
	def(&c.Claude.Binary, "claude")
	def(&c.Codex.Binary, "codex")
	def(&c.Triage.Kind, "claude")
	if c.Triage.StepBudget == 0 {
		c.Triage.StepBudget = 40
	}
	if c.Triage.Timeout == 0 {
		c.Triage.Timeout = 5 * time.Minute
	}
	for i := range c.Repos {
		def(&c.Repos[i].DefaultBranch, "main")
		def(&c.Repos[i].Project, c.Repos[i].JiraProject)
	}
	def(&c.Tracker, "jira")
	l := &c.GitHub.Labels
	def(&l.Ready, "hive:ready")
	def(&l.InProgress, "hive:in-progress")
	def(&l.NeedsInfo, "hive:needs-info")
	def(&l.InReview, "hive:in-review")
	def(&l.NeedsHuman, "hive:needs-human")
	p := &c.GitHub.Project
	def(&p.Field, "Status")
	def(&p.Columns.Ready, "Ready")
	def(&p.Columns.InProgress, "In Progress")
	def(&p.Columns.NeedsInfo, "Needs Info")
	def(&p.Columns.InReview, "In Review")
	def(&p.Columns.NeedsHuman, "Needs Human")
}

// Validate returns an error listing every missing or inconsistent field,
// each with a hint about where the value comes from.
//
// The claim field ids are optional: when empty they are resolved by name at
// startup.
func (c *Config) Validate() error {
	var problems []string
	need := func(v, name, hint string) {
		if strings.TrimSpace(v) == "" {
			problems = append(problems, name+" is required — "+hint)
		}
	}
	placeholder := func(v, name string) {
		for _, ph := range starterPlaceholders {
			if strings.Contains(v, ph) {
				problems = append(problems, name+" still has the starter placeholder "+ph+" — replace it with your real value")
				return
			}
		}
	}
	need(c.AgentID, "agent_id", "any short name for this worker, e.g. laptop-1")
	switch c.Tracker {
	case "", "jira":
		need(c.Jira.BaseURL, "jira.base_url", "your Jira Cloud site, e.g. https://yourteam.atlassian.net")
		need(c.Jira.Email, "jira.email", "the Atlassian account email the API token belongs to")
		need(c.Jira.JQL, "jira.jql", `the query that selects work, e.g. project = KEY AND status = "Ready" AND labels = hive`)
		placeholder(c.Jira.BaseURL, "jira.base_url")
		placeholder(c.Jira.Email, "jira.email")
		placeholder(c.Jira.JQL, "jira.jql")
		for _, f := range []struct{ v, name string }{{c.Jira.Fields.AgentID, "jira.fields.agent_id"}, {c.Jira.Fields.ClaimedAt, "jira.fields.claimed_at"}} {
			if f.v != "" && !strings.HasPrefix(f.v, "customfield_") {
				problems = append(problems, f.name+" must be a Jira custom field id like customfield_10042 (or leave it empty to look the field up by name)")
			}
		}
		if c.Jira.Token == "" {
			problems = append(problems, "HIVE_JIRA_TOKEN environment variable is required — create an API token at https://id.atlassian.com/manage-profile/security/api-tokens and `export HIVE_JIRA_TOKEN=...`")
		}
	case "github":
		if c.GitHub.Token == "" {
			problems = append(problems, "tracker: github needs a GitHub token with Issues read/write — `export HIVE_GITHUB_TOKEN=...`, or run `gh auth login` (the GitHub CLI token is picked up automatically)")
		}
		if p := c.GitHub.Project; p.Enabled() {
			need(p.Owner, "github.project.owner", "the user or organisation that owns the board: OWNER in github.com/users/OWNER/projects/N")
			if p.Number <= 0 {
				problems = append(problems, "github.project.number is required — N in github.com/users/OWNER/projects/N")
			}
		}
	default:
		problems = append(problems, fmt.Sprintf("tracker: want jira or github, got %q", c.Tracker))
	}
	if len(c.Repos) == 0 {
		problems = append(problems, "repos must list at least one repository — name (owner/repo), url (clone URL), project (the ticket key prefix)")
	}
	seen := map[string]int{}
	for i, r := range c.Repos {
		key := strings.ToUpper(r.Project)
		if j, dup := seen[key]; dup && key != "" {
			problems = append(problems, fmt.Sprintf("repos[%d].project %q is also used by repos[%d] — ticket keys must map to one repository", i, r.Project, j))
		}
		seen[key] = i
	}
	for i, r := range c.Repos {
		need(r.Name, fmt.Sprintf("repos[%d].name", i), "owner/repo as shown on GitHub")
		need(r.URL, fmt.Sprintf("repos[%d].url", i), "the clone URL your git credentials can push to, e.g. git@github.com:owner/repo.git")
		need(r.Project, fmt.Sprintf("repos[%d].project", i), "the ticket key prefix: the Jira project key, or any short upper-case tag for GitHub Issues")
		placeholder(r.Name, fmt.Sprintf("repos[%d].name", i))
		placeholder(r.URL, fmt.Sprintf("repos[%d].url", i))
		if r.Project == "KEY" {
			problems = append(problems, fmt.Sprintf("repos[%d].project still has the starter placeholder KEY — use your project key", i))
		} else if r.Project != "" && !projectKeyRe.MatchString(r.Project) {
			problems = append(problems, fmt.Sprintf("repos[%d].project %q is not a project key — it is the letters before the dash in ticket keys, e.g. SCRUM for SCRUM-4", i, r.Project))
		}
	}
	if c.Executor != "" && c.Executor != "claude" && c.Executor != "codex" && c.Executor != "fake" {
		problems = append(problems, fmt.Sprintf("executor: want claude, codex or fake, got %q", c.Executor))
	}
	if c.Triage.Kind != "" && c.Triage.Kind != "claude" && c.Triage.Kind != "passthrough" {
		problems = append(problems, fmt.Sprintf("triage.kind: want claude or passthrough, got %q", c.Triage.Kind))
	}
	if c.RetentionDays < -1 {
		problems = append(problems, "retention_days must be -1 (never prune), or a number of days")
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
	if len(problems) == 0 {
		return nil
	}
	return errors.New("invalid config:\n  - " + strings.Join(problems, "\n  - ") + "\n\nSee docs/setup.md for the full walkthrough.")
}
