package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const workerYAML = `
machine_id: worker-a
workroot: ~/hive-work
jira:
  email: me@example.com
`

const jiraRepoYAML = `
ticket_prefix: HIVE
ticket_tracker: jira
jira:
  base_url: https://example.atlassian.net
  jql: 'project = HIVE'
  fields:
    agent_id: customfield_10042
    claimed_at: customfield_10043
`

const githubRepoYAML = `
ticket_prefix: HD
ticket_tracker: github
`

// fakeOrigin replaces the git lookup for the duration of a test: every
// repo's origin is git@github.com:o/<dir name>.git on branch trunk.
func fakeOrigin(t *testing.T) {
	t.Helper()
	old := lookupOrigin
	lookupOrigin = func(_ context.Context, dir string) (string, string, error) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			return "", "", errors.New("not a git repository")
		}
		return "git@github.com:o/" + filepath.Base(dir) + ".git", "trunk", nil
	}
	t.Cleanup(func() { lookupOrigin = old })
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// makeRepo creates a git-looking directory; with a non-empty repoYAML it is
// enrolled.
func makeRepo(t *testing.T, dir, repoYAML string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if repoYAML != "" {
		if err := os.MkdirAll(filepath.Join(dir, RepoDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, RepoDir, RepoFileName), []byte(repoYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// setup writes a worker config whose code_dirs holds one enrolled Jira repo
// named "hive", and returns the config path and the code dir.
func setup(t *testing.T, extra string) (cfgPath, code string) {
	t.Helper()
	fakeOrigin(t)
	code = t.TempDir()
	makeRepo(t, filepath.Join(code, "hive"), jiraRepoYAML)
	return writeTemp(t, workerYAML+"code_dirs: ["+code+"]\n"+extra), code
}

func TestLoadAppliesDefaultsAndEnvToken(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	t.Setenv("HOME", "/home/tester")
	p, code := setup(t, "")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workroot != "/home/tester/hive-work" {
		t.Errorf("workroot = %q, want ~ expanded", cfg.Workroot)
	}
	if cfg.PollInterval != 60*time.Second || cfg.HeartbeatInterval != 60*time.Second || cfg.ClaimTimeout != 2*time.Hour {
		t.Errorf("duration defaults: %+v", cfg)
	}
	if cfg.ScanDepth != 4 || cfg.MaxConcurrent != 1 {
		t.Errorf("scan_depth/max_concurrent defaults = %d/%d", cfg.ScanDepth, cfg.MaxConcurrent)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("repos = %+v", cfg.Repos)
	}
	r := cfg.Repos[0]
	if r.Path != filepath.Join(code, "hive") || r.Name != "o/hive" || r.URL != "git@github.com:o/hive.git" || r.DefaultBranch != "trunk" {
		t.Errorf("origin not resolved: %+v", r)
	}
	if r.Jira.Token != "secret" || r.Jira.Email != "me@example.com" {
		t.Errorf("worker credentials not merged into repo: %+v", r.Jira)
	}
	if r.Jira.Statuses.Ready != "Ready" || r.Jira.Statuses.NeedsHuman != "Needs Human" {
		t.Errorf("status defaults not applied: %+v", r.Jira.Statuses)
	}
	if r.GitHub.APIURL != "https://api.github.com" || r.GitHub.Token != "gh" {
		t.Errorf("github account not merged: %+v", r.GitHub)
	}
}

func TestRepoOverridesOrigin(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, code := setup(t, "")
	makeRepo(t, filepath.Join(code, "gh"), githubRepoYAML+"name: acme/app\nurl: https://github.com/acme/app.git\ndefault_branch: develop\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.RepoByProject("hd")
	if r == nil || r.Name != "acme/app" || r.URL != "https://github.com/acme/app.git" || r.DefaultBranch != "develop" {
		t.Errorf("repo = %+v", r)
	}
}

func TestExplicitReposAndScanAreMergedAndDeduped(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	fakeOrigin(t)
	code := t.TempDir()
	makeRepo(t, filepath.Join(code, "hive"), jiraRepoYAML)
	makeRepo(t, filepath.Join(code, "plain"), "")
	elsewhere := makeRepo(t, filepath.Join(t.TempDir(), "gh"), githubRepoYAML)
	// A second clone of the same repository (same origin name) is shadowed.
	clone := makeRepo(t, filepath.Join(t.TempDir(), "hive"), jiraRepoYAML)
	body := workerYAML + "code_dirs: [" + code + "]\nrepos:\n  - path: " + elsewhere + "\n  - path: " + clone + "\n  - path: " + filepath.Join(code, "hive") + "\n"
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range cfg.Repos {
		paths = append(paths, r.Path)
	}
	if len(paths) != 2 || paths[0] != elsewhere || paths[1] != clone {
		t.Errorf("repos = %v (explicit first, in order; code_dirs copy shadowed)", paths)
	}
	if cfg.Shadowed[filepath.Join(code, "hive")] != clone {
		t.Errorf("shadowed = %v", cfg.Shadowed)
	}
}

func TestExplicitRepoMustBeEnrolled(t *testing.T) {
	fakeOrigin(t)
	plain := makeRepo(t, t.TempDir(), "")
	_, err := Load(writeTemp(t, workerYAML+"repos:\n  - path: "+plain+"\n"))
	if err == nil || !strings.Contains(err.Error(), "init -github") || !strings.Contains(err.Error(), plain) {
		t.Fatalf("err = %v", err)
	}
}

func TestNoReposIsAnError(t *testing.T) {
	_, err := Load(writeTemp(t, workerYAML))
	if err == nil || !strings.Contains(err.Error(), "hivedispatch scan") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadWorkerNeedsNoRepos(t *testing.T) {
	cfg, err := LoadWorker(writeTemp(t, workerYAML))
	if err != nil || len(cfg.Repos) != 0 || cfg.MachineID != "worker-a" {
		t.Fatalf("LoadWorker = %+v, %v", cfg, err)
	}
}

func TestLegacyLayoutIsRejected(t *testing.T) {
	legacy := `
ticket_tracker: github
machine_id: w
jira:
  base_url: https://x.atlassian.net
github:
  labels: {ready: go}
repos:
  - name: o/r
    url: git@github.com:o/r.git
    project: R
`
	_, err := LoadWorker(writeTemp(t, legacy))
	if err == nil {
		t.Fatal("legacy layout must be rejected")
	}
	for _, want := range []string{"tracker", "jira.base_url", "github.labels", "repos[0].name", ".hive-dispatch/repo.yaml", "docs/config.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%s", want, err)
		}
	}
}

func TestLoadParsesDurations(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, _ := setup(t, "poll_interval: 90s\nclaim_timeout: 3h\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 90*time.Second || cfg.ClaimTimeout != 3*time.Hour {
		t.Errorf("durations = %v/%v", cfg.PollInterval, cfg.ClaimTimeout)
	}
}

func TestJiraTokenOnlyNeededForJiraRepos(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "")
	p, _ := setup(t, "")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "HIVE_JIRA_TOKEN") || !strings.Contains(err.Error(), "id.atlassian.com") {
		t.Fatalf("err = %v, want mention of HIVE_JIRA_TOKEN", err)
	}
	fakeOrigin(t)
	code := t.TempDir()
	makeRepo(t, filepath.Join(code, "gh"), githubRepoYAML)
	if _, err := Load(writeTemp(t, "machine_id: w\ncode_dirs: ["+code+"]\n")); err != nil {
		t.Fatalf("github-only worker must not need jira settings: %v", err)
	}
}

func TestJiraEmailRequiredForJiraRepos(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	fakeOrigin(t)
	code := t.TempDir()
	makeRepo(t, filepath.Join(code, "hive"), jiraRepoYAML)
	_, err := Load(writeTemp(t, "machine_id: w\ncode_dirs: ["+code+"]\n"))
	if err == nil || !strings.Contains(err.Error(), "jira.email") {
		t.Fatalf("err = %v", err)
	}
}

func TestRepoProblemsNameTheFile(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	fakeOrigin(t)
	code := t.TempDir()
	dir := makeRepo(t, filepath.Join(code, "hive"), "ticket_prefix: HIVE\nticket_tracker: jira\n")
	_, err := Load(writeTemp(t, workerYAML+"code_dirs: ["+code+"]\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	file := filepath.Join(dir, RepoDir, RepoFileName)
	for _, want := range []string{file + ": jira.base_url", file + ": jira.jql"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q:\n%s", want, err)
		}
	}
}

func TestRepoFileRejectsUnknownAndWorkerKeys(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	fakeOrigin(t)
	code := t.TempDir()
	makeRepo(t, filepath.Join(code, "hive"), jiraRepoYAML+"  email: me@example.com\n")
	_, err := Load(writeTemp(t, workerYAML+"code_dirs: ["+code+"]\n"))
	if err == nil || !strings.Contains(err.Error(), "email") {
		t.Fatalf("jira.email in repo.yaml should be rejected: %v", err)
	}
}

func TestValidateReportsEveryMissingField(t *testing.T) {
	err := (&Config{}).Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"machine_id", "repositories", "docs/setup.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestValidateRejectsClaimTimeoutShorterThanHeartbeat(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, _ := setup(t, "heartbeat_interval: 5m\nclaim_timeout: 1m\n")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "claim_timeout") {
		t.Fatalf("err = %v, want claim_timeout complaint", err)
	}
}

func TestClaimFieldIDsAreOptionalButChecked(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	fakeOrigin(t)
	code := t.TempDir()
	makeRepo(t, filepath.Join(code, "hive"), strings.ReplaceAll(jiraRepoYAML, "  fields:\n    agent_id: customfield_10042\n    claimed_at: customfield_10043\n", ""))
	if _, err := Load(writeTemp(t, workerYAML+"code_dirs: ["+code+"]\n")); err != nil {
		t.Fatalf("empty field ids must be allowed: %v", err)
	}
	code = t.TempDir()
	makeRepo(t, filepath.Join(code, "hive"), strings.ReplaceAll(jiraRepoYAML, "customfield_10043", "HiveDispatch Claimed At"))
	if _, err := Load(writeTemp(t, workerYAML+"code_dirs: ["+code+"]\n")); err == nil || !strings.Contains(err.Error(), "customfield_") {
		t.Fatalf("a name instead of an id should be rejected with a hint: %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadRunDefaultsAndWindows(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, _ := setup(t, `
run_windows:
  timezone: America/New_York
  windows:
    - days: [mon, tue]
      start: "22:00"
      end: "06:00"
`)
	cfg, err := Load(p)
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
	p, _ := setup(t, "run_windows:\n  timezone: Mars/Olympus\n  windows:\n    - {start: \"25:00\", end: \"06:00\"}\n")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "timezone") || !strings.Contains(err.Error(), "start") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitHubTokenIsOptionalButStateStoreIsChecked(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "")
	p, _ := setup(t, "")
	if _, err := Load(p); err != nil {
		t.Fatalf("missing GitHub token must not fail validation: %v", err)
	}
	p, _ = setup(t, "state_store: cloud\n")
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "state_store") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadExecutorAndTriageDefaults(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, _ := setup(t, "")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Claude.Binary != "claude" || cfg.Codex.Binary != "codex" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.Triage.Kind != "claude" || cfg.Triage.StepBudget != 40 || cfg.Triage.Timeout != 5*time.Minute {
		t.Errorf("triage = %+v", cfg.Triage)
	}
	for extra, want := range map[string]string{"triage: {kind: coinflip}\n": "triage.kind", "max_concurrent: -1\n": "max_concurrent", "scan_depth: -1\n": "scan_depth"} {
		p, _ := setup(t, extra)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v", extra, err)
		}
	}
}

func TestLoadRetentionDefault(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, _ := setup(t, "")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetentionDays != 30 {
		t.Errorf("retention_days default = %d", cfg.RetentionDays)
	}
	p, _ = setup(t, "retention_days: -1\n")
	if cfg, err := Load(p); err != nil || cfg.RetentionDays != -1 {
		t.Errorf("-1 should mean never: %v %v", cfg, err)
	}
}

func TestStarterConfigsAreRejectedUntilEdited(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	fakeOrigin(t)
	code := t.TempDir()
	makeRepo(t, filepath.Join(code, "hive"), RepoStarter("jira"))
	worker := strings.Replace(Starter, "# code_dirs: [~/code]", "code_dirs: ["+code+"]", 1)
	worker = strings.Replace(worker, "# jira:\n#   email: you@example.com", "jira:\n  email: you@example.com", 1)
	p := writeTemp(t, worker)
	_, err := Load(p)
	if err == nil {
		t.Fatal("unedited starters must not validate")
	}
	for _, want := range []string{"jira.base_url", "YOURTEAM", "jira.email", "project", "placeholder"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err should mention %q:\n%s", want, err)
		}
	}
	edited := strings.NewReplacer("YOURTEAM", "acme", "project = KEY", "project = ACME", "ticket_prefix: KEY", "ticket_prefix: ACME").Replace(RepoStarter("jira"))
	makeRepo(t, filepath.Join(code, "hive"), edited)
	if _, err := Load(writeTemp(t, strings.ReplaceAll(worker, "you@example.com", "me@acme.com"))); err != nil {
		t.Fatalf("edited starters should validate: %v", err)
	}
	gh := strings.ReplaceAll(RepoStarter("github"), "ticket_prefix: KEY", "ticket_prefix: ACME")
	makeRepo(t, filepath.Join(code, "hive"), gh)
	if _, err := Load(writeTemp(t, strings.ReplaceAll(worker, "you@example.com", "me@acme.com"))); err != nil {
		t.Fatalf("edited github starter should validate: %v", err)
	}
}

func TestValidateRejectsNonKeyProject(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	fakeOrigin(t)
	code := t.TempDir()
	makeRepo(t, filepath.Join(code, "hive"), strings.ReplaceAll(jiraRepoYAML, "ticket_prefix: HIVE", "ticket_prefix: 1"))
	_, err := Load(writeTemp(t, workerYAML+"code_dirs: ["+code+"]\n"))
	if err == nil || !strings.Contains(err.Error(), "e.g. GITHUB") {
		t.Fatalf("err = %v, want a hint with an example prefix", err)
	}
}

func TestGitHubRepoDefaultsAndProject(t *testing.T) {
	fakeOrigin(t)
	code := t.TempDir()
	makeRepo(t, filepath.Join(code, "gh"), githubRepoYAML)
	cfg, err := Load(writeTemp(t, "machine_id: w\ncode_dirs: ["+code+"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Repos[0]
	if r.Tracker != "github" || r.Project != "HD" {
		t.Errorf("repo = %+v", r)
	}
	if l := r.GitHub.Labels; l.Ready != "hive:ready" || l.NeedsHuman != "hive:needs-human" {
		t.Errorf("label defaults = %+v", l)
	}
	if r.GitHub.Project.Enabled() {
		t.Errorf("no project block must mean disabled: %+v", r.GitHub.Project)
	}
	makeRepo(t, filepath.Join(code, "gh"), githubRepoYAML+"github:\n  project:\n    owner: thomasmeadows\n    number: 2\n")
	cfg, err = Load(writeTemp(t, "machine_id: w\ncode_dirs: ["+code+"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Repos[0].GitHub.Project
	if !p.Enabled() || p.Owner != "thomasmeadows" || p.Number != 2 || p.Field != "Status" || p.Columns.InProgress != "In Progress" {
		t.Errorf("project = %+v", p)
	}
	makeRepo(t, filepath.Join(code, "gh"), githubRepoYAML+"github:\n  project:\n    number: 2\n")
	if _, err := Load(writeTemp(t, "machine_id: w\ncode_dirs: ["+code+"]\n")); err == nil || !strings.Contains(err.Error(), "github.project.owner") {
		t.Errorf("missing owner: %v", err)
	}
}

func TestTrackerRequiredAndChecked(t *testing.T) {
	fakeOrigin(t)
	for body, want := range map[string]string{"ticket_prefix: HD\n": "tracker is required", "ticket_prefix: HD\nticket_tracker: trello\n": "trello"} {
		code := t.TempDir()
		makeRepo(t, filepath.Join(code, "r"), body)
		if _, err := Load(writeTemp(t, "machine_id: w\ncode_dirs: ["+code+"]\n")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v", body, err)
		}
	}
}

func TestValidateRejectsDuplicateProjects(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, code := setup(t, "")
	makeRepo(t, filepath.Join(code, "other"), jiraRepoYAML)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "also used by") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRepoResolvesOneRepo(t *testing.T) {
	fakeOrigin(t)
	worker, err := LoadWorker(writeTemp(t, "machine_id: w\n"))
	if err != nil {
		t.Fatal(err)
	}
	dir := makeRepo(t, filepath.Join(t.TempDir(), "gh"), githubRepoYAML)
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	r, err := worker.LoadRepo(dir)
	if err != nil || r.Name != "o/gh" || r.GitHub.Labels.Ready != "hive:ready" {
		t.Fatalf("LoadRepo = %+v, %v", r, err)
	}
	if worker.Enrolled(dir) {
		t.Error("a repo outside code_dirs and repos: is not enrolled")
	}
	if _, err := worker.LoadRepo(makeRepo(t, t.TempDir(), "")); err == nil || !strings.Contains(err.Error(), "init") {
		t.Errorf("unenrolled dir: %v", err)
	}
}

func TestSetGitHubTokenReachesEveryRepo(t *testing.T) {
	c := &Config{Repos: []RepoConfig{{}, {}}}
	c.SetGitHubToken("tok")
	if c.GitHub.Token != "tok" || c.Repos[0].GitHub.Token != "tok" || c.Repos[1].GitHub.Token != "tok" {
		t.Errorf("c = %+v", c)
	}
}

func TestMaxConcurrentAboveOne(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, _ := setup(t, "max_concurrent: 3\n")
	cfg, err := Load(p)
	if err != nil || cfg.MaxConcurrent != 3 {
		t.Fatalf("cfg.MaxConcurrent = %v, err %v", cfg, err)
	}
}

func TestParseRepoChecksContentWithoutWriting(t *testing.T) {
	fakeOrigin(t)
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	worker, err := LoadWorker(writeTemp(t, "machine_id: w\n"))
	if err != nil {
		t.Fatal(err)
	}
	dir := makeRepo(t, filepath.Join(t.TempDir(), "gh"), "")
	r, err := worker.ParseRepo(dir, []byte(githubRepoYAML))
	if err != nil || r.Name != "o/gh" || r.Project != "HD" {
		t.Fatalf("ParseRepo = %+v, %v", r, err)
	}
	_, err = worker.ParseRepo(dir, []byte("ticket_prefix: HD\nticket_tracker: github\njira:\n  email: me@x\n"))
	if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, RepoDir, RepoFileName)) {
		t.Errorf("worker key in repo.yaml: %v, want an error naming the file", err)
	}
	if _, err := os.Stat(filepath.Join(dir, RepoDir)); !errors.Is(err, os.ErrNotExist) {
		t.Error("ParseRepo must not write anything")
	}
}

func TestScanRowsDescribesEachRepository(t *testing.T) {
	cfgPath, code := setup(t, "")
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	makeRepo(t, filepath.Join(code, "plain"), "")
	makeRepo(t, filepath.Join(code, "broken"), "ticket_prefix: HD\nticket_tracker: gitlab\n")
	worker, err := LoadWorker(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	roots, home, err := worker.ScanRoots(nil)
	if err != nil || home || len(roots) != 1 {
		t.Fatalf("ScanRoots = %v, %v, %v", roots, home, err)
	}
	rows, err := worker.ScanRows(roots)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ScanRow{}
	for _, r := range rows {
		byName[filepath.Base(r.Path)] = r
	}
	if r := byName["hive"]; !r.Enrolled || !r.PickedUp || r.Problem != "" || r.Project != "HIVE" || r.Tracker != "jira" {
		t.Errorf("hive = %+v", r)
	}
	if r := byName["plain"]; r.Enrolled || r.Problem != "" {
		t.Errorf("plain = %+v", r)
	}
	if r := byName["broken"]; !r.Enrolled || r.Problem == "" {
		t.Errorf("broken = %+v", r)
	}
	bare, err := LoadWorker(writeTemp(t, "machine_id: w\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", code)
	if roots, home, err := bare.ScanRoots(nil); err != nil || !home || len(roots) != 1 || roots[0] != code {
		t.Errorf("no code_dirs: ScanRoots = %v, %v, %v; want the home directory", roots, home, err)
	}
}

func writeAgents(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, RepoDir, AgentsFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsDefaultToOneClaudeAgent(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, _ := setup(t, "")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Repos[0].Agents; len(got) != 1 || got[0] != (Agent{Name: "default", Role: RoleCoding, Executor: "claude"}) {
		t.Errorf("agents = %+v", got)
	}
}

func TestAgentsFileIsReadAndValidated(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, code := setup(t, "")
	repo := filepath.Join(code, "hive")
	writeAgents(t, repo, "# ours\nagents:\n  - name: claude-1\n    executor: claude\n  - name: codex-fast\n    executor: codex\n    model: gpt-5-codex\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{{Name: "claude-1", Role: RoleCoding, Executor: "claude"}, {Name: "codex-fast", Role: RoleCoding, Executor: "codex", Model: "gpt-5-codex"}}
	if got := cfg.Repos[0].Agents; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("agents = %+v", got)
	}
	for body, want := range map[string]string{
		"agents: []\n": "at least one agent",
		"agents:\n  - name: a\n    executor: claude\n  - name: A\n    executor: codex\n": "twice",
		"agents:\n  - name: bad name\n    executor: claude\n":                            "name",
		"agents:\n  - name: a\n    executor: gpt\n":                                      "executor",
		"agents:\n  - name: a\n    executor: claude\n    permission_mode: auto\n":        "permission_mode",
	} {
		writeAgents(t, repo, body)
		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), AgentsFileName) {
			t.Errorf("%q: err = %v, want %q and the file name", body, err, want)
		}
	}
}

func TestParseAgentsDefaultsExecutor(t *testing.T) {
	got, err := ParseAgents([]byte("agents:\n  - name: solo\n"))
	if err != nil || len(got) != 1 || got[0].Executor != "claude" {
		t.Errorf("ParseAgents = %+v, %v", got, err)
	}
}

func TestExecutorKeysMovedIntoAgents(t *testing.T) {
	for _, extra := range []string{"executor: codex\n", "claude:\n  model: opus\n", "codex:\n  model: gpt-5-codex\n"} {
		p, _ := setup(t, extra)
		if _, err := LoadWorker(p); err == nil || !strings.Contains(err.Error(), AgentsFileName) {
			t.Errorf("%q: err = %v, want a pointer to %s", extra, err, AgentsFileName)
		}
	}
	p, _ := setup(t, "claude:\n  binary: /opt/claude\ncodex:\n  binary: /opt/codex\n")
	cfg, err := LoadWorker(p)
	if err != nil || cfg.Claude.Binary != "/opt/claude" || cfg.Codex.Binary != "/opt/codex" {
		t.Errorf("binaries stay in the worker config: %+v, %v", cfg, err)
	}
}

func TestMachineIDDefaultsToHostname(t *testing.T) {
	old := hostname
	t.Cleanup(func() { hostname = old })
	hostname = func() (string, error) { return "desk-7", nil }
	cfg, err := LoadWorker(writeTemp(t, "code_dirs: [/tmp]\n"))
	if err != nil || cfg.MachineID != "desk-7" {
		t.Fatalf("machine_id = %q, %v; want the hostname", cfg.MachineID, err)
	}
	if cfg, err := LoadWorker(writeTemp(t, "machine_id: laptop-1\n")); err != nil || cfg.MachineID != "laptop-1" {
		t.Errorf("explicit machine_id = %q, %v", cfg.MachineID, err)
	}
	hostname = func() (string, error) { return "", errors.New("no hostname") }
	if _, err := LoadWorker(writeTemp(t, "code_dirs: [/tmp]\n")); err == nil || !strings.Contains(err.Error(), "machine_id is required") {
		t.Errorf("no hostname: %v", err)
	}
}

func TestAgentIDIsRenamedMachineID(t *testing.T) {
	_, err := LoadWorker(writeTemp(t, "agent_id: worker-1\n"))
	if err == nil || !strings.Contains(err.Error(), "machine_id") || !strings.Contains(err.Error(), "agent_id") {
		t.Errorf("err = %v, want a hint to rename agent_id to machine_id", err)
	}
}

func TestTicketPrefixDefaultsToTheTracker(t *testing.T) {
	fakeOrigin(t)
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	worker, err := LoadWorker(writeTemp(t, "machine_id: w\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := worker.ParseRepo(makeRepo(t, filepath.Join(t.TempDir(), "gh"), ""), []byte("ticket_tracker: github\n"))
	if err != nil || r.Project != "GITHUB" {
		t.Errorf("github default prefix = %q, %v", r.Project, err)
	}
	r, err = worker.ParseRepo(makeRepo(t, filepath.Join(t.TempDir(), "gh"), ""), []byte("ticket_tracker: github\nticket_prefix: web\n"))
	if err != nil || r.Project != "WEB" {
		t.Errorf("explicit prefix = %q, %v; want it upper-cased", r.Project, err)
	}
}

func TestJiraPrefixDefaultsToJIRA(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, code := setup(t, "")
	makeRepo(t, filepath.Join(code, "hive"), strings.Replace(jiraRepoYAML, "ticket_prefix: HIVE\n", "", 1))
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if r := cfg.Repos[0]; r.Project != "JIRA" {
		t.Errorf("prefix = %q, want JIRA", r.Project)
	}
}

func TestOldRepoKeysAreRenamed(t *testing.T) {
	fakeOrigin(t)
	worker, err := LoadWorker(writeTemp(t, "machine_id: w\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.ParseRepo(makeRepo(t, filepath.Join(t.TempDir(), "gh"), ""), []byte("project: HD\ntracker: github\n"))
	for _, want := range []string{"project was renamed ticket_prefix", "tracker was renamed ticket_tracker"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
}

func TestDefaultPrefixesMustNotCollide(t *testing.T) {
	fakeOrigin(t)
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	code := t.TempDir()
	makeRepo(t, filepath.Join(code, "a"), "ticket_tracker: github\n")
	makeRepo(t, filepath.Join(code, "b"), "ticket_tracker: github\n")
	_, err := Load(writeTemp(t, "machine_id: w\ncode_dirs: ["+code+"]\n"))
	if err == nil || !strings.Contains(err.Error(), `ticket_prefix "GITHUB" is also used by`) {
		t.Errorf("err = %v", err)
	}
}

func TestPlanningStateDefaults(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, _ := setup(t, "")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Repos[0]
	if r.GitHub.Labels.Planning != "hive:planning" || r.GitHub.Project.Columns.Planning != "Planning" || r.Jira.Statuses.Planning != "Planning" {
		t.Errorf("planning defaults: label %q column %q status %q", r.GitHub.Labels.Planning, r.GitHub.Project.Columns.Planning, r.Jira.Statuses.Planning)
	}
	if cfg.MaxReviewRounds != 2 {
		t.Errorf("max_review_rounds = %d, want 2", cfg.MaxReviewRounds)
	}
	p, _ = setup(t, "max_review_rounds: -1\n")
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "max_review_rounds") {
		t.Errorf("negative rounds: %v", err)
	}
}

func TestAgentRoles(t *testing.T) {
	got, err := ParseAgents([]byte("agents:\n  - name: p\n    role: planning\n  - name: c\n  - name: r\n    role: review\n    executor: codex\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Role != RolePlanning || got[1].Role != RoleCoding || got[2].Role != RoleReview {
		t.Errorf("roles = %+v", got)
	}
	if _, err := ParseAgents([]byte("agents:\n  - name: x\n    role: tester\n")); err == nil || !strings.Contains(err.Error(), "role") {
		t.Errorf("bad role: %v", err)
	}
}

func TestJiraJQLIsScopeOnly(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p, code := setup(t, "")
	makeRepo(t, filepath.Join(code, "hive"), strings.Replace(jiraRepoYAML, `jql: 'project = HIVE'`, `jql: 'project = HIVE AND status = "Ready"'`, 1))
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "jira.jql") || !strings.Contains(err.Error(), "status") {
		t.Errorf("err = %v, want jql without a status clause", err)
	}
}

func TestGrokBinary(t *testing.T) {
	for _, bin := range []string{"grok", "/opt/grok"} {
		extra := ""
		if bin != "grok" {
			extra = "grok:\n  binary: " + bin + "\n"
		}
		p, _ := setup(t, extra)
		cfg, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Grok.Binary != bin {
			t.Fatalf("binary %q, want %q", cfg.Grok.Binary, bin)
		}
	}
}
