package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/dispatch"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/state/localdir"
	"github.com/thomasmeadows/hivedispatch/internal/statusline"
)

// TestMain lets the test binary stand in for hivedispatch when a test makes
// it re-exec itself (the supervisor runs `<exe> check`): with
// HIVEDISPATCH_TEST_AS_CLI set it runs the CLI instead of the tests.
func TestMain(m *testing.M) {
	if os.Getenv("HIVEDISPATCH_TEST_AS_CLI") == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "hivedispatch ") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestCheckValidConfig(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	p := writeValidConfigWith(t, "")
	var out, errb bytes.Buffer
	if code := run([]string{"check", "-config", p}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "config ok") || !strings.Contains(out.String(), "o/r") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestCheckInvalidConfig(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "")
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("agent_id: w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"check", "-config", p}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "invalid config") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestInitOnExistingConfigIsANoop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(home, ".config", "hivedispatch", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("agent_id: keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "already exists") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestPollStatusText(t *testing.T) {
	at := time.Date(2026, 9, 20, 2, 42, 15, 0, time.Local)
	cases := []struct {
		last, now time.Time
		want      string
	}{
		{time.Time{}, at, "waiting for first poll"},
		{at, at, "last poll 2026-09-20 02:42:15 (0s ago)"},
		{at, at.Add(12*time.Second + 700*time.Millisecond), "last poll 2026-09-20 02:42:15 (12s ago)"},
		{at, at.Add(3*time.Minute + 5*time.Second), "last poll 2026-09-20 02:42:15 (3m5s ago)"},
	}
	for _, c := range cases {
		if got := pollStatus(c.last, c.now); got != c.want {
			t.Errorf("pollStatus(%v, %v) = %q, want %q", c.last, c.now, got, c.want)
		}
	}
}

func TestShowLastPollDrawsAndClears(t *testing.T) {
	var out syncBuffer
	line := statusline.New(&out)
	d := &dispatch.Dispatcher{}
	stop := showLastPoll(d, line)
	if d.Polled == nil {
		t.Fatal("showLastPoll must install the Polled hook")
	}
	d.Polled(time.Date(2026, 9, 20, 2, 42, 15, 0, time.Local))
	stop()
	got := out.String()
	if !strings.Contains(got, "last poll 2026-09-20 02:42:15") {
		t.Errorf("status line never drawn: %q", got)
	}
	if !strings.HasSuffix(got, "\r\x1b[2K") {
		t.Errorf("stop must erase the line: %q", got)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunRequiresConfig(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"run", "-config", filepath.Join(t.TempDir(), "missing.yaml")}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errb.String())
	}
}

func TestInitWritesStarterConfigWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out, errb bytes.Buffer
	if code := run([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	p := filepath.Join(home, ".config", "hivedispatch", "config.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("starter config not written: %v", err)
	}
	for _, want := range []string{"agent_id:", "code_dirs:", "repos:", ".hive-dispatch/repo.yaml", "HIVE_JIRA_TOKEN", "HIVE_GITHUB_TOKEN", "id.atlassian.com"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("starter config missing %q", want)
		}
	}
	if !strings.Contains(out.String(), p) || !strings.Contains(out.String(), "init -jira") {
		t.Errorf("stdout should name the file and the next step: %q", out.String())
	}
	// Second run must not overwrite.
	if err := os.WriteFile(p, []byte("agent_id: keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if raw, _ := os.ReadFile(p); string(raw) != "agent_id: keep-me\n" {
		t.Error("init overwrote an existing config")
	}
}

func TestInitGitHubOnFreshMachineWritesStarters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"init", "-github", repo}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, f := range []string{filepath.Join(home, ".config", "hivedispatch", "config.yaml"), filepath.Join(repo, ".hive-dispatch", "repo.yaml"), filepath.Join(repo, ".hive-dispatch", "policy.yaml")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(repo, ".hive-dispatch", "repo.yaml"))
	if err != nil || !strings.Contains(string(raw), "tracker: github") {
		t.Errorf("repo.yaml = %q, %v", raw, err)
	}
	for _, want := range []string{"wrote starter config", "repo.yaml", "init -github " + repo, "will not pick up", "- path: " + repo} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout should mention %q:\n%s", want, out.String())
		}
	}
}

func TestInitRelativeDirIsMadeAbsolute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	t.Chdir(repo)
	var out, errb bytes.Buffer
	if code := run([]string{"init", "-github", "."}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "- path: "+repo) || !strings.Contains(out.String(), "code_dirs: ["+filepath.Dir(repo)+"]") {
		t.Errorf("hints should use absolute paths:\n%s", out.String())
	}
}

func TestInitRejectsTrackerMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := writeRepo(t, t.TempDir(), "project: HD\ntracker: github\nname: o/r\nurl: git@github.com:o/r.git\ndefault_branch: main\n")
	var out, errb bytes.Buffer
	if code := run([]string{"init", "-jira", repo}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "tracker: github") || !strings.Contains(errb.String(), "init -github") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestInitReplacesEmptyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(home, ".config", "hivedispatch", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if raw, _ := os.ReadFile(p); len(raw) == 0 {
		t.Error("empty file should be replaced by the starter")
	}
}

