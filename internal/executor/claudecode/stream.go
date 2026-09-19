// Package claudecode adapts the Claude Code CLI to executor.Executor.
//
// The CLI is run headless with --output-format stream-json. The orchestrator
// never interprets the session id it stores as the resume token; it only
// hands it back with --resume.
package claudecode

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

type rateLimit struct {
	Status   string
	ResetsAt time.Time
}

type resultMsg struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	SessionID        string          `json:"session_id"`
	NumTurns         int             `json:"num_turns"`
	StopReason       string          `json:"stop_reason"`
	TerminalReason   string          `json:"terminal_reason"`
	APIErrorStatus   *int            `json:"api_error_status"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
}

// transcript is what the parser learned from a run.
type transcript struct {
	SessionID   string
	ToolUses    int
	EditedFiles []string
	RateLimit   *rateLimit
	Result      *resultMsg
	Lines       int
}

type streamParser struct {
	cwd       string
	onToolUse func(count int)
	t         transcript
	edited    map[string]bool
}

func newStreamParser(cwd string, onToolUse func(int)) *streamParser {
	return &streamParser{cwd: cwd, onToolUse: onToolUse, edited: map[string]bool{}}
}

// editingTools are the built-in tools whose input names a file they change.
var editingTools = map[string]string{
	"Edit": "file_path", "Write": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path",
}

type envelope struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Message   *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	RateLimitInfo *struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resetsAt"`
	} `json:"rate_limit_info"`
}

type contentBlock struct {
	Type  string                     `json:"type"`
	Name  string                     `json:"name"`
	Input map[string]json.RawMessage `json:"input"`
}

// Line consumes one line of output. Non-JSON lines are counted and ignored.
func (p *streamParser) Line(raw []byte) {
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
	case "system":
		if env.Subtype == "init" && env.SessionID != "" {
			p.t.SessionID = env.SessionID
		}
	case "assistant":
		if env.Message == nil {
			return
		}
		var blocks []contentBlock
		if json.Unmarshal(env.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			if b.Type != "tool_use" {
				continue
			}
			p.t.ToolUses++
			if field, ok := editingTools[b.Name]; ok {
				var path string
				if json.Unmarshal(b.Input[field], &path) == nil && path != "" {
					p.addEdited(path)
				}
			}
			if p.onToolUse != nil {
				p.onToolUse(p.t.ToolUses)
			}
		}
	case "rate_limit_event":
		if env.RateLimitInfo != nil {
			p.t.RateLimit = &rateLimit{Status: env.RateLimitInfo.Status, ResetsAt: time.Unix(env.RateLimitInfo.ResetsAt, 0)}
		}
	case "result":
		var r resultMsg
		if json.Unmarshal(raw, &r) == nil {
			p.t.Result = &r
			if r.SessionID != "" {
				p.t.SessionID = r.SessionID
			}
		}
	}
}

func (p *streamParser) addEdited(path string) {
	if rel, err := filepath.Rel(p.cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		path = rel
	}
	if !p.edited[path] {
		p.edited[path] = true
		p.t.EditedFiles = append(p.t.EditedFiles, path)
	}
}

// Transcript returns what has been parsed so far.
func (p *streamParser) Transcript() transcript {
	return p.t
}
