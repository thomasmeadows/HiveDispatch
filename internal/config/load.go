package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where Load looks when no path is given.
func DefaultPath() string {
	return filepath.Join(homeDir(), ".config", "hivedispatch", "config.yaml")
}

// Load reads the worker config, resolves and reads every enrolled
// repository, and validates the lot.
func Load(path string) (*Config, error) {
	c, err := readWorker(path)
	if err != nil {
		return nil, err
	}
	c.resolveRepos(context.Background())
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// LoadUnvalidated reads the worker config and resolves repositories
// without validating either, for tools that inspect a half-written setup.
func LoadUnvalidated(path string) (*Config, error) {
	c, err := readWorker(path)
	if err != nil {
		return nil, err
	}
	c.resolveRepos(context.Background())
	return c, nil
}

// LoadWorker reads and validates only the worker config, without looking
// at any repository — for commands that must work before one is enrolled.
func LoadWorker(path string) (*Config, error) {
	c, err := readWorker(path)
	if err != nil {
		return nil, err
	}
	if err := problemsError(c.workerProblems()); err != nil {
		return nil, err
	}
	return c, nil
}

// readWorker parses the worker config, fills secrets from the environment,
// expands paths, and applies defaults.
func readWorker(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := checkLegacy(raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.Jira.Token = os.Getenv("HIVE_JIRA_TOKEN")
	c.GitHub.Token = os.Getenv("HIVE_GITHUB_TOKEN")
	c.Workroot = expandHome(c.Workroot)
	if c.Workroot == "" {
		c.Workroot = filepath.Join(homeDir(), ".local", "share", "hivedispatch")
	}
	for i, d := range c.CodeDirs {
		c.CodeDirs[i] = absPath(expandHome(d))
	}
	c.applyDefaults()
	return &c, nil
}

// checkLegacy rejects a worker config written for the single-tracker layout,
// naming every key that has moved into a repository's repo.yaml.
func checkLegacy(raw []byte) error {
	var m struct {
		Tracker any              `yaml:"tracker"`
		Jira    map[string]any   `yaml:"jira"`
		GitHub  map[string]any   `yaml:"github"`
		Repos   []map[string]any `yaml:"repos"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil // reported by the real parse
	}
	var keys []string
	if m.Tracker != nil {
		keys = append(keys, "tracker")
	}
	for _, k := range []string{"base_url", "jql", "fields", "statuses"} {
		if _, ok := m.Jira[k]; ok {
			keys = append(keys, "jira."+k)
		}
	}
	for _, k := range []string{"labels", "project"} {
		if _, ok := m.GitHub[k]; ok {
			keys = append(keys, "github."+k)
		}
	}
	for i, r := range m.Repos {
		var rk []string
		for k := range r {
			if k != "path" {
				rk = append(rk, fmt.Sprintf("repos[%d].%s", i, k))
			}
		}
		sort.Strings(rk)
		keys = append(keys, rk...)
	}
	if len(keys) == 0 {
		return nil
	}
	return errors.New("this config uses the old single-tracker layout (" + strings.Join(keys, ", ") + ").\n" +
		"Tracker settings now live in each repository's .hive-dispatch/repo.yaml (project, tracker, jira.base_url/jql/fields/statuses, github.labels/project);\n" +
		"the worker config keeps agent_id, jira.email, and where to find repositories (code_dirs, repos: - path:).\n" +
		"Run `hivedispatch init -github` (or -jira) inside each repository, then remove the moved keys. See docs/config.md.")
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), strings.TrimPrefix(p, "~"))
	}
	return p
}
