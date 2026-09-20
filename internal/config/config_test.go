package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `
agent_id: worker-a
workroot: ~/hive-work
jira:
  base_url: https://example.atlassian.net
  email: me@example.com
  jql: 'project = HIVE AND status = "Ready"'
  fields:
    agent_id: customfield_10042
    claimed_at: customfield_10043
repos:
  - name: thomasmeadows/HiveDispatch
    url: git@github.com:thomasmeadows/HiveDispatch.git
    jira_project: HIVE
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaultsAndEnvToken(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	t.Setenv("HOME", "/home/tester")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Jira.Token != "secret" {
		t.Errorf("token = %q, want secret", cfg.Jira.Token)
	}
	if cfg.Workroot != "/home/tester/hive-work" {
		t.Errorf("workroot = %q, want ~ expanded", cfg.Workroot)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("poll_interval default = %v, want 60s", cfg.PollInterval)
	}
	if cfg.HeartbeatInterval != 60*time.Second {
		t.Errorf("heartbeat_interval default = %v, want 60s", cfg.HeartbeatInterval)
	}
	if cfg.ClaimTimeout != 2*time.Hour {
		t.Errorf("claim_timeout default = %v, want 2h", cfg.ClaimTimeout)
	}
	if cfg.Jira.Statuses.Ready != "Ready" || cfg.Jira.Statuses.NeedsHuman != "Needs Human" {
		t.Errorf("status defaults not applied: %+v", cfg.Jira.Statuses)
	}
	if cfg.Repos[0].DefaultBranch != "main" {
		t.Errorf("default_branch default = %q, want main", cfg.Repos[0].DefaultBranch)
	}
}

func TestLoadParsesDurations(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	body := validYAML + "poll_interval: 90s\nclaim_timeout: 3h\n"
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 90*time.Second || cfg.ClaimTimeout != 3*time.Hour {
		t.Errorf("durations = %v/%v", cfg.PollInterval, cfg.ClaimTimeout)
	}
}

func TestLoadRejectsMissingToken(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "")
	_, err := Load(writeTemp(t, validYAML))
	if err == nil || !strings.Contains(err.Error(), "HIVE_JIRA_TOKEN") {
		t.Fatalf("err = %v, want mention of HIVE_JIRA_TOKEN", err)
	}
}

func TestValidateReportsEveryMissingField(t *testing.T) {
	c := &Config{}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"agent_id", "jira.base_url", "jira.email", "jira.jql", "jira.fields.agent_id", "jira.fields.claimed_at", "repos"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestValidateRejectsClaimTimeoutShorterThanHeartbeat(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	body := validYAML + "heartbeat_interval: 5m\nclaim_timeout: 1m\n"
	_, err := Load(writeTemp(t, body))
	if err == nil || !strings.Contains(err.Error(), "claim_timeout") {
		t.Fatalf("err = %v, want claim_timeout complaint", err)
	}
}

func TestValidateSkipsFieldsDuringInit(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	t.Setenv("HIVE_INIT", "1")
	body := strings.ReplaceAll(validYAML, "    agent_id: customfield_10042\n    claimed_at: customfield_10043\n", "")
	if _, err := Load(writeTemp(t, body)); err != nil {
		t.Fatalf("Load during init: %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadRunDefaultsAndWindows(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	body := validYAML + `
run_windows:
  timezone: America/New_York
  windows:
    - days: [mon, tue]
      start: "22:00"
      end: "06:00"
`
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RunTimeout != 45*time.Minute || cfg.StepBudget != 200 || cfg.MaxAttempts != 3 || cfg.PollJitter != 10*time.Second {
		t.Errorf("defaults: %+v", cfg)
	}
	if cfg.RunWindows.Timezone != "America/New_York" || len(cfg.RunWindows.Windows) != 1 || cfg.RunWindows.Windows[0].End != "06:00" {
		t.Errorf("windows = %+v", cfg.RunWindows)
	}
}

func TestValidateRejectsBadWindow(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	body := validYAML + "run_windows:\n  timezone: Mars/Olympus\n  windows:\n    - {start: \"25:00\", end: \"06:00\"}\n"
	_, err := Load(writeTemp(t, body))
	if err == nil || !strings.Contains(err.Error(), "timezone") || !strings.Contains(err.Error(), "start") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadGitHubAndStateDefaults(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHub.APIURL != "https://api.github.com" || cfg.GitHub.Token != "gh" || cfg.StateStore != "branch" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadRejectsMissingGitHubTokenAndBadStateStore(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "")
	_, err := Load(writeTemp(t, validYAML+"state_store: cloud\n"))
	if err == nil || !strings.Contains(err.Error(), "HIVE_GITHUB_TOKEN") || !strings.Contains(err.Error(), "state_store") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadExecutorDefaults(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Executor != "claude" || cfg.Claude.Binary != "claude" {
		t.Errorf("cfg = %+v", cfg)
	}
	if _, err := Load(writeTemp(t, validYAML+"executor: gpt\n")); err == nil || !strings.Contains(err.Error(), "executor") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadTriageDefaults(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Triage.Kind != "claude" || cfg.Triage.StepBudget != 40 || cfg.Triage.Timeout != 5*time.Minute {
		t.Errorf("triage = %+v", cfg.Triage)
	}
	if _, err := Load(writeTemp(t, validYAML+"triage: {kind: coinflip}\n")); err == nil || !strings.Contains(err.Error(), "triage.kind") {
		t.Errorf("err = %v", err)
	}
}

func TestValidateMessagesSayWhereToGetValues(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "")
	t.Setenv("HIVE_GITHUB_TOKEN", "")
	err := (&Config{}).Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"id.atlassian.com", "github.com/settings", "customfield_", "docs/setup.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%s", want, err)
		}
	}
}

func TestStarterConfigIsRejectedUntilEdited(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_INIT", "1")
	p := writeTemp(t, Starter)
	_, err := Load(p)
	if err == nil {
		t.Fatal("unedited starter must not validate")
	}
	for _, want := range []string{"jira.base_url", "YOURTEAM", "repos[0].name", "placeholder"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err should mention %q:\n%s", want, err)
		}
	}
	// Edited starter validates (fields empty is fine under HIVE_INIT).
	edited := strings.NewReplacer("YOURTEAM", "acme", "you@example.com", "me@acme.com", "project = KEY", "project = ACME",
		"yourorg/yourrepo", "acme/app", "yourorg", "acme", "yourrepo", "app", "jira_project: KEY", "jira_project: ACME").Replace(Starter)
	if _, err := Load(writeTemp(t, edited)); err != nil {
		t.Fatalf("edited starter should validate during init: %v", err)
	}
}
