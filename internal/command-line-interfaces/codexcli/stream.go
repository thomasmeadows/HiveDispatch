// Package codexcli runs the Codex CLI headless (`codex exec --json`) and
// parses its JSONL event stream. Process supervision (process-group kill,
// step budget, timeout, bounded logs) mirrors claudecli; the two CLIs share
// no wire format, so they share no parser.
package codexcli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

// LooksLikeBudget reports whether a finished transcript stopped for quota or
// rate-limit reasons. Codex reports these only as error text, so this is a
// text match; the reset time is never carried on the stream.
func LooksLikeBudget(tr Transcript) bool {
	low := strings.ToLower(tr.Error)
	for _, needle := range []string{"usage limit", "rate limit", "insufficient_quota", "too many requests", "429"} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

// Transcript is what the parser learned from a run.
type Transcript struct {
	ThreadID      string   // from thread.started; the resume token
	ToolUses      int      // commands, file changes, MCP calls and web searches
	EditedFiles   []string // from file_change items, relative to the workspace
	LastMessage   string   // text of the last agent_message
	TurnCompleted bool     // a turn.completed event was seen
	Error         string   // message of the last turn.failed or error event
	Lines         int
	InputTokens   int             // summed over turn.completed events
	OutputTokens  int             // summed over turn.completed events
	Steps         []executor.Step // tool items and messages, in order
}

// Parser consumes `codex exec --json` lines and accumulates a Transcript.
type Parser struct {
	cwd       string // the workspace we ran in
	onToolUse func(count int)
	t         Transcript
	edited    map[string]bool
	counted   map[string]bool // item ids already counted as tool uses
	pending   map[string]int  // item id -> index in t.Steps, until it completes

	// Now times steps as their lines arrive; nil = time.Now.
	Now func() time.Time
}

// NewParser returns a parser that relativises changed paths against cwd and
// calls onToolUse with the running count after every tool-like item.
func NewParser(cwd string, onToolUse func(int)) *Parser {
	return &Parser{cwd: cwd, onToolUse: onToolUse, edited: map[string]bool{}, counted: map[string]bool{}, pending: map[string]int{}}
}

// toolItems are the item types that represent the agent acting on the world.
var toolItems = map[string]bool{"command_execution": true, "file_change": true, "mcp_tool_call": true, "web_search": true}

type envelope struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"` // on error events
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"` // on turn.failed
	Item  *item `json:"item"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"` // on turn.completed
}

type item struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	Changes []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`
	Status           string `json:"status"`
}

// Line consumes one line of output. Non-JSON lines are counted and ignored.
func (p *Parser) Line(raw []byte) {
	p.t.Lines++
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || raw[0] != '{' {
		return
	}
	var env envelope
	if json.Unmarshal(raw, &env) != nil {
		return
	}
	switch env.Type {
	case "thread.started":
		if env.ThreadID != "" {
			p.t.ThreadID = env.ThreadID
		}
	case "turn.completed":
		p.t.TurnCompleted = true
		if env.Usage != nil {
			p.t.InputTokens += env.Usage.InputTokens
			p.t.OutputTokens += env.Usage.OutputTokens
		}
	case "turn.failed":
		if env.Error != nil && env.Error.Message != "" {
			p.t.Error = env.Error.Message
		}
	case "error":
		if env.Message != "" {
			p.t.Error = env.Message
		}
	case "item.started", "item.updated", "item.completed":
		if env.Item == nil {
			return
		}
		it := env.Item
		for _, c := range it.Changes {
			if c.Path != "" {
				p.addEdited(c.Path)
			}
		}
		if it.Type == "agent_message" && env.Type == "item.completed" {
			p.t.LastMessage = it.Text
		}
		p.step(env.Type, it)
		if toolItems[it.Type] && !p.counted[it.ID] {
			p.counted[it.ID] = true
			p.t.ToolUses++
			if p.onToolUse != nil {
				p.onToolUse(p.t.ToolUses)
			}
		}
	}
}

// step records tool items from start to completion and messages when they
// complete.
func (p *Parser) step(event string, it *item) {
	now := p.now()
	switch {
	case toolItems[it.Type]:
		i, started := p.pending[it.ID]
		if !started {
			if event == "item.updated" {
				return
			}
			input := it.Command
			if input == "" {
				raw, _ := json.Marshal(it)
				input = string(raw)
			}
			i = p.addStep(executor.Step{Kind: executor.StepTool, Name: it.Type, Input: executor.CapOutput(input), Start: now})
			if i < 0 {
				return
			}
			p.pending[it.ID] = i
		}
		if event != "item.completed" {
			return
		}
		delete(p.pending, it.ID)
		st := &p.t.Steps[i]
		st.End = now
		st.Output, st.IsError = p.itemResult(it)
		st.Output = executor.CapOutput(st.Output)
	case event == "item.completed" && (it.Type == "agent_message" || it.Type == "reasoning") && strings.TrimSpace(it.Text) != "":
		name := "assistant"
		if it.Type == "reasoning" {
			name = "reasoning"
		}
		p.addStep(executor.Step{Kind: executor.StepMessage, Name: name, Output: executor.CapOutput(it.Text), Start: now, End: now})
	}
}

// itemResult is what a completed tool item produced, and whether it failed.
func (p *Parser) itemResult(it *item) (string, bool) {
	failed := it.Status == "failed" || (it.ExitCode != nil && *it.ExitCode != 0)
	switch it.Type {
	case "command_execution":
		out := it.AggregatedOutput
		if it.ExitCode != nil && *it.ExitCode != 0 {
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			out += fmt.Sprintf("exit code %d", *it.ExitCode)
		}
		return out, failed
	case "file_change":
		lines := make([]string, 0, len(it.Changes))
		for _, c := range it.Changes {
			lines = append(lines, c.Kind+" "+p.rel(c.Path))
		}
		return strings.Join(lines, "\n"), failed
	}
	return it.Status, failed
}

func (p *Parser) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// addStep records st and returns its index, or -1 past executor.MaxSteps.
func (p *Parser) addStep(st executor.Step) int {
	if len(p.t.Steps) >= executor.MaxSteps {
		return -1
	}
	p.t.Steps = append(p.t.Steps, st)
	return len(p.t.Steps) - 1
}

// rel makes path relative to the workspace when it is inside it.
func (p *Parser) rel(path string) string {
	if p.cwd != "" {
		if rel, err := filepath.Rel(p.cwd, path); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return rel
		}
	}
	return path
}

func (p *Parser) addEdited(path string) {
	path = p.rel(path)
	if !p.edited[path] {
		p.edited[path] = true
		p.t.EditedFiles = append(p.t.EditedFiles, path)
	}
}

// Transcript returns what has been parsed so far.
func (p *Parser) Transcript() Transcript {
	return p.t
}
