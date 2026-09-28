package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/docs"
	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
	"github.com/thomasmeadows/hivedispatch/internal/yamlfile"
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
func (w writeConfig) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Content string `json:"content"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	staged, err := yamlfile.Stage(w.path, []byte(in.Content), func(tmp string) error {
		_, err := config.Load(tmp)
		return err
	})
	if err != nil {
		return "", err
	}
	prompt := staged.Diff
	if staged.Problems != "" {
		prompt += "\nThe config still has problems:\n" + staged.Problems + "\n"
	}
	prompt += "\nApply to " + w.path + "?"
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if !w.confirm(prompt) {
		return "declined by user; the config is unchanged", nil
	}
	if err := staged.Commit(); err != nil {
		return "", err
	}
	out := "wrote " + w.path
	if staged.Problems != "" {
		out += "\n\nIt is not complete yet:\n" + staged.Problems
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

// NewReadRepoFile returns the tool that reads an enrolled repository's
// .hive-dispatch files or AGENTS.md from its local checkout.
func NewReadRepoFile(workerConfigPath string) Tool {
	return readRepoFile{workerConfigPath: workerConfigPath}
}

// repoFileNames are the files read_repo_file may read, relative to the
// repository root.
var repoFileNames = []string{".hive-dispatch/repo.yaml", ".hive-dispatch/policy.yaml", "AGENTS.md"}

// Def describes the read_repo_file tool.
func (readRepoFile) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "read_repo_file",
		Description: "Read a file from an enrolled repository's local checkout: .hive-dispatch/repo.yaml (its project key, tracker and tracker settings), .hive-dispatch/policy.yaml (the agent policy the executor obeys; the worker uses the committed copy) or AGENTS.md.",
		Schema:      []byte(`{"type":"object","properties":{"repo":{"type":"string","description":"owner/repo, or the repository's project key"},"name":{"type":"string","enum":[".hive-dispatch/repo.yaml",".hive-dispatch/policy.yaml","AGENTS.md"]}},"required":["repo","name"]}`),
	}
}

// Call reads name from repo's local checkout.
func (r readRepoFile) Call(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Repo string `json:"repo"`
		Name string `json:"name"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	if !slices.Contains(repoFileNames, in.Name) {
		return "", fmt.Errorf("name must be one of %s, got %q", strings.Join(repoFileNames, ", "), in.Name)
	}
	// Unvalidated, so this works on a half-written config.
	cfg, err := config.LoadUnvalidated(r.workerConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("no config at " + r.workerConfigPath)
	}
	if err != nil {
		return "", err
	}
	var known []string
	dir := ""
	for _, repo := range cfg.Repos {
		known = append(known, repo.Name+" ("+repo.Project+")")
		if strings.EqualFold(repo.Name, in.Repo) || strings.EqualFold(repo.Project, in.Repo) {
			dir = repo.Path
		}
	}
	if dir == "" {
		return "", fmt.Errorf("%q is not an enrolled repository (enrolled: %s); `scan` lists what is found", in.Repo, strings.Join(known, ", "))
	}
	p := filepath.Join(dir, filepath.FromSlash(in.Name))
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no %s", p)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// --- remember ---

type remember struct {
	mem *Memory
	now func() time.Time
}

// NewRemember returns the tool that appends to the notes file.
func NewRemember(mem *Memory, now func() time.Time) Tool { return remember{mem: mem, now: now} }

// Def describes the remember tool.
func (remember) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "remember",
		Description: "Append a short note to your persistent memory, shown to you at the start of every session. Use it for facts about this operator's setup that will matter next time (which tracker, where the token comes from, what was fixed).",
		Schema:      []byte(`{"type":"object","properties":{"note":{"type":"string"}},"required":["note"]}`),
	}
}

// Call appends a note to the persistent memory file.
func (r remember) Call(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Note string `json:"note"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Note) == "" {
		return "", errors.New("note is empty")
	}
	if err := r.mem.Append(r.now(), in.Note); err != nil {
		return "", err
	}
	return "remembered in " + r.mem.NotesPath(), nil
}
