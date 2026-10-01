// Package claudecli runs the Claude Code CLI headless and parses its
// stream-json output. Both the executor and the triager build on it, so
// process supervision (process-group kill, step budget, timeout, bounded
// logs) lives in one place.
package claudecli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

// BudgetError reports that the provider refused work for quota reasons.
// ResetsAt is zero when the reset time is unknown.
type BudgetError struct {
	Message  string
	ResetsAt time.Time
}

func (e *BudgetError) Error() string {
	if e.ResetsAt.IsZero() {
		return "budget: " + e.Message
	}
	return "budget: " + e.Message + " (resets " + e.ResetsAt.UTC().Format("2006-01-02 15:04 UTC") + ")"
}

// LooksLikeBudget reports whether a finished transcript indicates a quota
// or rate-limit stop, and the reset time if the stream carried one.
func LooksLikeBudget(tr Transcript) (bool, time.Time) {
	var reset time.Time
	if tr.RateLimit != nil && !tr.RateLimit.ResetsAt.IsZero() {
		reset = tr.RateLimit.ResetsAt
	}
	r := tr.Result
	if r != nil && r.APIErrorStatus != nil && *r.APIErrorStatus == 429 {
		return true, reset
	}
	if tr.RateLimit != nil && tr.RateLimit.Status != "" && tr.RateLimit.Status != "allowed" {
		return true, reset
	}
	if r != nil {
		low := strings.ToLower(r.Result)
		if strings.Contains(low, "usage limit") || strings.Contains(low, "rate limit") || strings.Contains(low, "session limit") {
			return true, reset
		}
	}
	return false, reset
}

// RateLimit is the last rate_limit_event seen on the stream.
type RateLimit struct {
	Status   string
	ResetsAt time.Time
}

// ResultMsg is the final "result" message of a headless run.
type ResultMsg struct {
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
	Usage            *ResultUsage    `json:"usage"`
}

// ResultUsage is the run's token totals.
type ResultUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// Transcript is what the parser learned from a run.
type Transcript struct {
	SessionID   string
	ToolUses    int
	EditedFiles []string
	RateLimit   *RateLimit
	Result      *ResultMsg
	Lines       int
	Model       string          // from the init event
	Steps       []executor.Step // tool calls and messages, in order
}

// Parser consumes stream-json lines and accumulates a Transcript.
type Parser struct {
	cwd       string // the workspace we ran in
	initCwd   string // what the CLI reported in its init event
	onToolUse func(count int)
	t         Transcript
	edited    map[string]bool
	pending   map[string]int // tool_use id -> index in t.Steps, until its result arrives

	// Now times steps as their lines arrive; nil = time.Now.
	Now func() time.Time
}

// NewParser returns a parser that relativises edited paths against cwd and
// calls onToolUse with the running count after every tool call.
func NewParser(cwd string, onToolUse func(int)) *Parser {
	return &Parser{cwd: cwd, onToolUse: onToolUse, edited: map[string]bool{}, pending: map[string]int{}}
}

// editingTools are the built-in tools whose input names a file they change.
var editingTools = map[string]string{
	"Edit": "file_path", "Write": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path",
}

type envelope struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Model     string `json:"model"`
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
	ID    string                     `json:"id"`
	Name  string                     `json:"name"`
	Input map[string]json.RawMessage `json:"input"`
	Text  string                     `json:"text"`
	// tool_result blocks, in user messages
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
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
	case "system":
		if env.Subtype == "init" {
			if env.SessionID != "" {
				p.t.SessionID = env.SessionID
			}
			p.initCwd = env.Cwd
			if env.Model != "" {
				p.t.Model = env.Model
			}
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
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				now := p.now()
				p.addStep(executor.Step{Kind: executor.StepMessage, Name: "assistant", Output: executor.CapOutput(b.Text), Start: now, End: now})
			}
			if b.Type != "tool_use" {
				continue
			}
			input, _ := json.Marshal(b.Input)
			if i := p.addStep(executor.Step{Kind: executor.StepTool, Name: b.Name, Input: executor.CapOutput(string(input)), Start: p.now()}); i >= 0 && b.ID != "" {
				p.pending[b.ID] = i
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
	case "user":
		if env.Message == nil {
			return
		}
		var blocks []contentBlock
		if json.Unmarshal(env.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			i, ok := p.pending[b.ToolUseID]
			if b.Type != "tool_result" || !ok {
				continue
			}
			delete(p.pending, b.ToolUseID)
			st := &p.t.Steps[i]
			st.End = p.now()
			st.Output = executor.CapOutput(resultText(b.Content))
			st.IsError = b.IsError
		}
	case "rate_limit_event":
		if env.RateLimitInfo != nil {
			p.t.RateLimit = &RateLimit{Status: env.RateLimitInfo.Status, ResetsAt: time.Unix(env.RateLimitInfo.ResetsAt, 0)}
		}
	case "result":
		var r ResultMsg
		if json.Unmarshal(raw, &r) == nil {
			p.t.Result = &r
			if r.SessionID != "" {
				p.t.SessionID = r.SessionID
			}
		}
	}
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

// resultText is a tool_result's content: a string, or text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return string(raw)
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (p *Parser) addEdited(path string) {
	for _, base := range []string{p.cwd, p.initCwd} {
		if base == "" {
			continue
		}
		if rel, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			path = rel
			break
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
