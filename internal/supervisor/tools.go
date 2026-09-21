package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thomasmeadows/hivedispatch/docs"
	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

const noArgs = `{"type":"object","properties":{}}`

func decode(args json.RawMessage, into any) error {
	if len(args) == 0 {
		return nil
	}
	if err := json.Unmarshal(args, into); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// --- read_config ---

type readConfig struct{ path string }

// NewReadConfig returns the tool that reads the worker config.
func NewReadConfig(path string) Tool { return readConfig{path: path} }

// Def describes the read_config tool.
func (r readConfig) Def() model.ToolDef {
	return model.ToolDef{Name: "read_config", Description: "Read the worker config file (" + r.path + "). Returns its YAML, or says it is missing.", Schema: []byte(noArgs)}
}

// Call reads the worker config file, or says it is missing.
func (r readConfig) Call(context.Context, json.RawMessage) (string, error) {
	raw, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return "no config at " + r.path + " (write_config creates it)", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// --- write_config ---

type writeConfig struct {
	path    string
	confirm func(string) bool
}

// NewWriteConfig returns the tool that replaces the worker config after
// the operator confirms the diff.
func NewWriteConfig(path string, confirm func(string) bool) Tool {
	return writeConfig{path: path, confirm: confirm}
}

// Def describes the write_config tool.
func (w writeConfig) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "write_config",
		Description: "Replace the whole worker config with new YAML. The operator sees a diff and must approve. Unparseable YAML is refused; a config that parses but is incomplete is written and the remaining problems are returned so you can tell the operator.",
		Schema:      []byte(`{"type":"object","properties":{"content":{"type":"string","description":"the complete new config.yaml"}},"required":["content"]}`),
	}
}

// Call validates content as the new worker config, shows the operator a
// diff (and any validation problems) to confirm, then writes it.
func (w writeConfig) Call(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Content string `json:"content"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Content) == "" {
		return "", errors.New("content is empty")
	}
	var probe map[string]any
	if err := yaml.Unmarshal([]byte(in.Content), &probe); err != nil {
		return "", fmt.Errorf("not valid YAML, nothing written: %w", err)
	}
	old, err := os.ReadFile(w.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return "", err
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(in.Content), 0o600); err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp) }()
	problems := ""
	if _, err := config.Load(tmp); err != nil {
		problems = strings.ReplaceAll(err.Error(), tmp, w.path)
	}
	prompt := Diff(filepath.Base(w.path), string(old), in.Content)
	if problems != "" {
		prompt += "\nThe config still has problems:\n" + problems + "\n"
	}
	prompt += "\nApply to " + w.path + "?"
	if !w.confirm(prompt) {
		return "declined by user; the config is unchanged", nil
	}
	if old != nil {
		if err := os.WriteFile(w.path+".bak", old, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, w.path); err != nil {
		return "", err
	}
	out := "wrote " + w.path
	if problems != "" {
		out += "\n\nIt is not complete yet:\n" + problems
	}
	return out, nil
}

// --- read_doc ---

var docFiles = map[string]string{
	"setup": "setup.md", "config": "config.md", "design": "design-spec.md", "decisions": "decisions.md",
}

const docNames = "setup, config, design, decisions"

type readDoc struct{}

// NewReadDoc returns the tool that reads the embedded operator docs.
func NewReadDoc() Tool { return readDoc{} }

// Def describes the read_doc tool.
func (readDoc) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "read_doc",
		Description: "Read one of HiveDispatch's docs: setup (step-by-step setup for Jira and GitHub Issues), config (every config key), design (architecture), decisions (design decisions and why).",
		Schema:      []byte(`{"type":"object","properties":{"name":{"type":"string","enum":["setup","config","design","decisions"]}},"required":["name"]}`),
	}
}

// Call reads the named embedded doc.
func (readDoc) Call(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	file, ok := docFiles[in.Name]
	if !ok {
		return "", fmt.Errorf("unknown doc %q: want one of %s", in.Name, docNames)
	}
	raw, err := docs.FS.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// --- read_repo_file ---

type readRepoFile struct{ workerConfigPath string }

// NewReadRepoFile returns the tool that reads a governed repo's policy
// file or AGENTS.md from its base checkout under workroot.
func NewReadRepoFile(workerConfigPath string) Tool {
	return readRepoFile{workerConfigPath: workerConfigPath}
}

// Def describes the read_repo_file tool.
func (readRepoFile) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "read_repo_file",
		Description: "Read .hivedispatch.yaml (the repo policy the executor obeys) or AGENTS.md from a configured repository's checkout. The checkout exists only after a run has cloned it.",
		Schema:      []byte(`{"type":"object","properties":{"repo":{"type":"string","description":"owner/repo as in repos[].name"},"name":{"type":"string","enum":[".hivedispatch.yaml","AGENTS.md"]}},"required":["repo","name"]}`),
	}
}

// workerRepos reads only workroot and repos[].name from the worker config,
// without validating the rest, so this works on a half-written config.
func workerRepos(path string) (workroot string, names []string, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, errors.New("no config at " + path)
	}
	if err != nil {
		return "", nil, err
	}
	var c struct {
		Workroot string `yaml:"workroot"`
		Repos    []struct {
			Name string `yaml:"name"`
		} `yaml:"repos"`
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return "", nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, r := range c.Repos {
		names = append(names, r.Name)
	}
	workroot = c.Workroot
	if home := os.Getenv("HOME"); strings.HasPrefix(workroot, "~/") && home != "" {
		workroot = filepath.Join(home, workroot[2:])
	}
	if workroot == "" {
		workroot = filepath.Join(os.Getenv("HOME"), ".local", "share", "hivedispatch")
	}
	return workroot, names, nil
}

// Call reads name (.hivedispatch.yaml or AGENTS.md) from repo's checkout.
func (r readRepoFile) Call(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Repo string `json:"repo"`
		Name string `json:"name"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	if in.Name != ".hivedispatch.yaml" && in.Name != "AGENTS.md" {
		return "", fmt.Errorf("name must be .hivedispatch.yaml or AGENTS.md, got %q", in.Name)
	}
	workroot, names, err := workerRepos(r.workerConfigPath)
	if err != nil {
		return "", err
	}
	known := false
	for _, n := range names {
		if strings.EqualFold(n, in.Repo) {
			known = true
		}
	}
	if !known {
		return "", fmt.Errorf("%q is not in repos[] of %s (configured: %s)", in.Repo, r.workerConfigPath, strings.Join(names, ", "))
	}
	base := filepath.Join(workroot, "repos", strings.ReplaceAll(in.Repo, "/", "__"), "repo")
	raw, err := os.ReadFile(filepath.Join(base, in.Name))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no %s in %s (the checkout appears after the first run clones the repo)", in.Name, base)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
