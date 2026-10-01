package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

// Runner runs a command and returns its combined output and exit code. err
// is set only when the command could not start (exec.ErrNotFound when the
// binary is missing); a nonzero exit is a code, not an error.
type Runner func(ctx context.Context, name string, args ...string) (output string, exitCode int, err error)

// execRunner is the Runner that really runs the command.
func execRunner(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.String(), exitErr.ExitCode(), nil
	}
	return out.String(), 0, err
}

// executorSpec is one executor the Agent Configuration page checks and can
// install. The install command is fixed here, never taken from a request.
type executorSpec struct {
	name, label string
	key         string // the worker config block holding binary:
	binary      string // the default binary
	install     string // a shell command; empty means no automatic install
	docs        string
	note        string
}

// executorSpecs lists every executor but fake, in the order the page shows
// them. langgraph's install command depends on the binary's version and is
// filled in from Options.GraphInstall.
var executorSpecs = []executorSpec{
	{name: "claude", label: "Claude Code", key: "claude", binary: "claude",
		install: "curl -fsSL https://claude.ai/install.sh | bash",
		docs:    "https://docs.anthropic.com/en/docs/claude-code/setup",
		note:    "Then run claude once to log in."},
	{name: "codex", label: "Codex", key: "codex", binary: "codex",
		install: "npm install -g @openai/codex",
		docs:    "https://github.com/openai/codex",
		note:    "Needs Node.js. Then run codex once to log in."},
	{name: "grok", label: "Grok Build", key: "grok", binary: "grok",
		install: "curl -fsSL https://x.ai/cli/install.sh | bash",
		docs:    "https://github.com/xai-org/grok-build",
		note:    "Then run grok login (grok login --device-code on a headless machine), or set XAI_API_KEY."},
	{name: "antigravity", label: "Antigravity CLI", key: "antigravity", binary: "agy",
		install: "curl -fsSL https://antigravity.google/cli/install.sh | bash",
		docs:    "https://antigravity.google/docs/cli/install/",
		note:    "Then run agy once to authenticate."},
	{name: "deepcode", label: "DeepCode", key: "deepcode", binary: "deepcode",
		install: "npm install -g @vegamo/deepcode-cli",
		docs:    "https://api-docs.deepseek.com/quick_start/agent_integrations/deepcode",
		note:    "Needs Node.js. Then put the API key and model in ~/.deepcode/settings.json."},
	{name: "langgraph", label: "LangGraph workflow (hivegraph)", key: "graph", binary: "hivegraph",
		docs: "https://github.com/thomasmeadows/HiveDispatch/tree/main/graph",
		note: "Optional: only agents with executor langgraph need it. Needs Python and pipx. The chat model is set under Configuration → Graph workflows."},
}

const (
	versionTimeout = 15 * time.Second
	installTimeout = 10 * time.Minute
	maxVersionLen  = 200
)

// executorStatus is one row of GET /api/executors.
type executorStatus struct {
	Name      string   `json:"name"`
	Label     string   `json:"label"`
	Binary    string   `json:"binary"`
	ConfigKey string   `json:"config_key"`
	Installed bool     `json:"installed"`
	Version   string   `json:"version,omitempty"`
	Path      string   `json:"path,omitempty"`
	OnPath    bool     `json:"on_path"`
	Problem   string   `json:"problem,omitempty"`
	Install   string   `json:"install,omitempty"`
	Docs      string   `json:"docs"`
	Note      string   `json:"note"`
	UsedBy    []string `json:"used_by"`
}

func (s *Server) runner() Runner {
	if s.o.Run != nil {
		return s.o.Run
	}
	return execRunner
}

func (s *Server) spec(name string) (executorSpec, bool) {
	for _, sp := range executorSpecs {
		if sp.name == name {
			if sp.name == "langgraph" {
				sp.install = s.o.GraphInstall
			}
			return sp, true
		}
	}
	return executorSpec{}, false
}

// binaries reads <block>.binary for every executor from the worker config,
// falling back to each default. A missing or broken file means defaults.
func (s *Server) binaries() map[string]string {
	var f map[string]struct {
		Binary string `yaml:"binary"`
	}
	if raw, err := os.ReadFile(s.o.ConfigPath); err == nil {
		// A config that does not parse still gets the default binaries; the
		// Configuration page reports the parse error.
		_ = yaml.Unmarshal(raw, &f)
	}
	out := map[string]string{}
	for _, sp := range executorSpecs {
		out[sp.name] = sp.binary
		if b := strings.TrimSpace(f[sp.key].Binary); b != "" {
			out[sp.name] = b
		}
	}
	return out
}

