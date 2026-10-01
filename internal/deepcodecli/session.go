// Package deepcodecli runs the DeepCode CLI headless (`deepcode -x -p`) and
// reads what it did from the session it saves under ~/.deepcode/projects.
// DeepCode prints only its final reply, so the session file is where the
// steps, edits and token usage come from.
package deepcodecli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

// Transcript is what a run's session says it did.
type Transcript struct {
	SessionID   string
	Steps       []executor.Step
	EditedFiles []string // relative to the workspace
	ToolCalls   int
	Usage       executor.Usage
	Status      string // the session's: completed, failed, ask_permission, waiting_for_user, ...
	FailReason  string
	Reply       string // the last assistant reply
}

// editingTools are DeepCode tools whose file_path argument is a file they change.
var editingTools = map[string]bool{"edit": true, "write": true, "multi_edit": true, "multiedit": true}

type message struct {
	SessionID     string `json:"sessionId"`
	Role          string `json:"role"`
	Content       string `json:"content"`
	CreateTime    string `json:"createTime"`
	MessageParams *struct {
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
		ToolCallID string `json:"tool_call_id"`
	} `json:"messageParams"`
}

type toolResult struct {
	OK       bool   `json:"ok"`
	Output   string `json:"output"`
	Error    string `json:"error"`
	Metadata *struct {
		ExitCode *int `json:"exitCode"`
	} `json:"metadata"`
}

type indexEntry struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	FailReason     string `json:"failReason"`
	AssistantReply string `json:"assistantReply"`
	Usage          *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	UsagePerModel map[string]json.RawMessage `json:"usagePerModel"`
}

func readMessages(file string) ([]message, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out, sc.Err()
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s) // zero for a missing or bad time
	return t
}

// ParseSession reads a session file and its sibling sessions-index.json.
// cwd is the workspace the run used, for relative edited paths.
func ParseSession(file, cwd string) (Transcript, error) {
	msgs, err := readMessages(file)
	if err != nil {
		return Transcript{}, err
	}
	tr := Transcript{SessionID: strings.TrimSuffix(filepath.Base(file), ".jsonl")}
	pending := map[string]int{}    // tool call id -> index in tr.Steps
	editing := map[string]string{} // tool call id -> file it edits
	edited := map[string]bool{}
	for _, m := range msgs {
		at := parseTime(m.CreateTime)
		switch m.Role {
		case "assistant":
			if m.MessageParams == nil || len(m.MessageParams.ToolCalls) == 0 {
				if strings.TrimSpace(m.Content) != "" {
					tr.Steps = appendStep(tr.Steps, executor.Step{Kind: executor.StepMessage, Name: "assistant", Output: executor.CapOutput(m.Content), Start: at, End: at})
				}
				continue
			}
			for _, tc := range m.MessageParams.ToolCalls {
				tr.ToolCalls++
				if len(tr.Steps) < executor.MaxSteps {
					tr.Steps = append(tr.Steps, executor.Step{Kind: executor.StepTool, Name: tc.Function.Name, Input: executor.CapOutput(tc.Function.Arguments), Start: at})
					pending[tc.ID] = len(tr.Steps) - 1
				}
				if editingTools[tc.Function.Name] {
					var args struct {
						FilePath string `json:"file_path"`
					}
					if json.Unmarshal([]byte(tc.Function.Arguments), &args) == nil && args.FilePath != "" {
						editing[tc.ID] = args.FilePath
					}
				}
			}
		case "tool":
			if m.MessageParams == nil {
				continue
			}
			id := m.MessageParams.ToolCallID
			var r toolResult
			ok := json.Unmarshal([]byte(m.Content), &r) == nil
			failed := !ok || !r.OK || (r.Metadata != nil && r.Metadata.ExitCode != nil && *r.Metadata.ExitCode != 0)
			if i, found := pending[id]; found {
				st := &tr.Steps[i]
				st.End = at
				st.IsError = failed
				out := r.Output
				if r.Error != "" {
					out = r.Error
				}
				if !ok {
					out = m.Content
				}
				st.Output = executor.CapOutput(out)
				delete(pending, id)
			}
			if path, isEdit := editing[id]; isEdit && !failed {
				rel := relative(cwd, path)
				if !edited[rel] {
					edited[rel] = true
					tr.EditedFiles = append(tr.EditedFiles, rel)
				}
			}
		}
	}
	readIndex(filepath.Join(filepath.Dir(file), "sessions-index.json"), &tr)
	return tr, nil
}

