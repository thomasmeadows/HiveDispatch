// Package repoconfig reads .hivedispatch.yaml from a governed repository.
// Per-repo policy lives in the repo so changes go through the same review
// as code.
package repoconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the config file at the repo root.
const FileName = ".hivedispatch.yaml"

// Config is the per-repo policy.
type Config struct {
	Executor ExecutorConfig `yaml:"executor"`
	Guidance string         `yaml:"guidance"`
}

// ExecutorConfig controls how the coding agent is invoked.
type ExecutorConfig struct {
	Model          string   `yaml:"model"`
	PermissionMode string   `yaml:"permission_mode"`
	Tools          []string `yaml:"tools"`
	AllowedTools   []string `yaml:"allowed_tools"`
	MaxBudgetUSD   float64  `yaml:"max_budget_usd"`
	// Path lists directories prepended to PATH for the agent, so tools the
	// policy allows by name (e.g. golangci-lint) resolve. ~ and $VAR expand.
	Path []string `yaml:"path"`
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

// Load reads dir/.hivedispatch.yaml; a missing file yields defaults.
func Load(dir string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if err == nil {
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("%s: %w", FileName, err)
		}
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
	return c, nil
}
