// Package codexcli runs the Codex CLI headless (`codex exec --json`) and
// parses its JSONL event stream. Process supervision (process-group kill,
// step budget, timeout, bounded logs) mirrors claudecli; the two CLIs share
// no wire format, so they share no parser.
package codexcli

import (
	"encoding/json"
	"path/filepath"
	"strings"
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
}

// Parser consumes `codex exec --json` lines and accumulates a Transcript.
type Parser struct {
	cwd       string // the workspace we ran in
	onToolUse func(count int)
	t         Transcript
	edited    map[string]bool
	counted   map[string]bool // item ids already counted as tool uses
}

// NewParser returns a parser that relativises changed paths against cwd and
// calls onToolUse with the running count after every tool-like item.
func NewParser(cwd string, onToolUse func(int)) *Parser {
	return &Parser{cwd: cwd, onToolUse: onToolUse, edited: map[string]bool{}, counted: map[string]bool{}}
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
	Item *struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Text    string `json:"text"`
		Changes []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
	} `json:"item"`
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
		if toolItems[it.Type] && !p.counted[it.ID] {
			p.counted[it.ID] = true
			p.t.ToolUses++
			if p.onToolUse != nil {
				p.onToolUse(p.t.ToolUses)
			}
		}
	}
}

func (p *Parser) addEdited(path string) {
	if p.cwd != "" {
		if rel, err := filepath.Rel(p.cwd, path); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			path = rel
		}
	}
	if !p.edited[path] {
		p.edited[path] = true
		p.t.EditedFiles = append(p.t.EditedFiles, path)
	}
}

// Transcript returns what has been parsed so far.
func (p *Parser) Transcript() Transcript {
	return p.t
}