func appendStep(steps []executor.Step, st executor.Step) []executor.Step {
	if len(steps) >= executor.MaxSteps {
		return steps
	}
	return append(steps, st)
}

func relative(cwd, path string) string {
	if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return rel
	}
	return path
}

// readIndex fills the session's status, reply and usage from its entry in
// sessions-index.json, when there is one.
func readIndex(path string, tr *Transcript) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var idx struct {
		Entries []indexEntry `json:"entries"`
	}
	if json.Unmarshal(raw, &idx) != nil {
		return
	}
	for _, e := range idx.Entries {
		if e.ID != tr.SessionID {
			continue
		}
		tr.Status, tr.FailReason, tr.Reply = e.Status, e.FailReason, e.AssistantReply
		if e.Usage != nil {
			tr.Usage.InputTokens, tr.Usage.OutputTokens = e.Usage.PromptTokens, e.Usage.CompletionTokens
		}
		models := make([]string, 0, len(e.UsagePerModel))
		for name := range e.UsagePerModel {
			models = append(models, name)
		}
		sort.Strings(models)
		tr.Usage.Model = strings.Join(models, ",")
		return
	}
}

// CountToolCalls counts the tool calls a session has made so far, for the
// step budget while the run is going.
func CountToolCalls(file string) int {
	msgs, err := readMessages(file)
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range msgs {
		if m.Role == "assistant" && m.MessageParams != nil {
			n += len(m.MessageParams.ToolCalls)
		}
	}
	return n
}

var rootPathRe = regexp.MustCompile(`"root path":\s*"((?:[^"\\]|\\.)*)"`)

// RootPath is the workspace a session ran in, as DeepCode recorded it.
func RootPath(file string) string {
	msgs, err := readMessages(file)
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		if m.Role != "system" {
			continue
		}
		if sm := rootPathRe.FindStringSubmatch(m.Content); sm != nil {
			var s string
			if json.Unmarshal([]byte(`"`+sm[1]+`"`), &s) == nil {
				return s
			}
		}
	}
	return ""
}

// Sessions lists every session file under home (~/.deepcode).
func Sessions(home string) map[string]bool {
	files, _ := filepath.Glob(filepath.Join(home, "projects", "*", "*.jsonl")) // only a bad pattern errors
	out := make(map[string]bool, len(files))
	for _, f := range files {
		out[f] = true
	}
	return out
}

// FindSession returns the session file of a run in workspace: the resumed
// session's file, or a session that did not exist before the run (before)
// and was recorded for this workspace. "" when there is none yet.
func FindSession(home, workspace, resume string, before map[string]bool) string {
	if resume != "" {
		files, _ := filepath.Glob(filepath.Join(home, "projects", "*", resume+".jsonl")) // only a bad pattern errors
		if len(files) > 0 {
			return files[0]
		}
		return ""
	}
	want := filepath.Clean(workspace)
	for f := range Sessions(home) {
		if before[f] {
			continue
		}
		if filepath.Clean(RootPath(f)) == want {
			return f
		}
	}
	return ""
}

// LooksLikeBudget reports whether a failure reads as a quota or rate limit.
func LooksLikeBudget(reason string) bool {
	low := strings.ToLower(reason)
	for _, needle := range []string{"429", "rate limit", "too many requests", "quota", "insufficient balance", "402"} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}
