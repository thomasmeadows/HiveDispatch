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
	Role     string `yaml:"role,omitempty" json:"role"`   // planning, coding (default) or review
	Executor string `yaml:"executor" json:"executor"`     // claude (default), codex, grok, antigravity, deepcode, langgraph or fake
	Model    string `yaml:"model,omitempty" json:"model"` // default: the policy's model, then the CLI's
	// CodeWith is langgraph only: what its code node runs — claude, codex,
	// deepcode or fake, or langgraph for HiveDispatch's own coding agent.
	CodeWith string `yaml:"code_with,omitempty" json:"code_with,omitempty"`
}

// Agent roles: which board column an agent works.
const (
	RolePlanning = "planning" // Planning: posts a plan, moves the ticket to Ready
	RoleCoding   = "coding"   // Ready: implements the ticket and opens a pull request
	RoleReview   = "review"   // In Review: reviews the pull request, sends it back or passes it
)

// DefaultAgents is the pool of a repository without an agents.yaml.
func DefaultAgents() []Agent { return []Agent{{Name: "default", Role: RoleCoding, Executor: "claude"}} }

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
		if a.Role == "" {
			a.Role = RoleCoding
		}
		if a.Role != RolePlanning && a.Role != RoleCoding && a.Role != RoleReview {
			problems = append(problems, fmt.Sprintf("agents[%d].role: want planning, coding or review, got %q", i, a.Role))
		}
		switch {
		case !agentNameRe.MatchString(a.Name):
			problems = append(problems, fmt.Sprintf("agents[%d].name %q: use letters, digits, '.', '-' or '_' (it is used in the label %s<name>)", i, a.Name, PinLabelPrefix))
		case seen[strings.ToLower(a.Name)]:
			problems = append(problems, fmt.Sprintf("agents[%d].name %q is used twice", i, a.Name))
		}
		seen[strings.ToLower(a.Name)] = true
		switch a.Executor {
		case "claude", "codex", "fake":
			if a.CodeWith != "" {
				problems = append(problems, fmt.Sprintf("agents[%d].code_with: only an executor: langgraph agent has one", i))
			}
		case "grok", "antigravity":
			if a.Role != RoleCoding {
				problems = append(problems, fmt.Sprintf("agents[%d]: %s is for coding agents only", i, a.Executor))
			}
			if a.CodeWith != "" {
				problems = append(problems, fmt.Sprintf("agents[%d].code_with: only an executor: langgraph agent has one", i))
			}
		case "deepcode":
			if a.CodeWith != "" {
				problems = append(problems, fmt.Sprintf("agents[%d].code_with: only an executor: langgraph agent has one", i))
			}
			problems = append(problems, deepcodeProblems(i, "executor", *a)...)
		case "langgraph":
			if a.CodeWith == "" {
				a.CodeWith = "claude"
			}
			switch a.CodeWith {
			case "claude", "codex", "fake":
			case "langgraph":
				if a.Role != RoleCoding {
					problems = append(problems, fmt.Sprintf("agents[%d].code_with: langgraph is for coding agents only; a %s agent runs read-only through claude or codex", i, a.Role))
				}
			case "grok", "antigravity":
				if a.Role != RoleCoding {
					problems = append(problems, fmt.Sprintf("agents[%d]: %s is for coding agents only", i, a.CodeWith))
				}
			case "deepcode":
				problems = append(problems, deepcodeProblems(i, "code_with", *a)...)
			default:
				problems = append(problems, fmt.Sprintf("agents[%d].code_with: want claude, codex, grok, antigravity, deepcode, langgraph or fake, got %q", i, a.CodeWith))
			}
		default:
			problems = append(problems, fmt.Sprintf("agents[%d].executor: want claude, codex, grok, antigravity, deepcode, langgraph or fake, got %q", i, a.Executor))
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return f.Agents, nil
}

// deepcodeProblems checks an agent that runs DeepCode, as its executor or
// its code_with. DeepCode cannot be held to read-only, so it only codes,
// and it has no model flag: its model is in ~/.deepcode/settings.json.
func deepcodeProblems(i int, field string, a Agent) []string {
	var problems []string
	if a.Role != RoleCoding {
		problems = append(problems, fmt.Sprintf("agents[%d].%s: deepcode is for coding agents only; a %s agent runs read-only through claude or codex", i, field, a.Role))
	}
	if a.Model != "" {
		problems = append(problems, fmt.Sprintf("agents[%d].model: deepcode takes its model from ~/.deepcode/settings.json; remove model here", i))
	}
	return problems
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

// checkRenamed rejects keys that were renamed.
func checkRenamed(raw []byte) error {
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil // reported by the real parse
	}
	if _, ok := m["agent_id"]; ok {
		return errors.New("agent_id was renamed machine_id: rename it, or delete it to use this machine's hostname")
	}
	return nil
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
