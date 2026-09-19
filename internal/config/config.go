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
	"os"
	"strings"
	"time"
)

// Config is the worker-level configuration.
type Config struct {
	AgentID           string        `yaml:"agent_id"`
	Workroot          string        `yaml:"workroot"`
	PollInterval      time.Duration `yaml:"poll_interval"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	ClaimTimeout      time.Duration `yaml:"claim_timeout"`
	Jira              JiraConfig    `yaml:"jira"`
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

// RepoConfig is one repository the worker may dispatch work into.
type RepoConfig struct {
	Name          string `yaml:"name"`           // owner/repo
	URL           string `yaml:"url"`            // clone URL
	DefaultBranch string `yaml:"default_branch"` // default "main"
	JiraProject   string `yaml:"jira_project"`   // tickets in this project map to this repo
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
	for i := range c.Repos {
		def(&c.Repos[i].DefaultBranch, "main")
	}
}

// Validate returns an error listing every missing or inconsistent field.
//
// When HIVE_INIT=1 the claim field IDs are not required, because
// `hivedispatch init -jira` is what creates them.
func (c *Config) Validate() error {
	var problems []string
	need := func(v, name string) {
		if strings.TrimSpace(v) == "" {
			problems = append(problems, name+" is required")
		}
	}
	need(c.AgentID, "agent_id")
	need(c.Jira.BaseURL, "jira.base_url")
	need(c.Jira.Email, "jira.email")
	need(c.Jira.JQL, "jira.jql")
	if os.Getenv("HIVE_INIT") != "1" {
		need(c.Jira.Fields.AgentID, "jira.fields.agent_id")
		need(c.Jira.Fields.ClaimedAt, "jira.fields.claimed_at")
	}
	if c.Jira.Token == "" {
		problems = append(problems, "HIVE_JIRA_TOKEN environment variable is required")
	}
	if len(c.Repos) == 0 {
		problems = append(problems, "repos must list at least one repository")
	}
	for i, r := range c.Repos {
		need(r.Name, fmt.Sprintf("repos[%d].name", i))
		need(r.URL, fmt.Sprintf("repos[%d].url", i))
		need(r.JiraProject, fmt.Sprintf("repos[%d].jira_project", i))
	}
	if c.ClaimTimeout > 0 && c.HeartbeatInterval > 0 && c.ClaimTimeout <= c.HeartbeatInterval {
		problems = append(problems, "claim_timeout must be longer than heartbeat_interval")
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("invalid config:\n  - " + strings.Join(problems, "\n  - "))
}
