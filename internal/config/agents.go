package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentsFileName lists a repository's agents, inside RepoDir.
const AgentsFileName = "agents.yaml"

// Agent is one coding agent that works a repository's tickets. A
// repository's agents form a pool: each works one ticket at a time, and a
// ticket labelled hive:agent:<name> waits for that agent. Every agent follows
// the repository's policy.yaml; Model overrides the policy's model.
type Agent struct {
	Name     string `yaml:"name" json:"name"`
	Executor string `yaml:"executor" json:"executor"`     // claude (default), codex or fake
	Model    string `yaml:"model,omitempty" json:"model"` // default: the policy's model, then the CLI's
}

// DefaultAgents is the pool of a repository without an agents.yaml.
func DefaultAgents() []Agent { return []Agent{{Name: "default", Executor: "claude"}} }

// PinLabelPrefix is the label prefix that, followed by an agent's name, pins a ticket to that agent.
const PinLabelPrefix = "hive:agent:"

var agentNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// agentsFile is the schema of agents.yaml, decoded strictly.
type agentsFile struct {
	Agents []Agent `yaml:"agents"`
}

// ParseAgents decodes and validates the content of an agents.yaml.
func ParseAgents(raw []byte) ([]Agent, error) {
	var f agentsFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(f.Agents) == 0 {
		return nil, errors.New("list at least one agent under agents:, or delete the file to use one default Claude agent")
	}
	seen := map[string]bool{}
	var problems []string
	for i := range f.Agents {
		a := &f.Agents[i]
		a.Name = strings.TrimSpace(a.Name)
		if a.Executor == "" {
			a.Executor = "claude"
		}
		switch {
		case !agentNameRe.MatchString(a.Name):
			problems = append(problems, fmt.Sprintf("agents[%d].name %q: use letters, digits, '.', '-' or '_' (it is used in the label %s<name>)", i, a.Name, PinLabelPrefix))
		case seen[strings.ToLower(a.Name)]:
			problems = append(problems, fmt.Sprintf("agents[%d].name %q is used twice", i, a.Name))
		}
		seen[strings.ToLower(a.Name)] = true
		if a.Executor != "claude" && a.Executor != "codex" && a.Executor != "fake" {
			problems = append(problems, fmt.Sprintf("agents[%d].executor: want claude, codex or fake, got %q", i, a.Executor))
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return f.Agents, nil
}

// agentsFilePath is dir/.hive-dispatch/agents.yaml.
func agentsFilePath(dir string) string { return filepath.Join(dir, RepoDir, AgentsFileName) }

// readAgents reads dir's agents.yaml, or returns DefaultAgents without one.
func readAgents(dir string) ([]Agent, error) {
	file := agentsFilePath(dir)
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultAgents(), nil
	}
	if err != nil {
		return nil, err
	}
	agents, err := ParseAgents(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return agents, nil
}

// checkMovedExecutor rejects the worker-level executor settings that now
// belong to each repository's agents.
func checkMovedExecutor(raw []byte) error {
	var m struct {
		Executor any            `yaml:"executor"`
		Claude   map[string]any `yaml:"claude"`
		Codex    map[string]any `yaml:"codex"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil // reported by the real parse
	}
	var keys []string
	if m.Executor != nil {
		keys = append(keys, "executor")
	}
	if _, ok := m.Claude["model"]; ok {
		keys = append(keys, "claude.model")
	}
	if _, ok := m.Codex["model"]; ok {
		keys = append(keys, "codex.model")
	}
	if len(keys) == 0 {
		return nil
	}
	return fmt.Errorf("%s moved into each repository's %s/%s (one entry per agent: name, executor, model) — remove them here; claude.binary and codex.binary stay", strings.Join(keys, ", "), RepoDir, AgentsFileName)
}
