// Package repoconfig reads the agent policy, .hive-dispatch/policy.yaml,
// from a governed repository. Per-repo policy lives in the repo so changes
// go through the same review as code; it is read from the ticket worktree,
// so the committed copy is the one that applies.
package repoconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Dir is the HiveDispatch folder at the repo root. It holds PolicyFile and
// the tracker settings (repo.yaml, read by package config).
const Dir = ".hive-dispatch"

// PolicyFile is the agent policy inside Dir.
const PolicyFile = "policy.yaml"

// FileName is the policy's path relative to the repo root, for messages.
const FileName = Dir + "/" + PolicyFile

// LegacyFileName is where the policy lived before Dir; it is no longer read.
const LegacyFileName = ".hivedispatch.yaml"

// Config is the per-repo policy.
type Config struct {
	Executor ExecutorConfig `yaml:"executor"`
	Guidance string         `yaml:"guidance"`
	Checks   []string       `yaml:"checks"` // langgraph runs: commands that must exit 0 after each code step
	Graph    GraphPolicy    `yaml:"-"`      // decoded in Parse, where explicit zeros are kept
}

// GraphPolicy bounds a langgraph run's loops.
type GraphPolicy struct {
	MaxFixRounds    int // code → checks → fix loops; default 3
	MaxReviewRounds int // self-review → fix loops; default 1
}

// ExecutorConfig controls how the coding agent is invoked. Model,
// PermissionMode, Tools, AllowedTools and MaxBudgetUSD are Claude Code
// vocabulary and only the claude executor reads them; Codex has its own
// section. Path applies to every executor.
type ExecutorConfig struct {
	Model          string   `yaml:"model"`
	PermissionMode string   `yaml:"permission_mode"`
	Tools          []string `yaml:"tools"`
	AllowedTools   []string `yaml:"allowed_tools"`
	MaxBudgetUSD   float64  `yaml:"max_budget_usd"`
	// Path lists directories prepended to PATH for the agent, so tools the
	// policy allows by name (e.g. golangci-lint) resolve. ~ and $VAR expand.
	Path  []string    `yaml:"path"`
	Codex CodexConfig `yaml:"codex"`
}

// CodexConfig is the per-repo policy for the codex executor.
type CodexConfig struct {
	Model   string `yaml:"model"`   // default: the worker's codex.model, then the CLI default
	Sandbox string `yaml:"sandbox"` // read-only | workspace-write (default) | danger-full-access
	Network bool   `yaml:"network"` // allow outbound network inside workspace-write
}

// ExtraPath returns Path with ~ and environment variables expanded.
func (e ExecutorConfig) ExtraPath() []string {
	var out []string
	home, _ := os.UserHomeDir()
	for _, p := range e.Path {
		p = os.ExpandEnv(p)
		if p == "~" || strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

var permissionModes = map[string]bool{"acceptEdits": true, "auto": true, "bypassPermissions": true, "manual": true, "dontAsk": true, "plan": true}

var codexSandboxes = map[string]bool{"read-only": true, "workspace-write": true, "danger-full-access": true}

// Load reads dir/.hive-dispatch/policy.yaml; a missing file yields
// defaults, unless the repo still has the legacy .hivedispatch.yaml, which
// is an error rather than silently ignored policy.
func Load(dir string) (Config, error) {
	raw, err := os.ReadFile(filepath.Join(dir, Dir, PolicyFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	if err != nil {
		if _, lerr := os.Stat(filepath.Join(dir, LegacyFileName)); lerr == nil {
			return Config{}, fmt.Errorf("%s is no longer read: move it to %s (same keys) — see docs/config.md", LegacyFileName, FileName)
		}
	}
	return Parse(raw)
}

// Parse decodes raw as a policy file (empty means all defaults), applies
// defaults and validates it.
func Parse(raw []byte) (Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", FileName, err)
	}
	if c.Executor.PermissionMode == "" {
		c.Executor.PermissionMode = "dontAsk"
	}
	if len(c.Executor.Tools) == 0 {
		c.Executor.Tools = []string{"default"}
	}
	if !permissionModes[c.Executor.PermissionMode] {
		return c, fmt.Errorf("%s: executor.permission_mode %q is not a Claude Code permission mode", FileName, c.Executor.PermissionMode)
	}
	if c.Executor.Codex.Sandbox == "" {
		c.Executor.Codex.Sandbox = "workspace-write"
	}
	if !codexSandboxes[c.Executor.Codex.Sandbox] {
		return c, fmt.Errorf("%s: executor.codex.sandbox %q is not a Codex sandbox mode (read-only, workspace-write, danger-full-access)", FileName, c.Executor.Codex.Sandbox)
	}
	var g struct {
		Graph struct {
			MaxFixRounds    *int `yaml:"max_fix_rounds"`
			MaxReviewRounds *int `yaml:"max_review_rounds"`
		} `yaml:"graph"`
	}
	if err := yaml.Unmarshal(raw, &g); err != nil {
		return c, fmt.Errorf("%s: %w", FileName, err)
	}
	c.Graph = GraphPolicy{MaxFixRounds: 3, MaxReviewRounds: 1}
	if p := g.Graph.MaxFixRounds; p != nil {
		c.Graph.MaxFixRounds = *p
	}
	if p := g.Graph.MaxReviewRounds; p != nil {
		c.Graph.MaxReviewRounds = *p
	}
	if c.Graph.MaxFixRounds < 0 || c.Graph.MaxReviewRounds < 0 {
		return c, fmt.Errorf("%s: graph.max_fix_rounds and graph.max_review_rounds must not be negative", FileName)
	}
	for i, chk := range c.Checks {
		if strings.TrimSpace(chk) == "" {
			return c, fmt.Errorf("%s: checks[%d] is empty", FileName, i)
		}
	}
	return c, nil
}