// usedBy maps each executor to the agents that run it ("repo: agent"). A
// langgraph agent's code_with counts as a use of that executor too.
func (s *Server) usedBy() map[string][]string {
	out := map[string][]string{}
	if _, err := os.Stat(s.o.ConfigPath); err != nil {
		return out
	}
	cfg, err := config.LoadUnvalidated(s.o.ConfigPath)
	if err != nil {
		return out
	}
	for _, r := range cfg.Repos {
		repo := r.Name
		if repo == "" {
			repo = filepath.Base(r.Path)
		}
		for _, a := range r.Agents {
			ex := a.Executor
			if ex == "" {
				ex = "claude"
			}
			out[ex] = append(out[ex], repo+": "+a.Name)
			if ex == "langgraph" && a.CodeWith != "" && a.CodeWith != "langgraph" {
				out[a.CodeWith] = append(out[a.CodeWith], repo+": "+a.Name+" (code_with)")
			}
		}
	}
	return out
}

func (s *Server) home() string {
	if s.o.Getenv != nil {
		return s.o.Getenv("HOME")
	}
	return os.Getenv("HOME")
}

// status runs `<binary> --version`. A bare name missing from PATH is also
// looked for in ~/.local/bin, where the curl installers put their binaries,
// so the page can say to fix PATH or the config instead of reinstalling.
func (s *Server) status(ctx context.Context, sp executorSpec, binary string, used []string) executorStatus {
	st := executorStatus{
		Name: sp.name, Label: sp.label, Binary: binary, ConfigKey: sp.key + ".binary",
		Install: sp.install, Docs: sp.docs, Note: sp.note, UsedBy: used,
	}
	if st.UsedBy == nil {
		st.UsedBy = []string{}
	}
	run := s.runner()
	try := func(bin string) (bool, error) {
		cctx, cancel := context.WithTimeout(ctx, versionTimeout)
		defer cancel()
		out, code, err := run(cctx, bin, "--version")
		if err != nil {
			return false, err
		}
		if code != 0 {
			st.Problem = fmt.Sprintf("%s --version failed (exit %d): %s", bin, code, firstLine(out))
			return false, nil
		}
		st.Installed, st.Version, st.Path = true, firstLine(out), bin
		return true, nil
	}
	ok, err := try(binary)
	switch {
	case ok:
		st.OnPath = !strings.ContainsRune(binary, '/')
		if st.OnPath {
			if p, err := exec.LookPath(binary); err == nil {
				st.Path = p
			}
		}
		return st
	case err == nil: // ran, but failed
		return st
	case !errors.Is(err, exec.ErrNotFound) && !errors.Is(err, os.ErrNotExist):
		st.Problem = err.Error()
		return st
	}
	if strings.ContainsRune(binary, '/') {
		return st
	}
	if home := s.home(); home != "" {
		local := filepath.Join(home, ".local", "bin", binary)
		if fi, err := os.Stat(local); err == nil && !fi.IsDir() {
			if ok, _ := try(local); ok {
				st.Problem = fmt.Sprintf("installed at %s, which is not on the worker's PATH: add ~/.local/bin to PATH or set %s to that path", local, st.ConfigKey)
			}
		}
	}
	return st
}

// firstLine is the first non-empty line of out, trimmed and capped.
func firstLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > maxVersionLen {
				l = l[:maxVersionLen] + "…"
			}
			return l
		}
	}
	return ""
}

func (s *Server) executors(w http.ResponseWriter, r *http.Request) {
	bins, used := s.binaries(), s.usedBy()
	out := make([]executorStatus, len(executorSpecs))
	var wg sync.WaitGroup
	for i, sp := range executorSpecs {
		sp, _ = s.spec(sp.name)
		wg.Go(func() { out[i] = s.status(r.Context(), sp, bins[sp.name], used[sp.name]) })
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"executors": out})
}

func (s *Server) installExecutor(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sp, ok := s.spec(in.Name)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("no executor %q to install", in.Name))
		return
	}
	if sp.install == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s has no automatic install; see %s", sp.label, sp.docs))
		return
	}
	if !s.installing.TryLock() {
		writeError(w, http.StatusConflict, errors.New("another install is running; wait for it to finish"))
		return
	}
	defer s.installing.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), installTimeout)
	defer cancel()
	out, code, err := s.runner()(ctx, "sh", "-c", sp.install)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	st := s.status(r.Context(), sp, s.binaries()[sp.name], s.usedBy()[sp.name])
	writeJSON(w, http.StatusOK, map[string]any{"command": sp.install, "exit_code": code, "output": out, "executor": st})
}
