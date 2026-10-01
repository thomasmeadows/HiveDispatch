package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRunner answers commands from a table keyed by the joined argv; any
// other command is not found. It records what ran.
type fakeRunner struct {
	mu   sync.Mutex
	outs map[string]string
	code map[string]int
	ran  []string
	// after, when set, runs once a command matched (an install that puts
	// the binary in place).
	after func(cmd string)
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, int, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.ran = append(f.ran, cmd)
	out, ok := f.outs[cmd]
	code := f.code[cmd]
	after := f.after
	f.mu.Unlock()
	if !ok {
		return "", 0, fmt.Errorf("exec: %q: %w", name, exec.ErrNotFound)
	}
	if after != nil {
		after(cmd)
	}
	return out, code, nil
}

type executorRow struct {
	Name      string   `json:"name"`
	Binary    string   `json:"binary"`
	Installed bool     `json:"installed"`
	Version   string   `json:"version"`
	Problem   string   `json:"problem"`
	Install   string   `json:"install"`
	UsedBy    []string `json:"used_by"`
	Path      string   `json:"path"`
	OnPath    bool     `json:"on_path"`
}

func executorsByName(t *testing.T, e *env) map[string]executorRow {
	t.Helper()
	var got struct{ Executors []executorRow }
	if code := e.get(t, "/api/executors", &got); code != 200 {
		t.Fatalf("GET /api/executors = %d", code)
	}
	m := map[string]executorRow{}
	for _, r := range got.Executors {
		m[r.Name] = r
	}
	return m
}

func TestExecutorsReportVersions(t *testing.T) {
	f := &fakeRunner{
		outs: map[string]string{
			"claude --version":   "\n2.1.0 (Claude Code)\nmore\n",
			"my-codex --version": "codex-cli 0.40.0\n",
			"grok --version":     "",
		},
		code: map[string]int{"grok --version": 2},
	}
	home := t.TempDir()
	e := newEnv(t, Options{Run: f.run, GraphInstall: "pipx install hivegraph-test", Getenv: func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}})
	raw, _ := os.ReadFile(e.cfgPath)
	mustWrite(t, e.cfgPath, string(raw)+"codex:\n  binary: my-codex\n")
	mustWrite(t, filepath.Join(e.repo, ".hive-dispatch", "agents.yaml"),
		"agents:\n  - name: c1\n    executor: claude\n  - name: g1\n    executor: langgraph\n    code_with: deepcode\n")

	m := executorsByName(t, e)
	for _, name := range []string{"claude", "codex", "grok", "antigravity", "deepcode", "langgraph"} {
		if _, ok := m[name]; !ok {
			t.Errorf("no row for %s", name)
		}
	}
	if _, ok := m["fake"]; ok {
		t.Error("the fake executor is not something to install")
	}
	if c := m["claude"]; !c.Installed || c.Version != "2.1.0 (Claude Code)" || !c.OnPath || len(c.UsedBy) != 1 || c.UsedBy[0] != "o/app: c1" {
		t.Errorf("claude = %+v", c)
	}
	if c := m["codex"]; !c.Installed || c.Binary != "my-codex" || c.Version != "codex-cli 0.40.0" {
		t.Errorf("codex reads codex.binary: %+v", c)
	}
	if g := m["grok"]; g.Installed || !strings.Contains(g.Problem, "exit 2") {
		t.Errorf("a failing --version is a problem, not installed: %+v", g)
	}
	if a := m["antigravity"]; a.Installed || a.Binary != "agy" || a.Problem != "" || !strings.Contains(a.Install, "antigravity.google") {
		t.Errorf("antigravity = %+v", a)
	}
	if d := m["deepcode"]; len(d.UsedBy) != 1 || d.UsedBy[0] != "o/app: g1 (code_with)" {
		t.Errorf("code_with counts as use: %+v", d)
	}
	if l := m["langgraph"]; l.Binary != "hivegraph" || l.Install != "pipx install hivegraph-test" || len(l.UsedBy) != 1 {
		t.Errorf("langgraph = %+v", l)
	}
}

func TestExecutorFoundOffPath(t *testing.T) {
	home := t.TempDir()
	local := filepath.Join(home, ".local", "bin", "agy")
	mustMkdir(t, filepath.Dir(local))
	mustWrite(t, local, "#!/bin/sh\n")
	f := &fakeRunner{outs: map[string]string{local + " --version": "agy 1.0\n"}}
	e := newEnv(t, Options{Run: f.run, Getenv: func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}})
	a := executorsByName(t, e)["antigravity"]
	if !a.Installed || a.OnPath || a.Path != local || !strings.Contains(a.Problem, "antigravity.binary") {
		t.Errorf("an install under ~/.local/bin off PATH = %+v", a)
	}
}

func TestExecutorInstall(t *testing.T) {
	installed := false
	f := &fakeRunner{outs: map[string]string{
		"sh -c npm install -g @vegamo/deepcode-cli": "added 1 package\n",
	}}
	f.after = func(cmd string) {
		if strings.HasPrefix(cmd, "sh -c") {
			f.mu.Lock()
			f.outs["deepcode --version"] = "0.3.1\n"
			f.mu.Unlock()
			installed = true
		}
	}
	e := newEnv(t, Options{Run: f.run})
	var res struct {
		ExitCode int         `json:"exit_code"`
		Output   string      `json:"output"`
		Command  string      `json:"command"`
		Executor executorRow `json:"executor"`
		Error    string      `json:"error"`
	}
	if code := e.post(t, "/api/executors/install", map[string]string{"name": "deepcode"}, &res); code != 200 {
		t.Fatalf("install = %d %+v", code, res)
	}
	if !installed || res.ExitCode != 0 || !strings.Contains(res.Output, "added 1 package") || res.Command != "npm install -g @vegamo/deepcode-cli" {
		t.Errorf("install result = %+v", res)
	}
	if !res.Executor.Installed || res.Executor.Version != "0.3.1" {
		t.Errorf("the result rechecks the executor: %+v", res.Executor)
	}
	if code := e.post(t, "/api/executors/install", map[string]string{"name": "rm -rf /"}, &res); code != http.StatusBadRequest {
		t.Errorf("unknown executor = %d", code)
	}
	if code := e.post(t, "/api/executors/install", map[string]string{"name": "fake"}, &res); code != http.StatusBadRequest {
		t.Errorf("fake executor = %d", code)
	}
}

func TestExecutorInstallFailureReportsExit(t *testing.T) {
	f := &fakeRunner{
		outs: map[string]string{"sh -c npm install -g @openai/codex": "npm: command not found\n"},
		code: map[string]int{"sh -c npm install -g @openai/codex": 127},
	}
	e := newEnv(t, Options{Run: f.run})
	var res struct {
		ExitCode int         `json:"exit_code"`
		Output   string      `json:"output"`
		Executor executorRow `json:"executor"`
	}
	if code := e.post(t, "/api/executors/install", map[string]string{"name": "codex"}, &res); code != 200 || res.ExitCode != 127 || res.Executor.Installed {
		t.Errorf("failed install = %d %+v", code, res)
	}
}