func TestInitJiraOnUneditedStarterExplainsEachValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HIVE_JIRA_TOKEN", "")
	t.Setenv("HIVE_GITHUB_TOKEN", "")
	repo := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"init", "-jira", repo}, &out, &errb); code != 0 {
		t.Fatalf("first run writes starters: exit %d: %s", code, errb.String())
	}
	if code := run([]string{"init", "-jira", repo}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errb.String())
	}
	for _, want := range []string{"id.atlassian.com", "YOURTEAM", "jira.email", "repo.yaml", "docs/setup.md"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr should mention %q:\n%s", want, errb.String())
		}
	}
	if strings.Contains(errb.String(), "HIVE_GITHUB_TOKEN") || strings.Contains(errb.String(), "customfield") {
		t.Errorf("init -jira must not demand the GitHub token or the field ids it creates:\n%s", errb.String())
	}
}

// writeRepo writes repoYAML as dir/.hive-dispatch/repo.yaml.
func writeRepo(t *testing.T, dir, repoYAML string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".hive-dispatch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hive-dispatch", "repo.yaml"), []byte(repoYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeValidConfigWith writes a minimal valid worker config, listing one
// Jira repository (o/r, project X) by path, plus extra YAML lines.
func writeValidConfigWith(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	repo := writeRepo(t, filepath.Join(dir, "r"), `
project: X
tracker: jira
name: o/r
url: git@github.com:o/r.git
default_branch: main
jira:
  base_url: https://x.atlassian.net
  jql: project = X
  fields: {agent_id: customfield_1, claimed_at: customfield_2}
`)
	p := filepath.Join(dir, "c.yaml")
	body := "agent_id: w\njira:\n  email: a@b.c\nrepos:\n  - path: " + repo + "\n" + extra
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOnceRejectsUnknownProjectBeforeNetwork(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p := writeValidConfigWith(t, "")
	var out, errb bytes.Buffer
	if code := run([]string{"once", "NOPE-1", "-config", p}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "NOPE") || !strings.Contains(errb.String(), "no enrolled repository") {
		t.Errorf("stderr = %q", errb.String())
	}
	if code := run([]string{"once"}, &out, &errb); code != 2 {
		t.Errorf("missing key should be usage error, got %d", code)
	}
}

func TestStatusWithLocalStore(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := writeValidConfigWith(t, "state_store: local\nworkroot: "+home+"/work\n")
	st := localdir.New(filepath.Join(home, "work", "state", "o__r"))
	if err := st.Save(context.Background(), &state.Run{Ticket: "X-1", Phase: state.PhaseDone, LastStatus: "completed", Attempts: 1, Agent: "w", PRURL: "https://x/pull/1"}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"status", "-config", p}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"X-1", "done", "completed", "https://x/pull/1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := run([]string{"status", "-config", p, "-json"}, &out, &errb); code != 0 || !strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		t.Errorf("json: exit %d out=%q", code, out.String())
	}
}

func TestCheckGithubTrackerNeedsToken(t *testing.T) {
	t.Setenv("HIVE_GITHUB_TOKEN", "")
	t.Setenv("PATH", t.TempDir()) // no gh, no git credential helper
	dir := t.TempDir()
	repo := writeRepo(t, filepath.Join(dir, "r"), "project: HD\ntracker: github\nname: o/r\nurl: git@github.com:o/r.git\ndefault_branch: main\n")
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("agent_id: w\nrepos:\n  - path: "+repo+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"check", "-config", p}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "gh auth login") || !strings.Contains(errb.String(), "HIVE_GITHUB_TOKEN") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestScanListsReposAndEnrolment(t *testing.T) {
	code := t.TempDir()
	for _, d := range []string{"plain", "old", "new"} {
		if err := os.MkdirAll(filepath.Join(code, d, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(code, "old", ".hivedispatch.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	writeRepo(t, filepath.Join(code, "new"), "project: NW\ntracker: github\nname: o/new\nurl: git@github.com:o/new.git\ndefault_branch: main\n")
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("agent_id: w\ncode_dirs: ["+code+"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"scan", "-config", p}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"plain", "legacy .hivedispatch.yaml", "enrolled", "NW", "github", "o/new", "3 repositories, 1 enrolled"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, out.String())
		}
	}
}

func TestSupervisorOneShotFake(t *testing.T) {
	t.Setenv("HIVEDISPATCH_TEST_AS_CLI", "1") // the supervisor re-execs the test binary as the CLI
	dir := t.TempDir()
	var out, errb bytes.Buffer
	stdin := strings.NewReader("what is wrong?\n")
	code := runSupervisor([]string{"-config", filepath.Join(dir, "config.yaml"), "-provider", "fake"}, stdin, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "(fake supervisor") {
		t.Errorf("stdout = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "supervisor", "sessions")); err != nil {
		t.Error("session dir not created beside the config")
	}
}

func TestUsageListsSupervisor(t *testing.T) {
	var out, errb bytes.Buffer
	run(nil, &out, &errb)
	if !strings.Contains(errb.String(), "supervisor") {
		t.Error("usage lacks supervisor")
	}
}
