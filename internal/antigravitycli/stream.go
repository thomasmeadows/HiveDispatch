// Package antigravitycli runs Antigravity CLI and parses its event stream.
package antigravitycli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

// Usage is Antigravity's token accounting. Thinking tokens are already in output.
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
}

// ResultMsg is the terminal result envelope. Usage is cumulative over the session.
type ResultMsg struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	Response       string `json:"response"`
	Error          string `json:"error"`
	Usage          Usage  `json:"usage"`
}

// Transcript holds the current invocation's steps and its terminal result.
type Transcript struct {
	SessionID    string
	Model        string
	ToolUses     int
	Steps        []executor.Step
	Usage        executor.Usage
	HasStepUsage bool
	Result       *ResultMsg
}

type stepKey struct {
	conversation string
	index        int
}
type stepState struct {
	traceIndex int
	done       bool
	usage      Usage
}

// Parser consumes Antigravity's init, step_update and result events.
type Parser struct {
	t         Transcript
	states    map[stepKey]*stepState
	onToolUse func(int)
}

// NewParser counts each tool step once, even when it emits multiple updates.
func NewParser(onToolUse func(int)) *Parser {
	return &Parser{states: map[stepKey]*stepState{}, onToolUse: onToolUse}
}

type update struct {
	ConversationID string `json:"conversation_id"`
	StepIndex      int    `json:"step_index"`
	State          string `json:"state"`
	StepType       string `json:"step_type"`
	ToolName       string `json:"tool_name"`
	TextDelta      string `json:"text_delta"`
	Usage          *Usage `json:"usage"`
	ToolInfo       *struct {
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
		Output     json.RawMessage `json:"output"`
		Error      *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"tool_info"`
	SubagentInfo json.RawMessage `json:"subagent_info"`
}

// Line accepts a JSON event. Unknown events are ignored for forward compatibility;
// malformed events fail the run rather than silently losing accounting or errors.
func (p *Parser) Line(raw []byte) error {
	if strings.TrimSpace(string(raw)) == "" {
		return nil
	}
	var e struct {
		Event          string `json:"event"`
		ConversationID string `json:"conversation_id"`
		Init           struct {
			Model string `json:"model"`
		} `json:"init"`
		Step   *update    `json:"step_update"`
		Result *ResultMsg `json:"result"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("antigravity stream: %w", err)
	}
	switch e.Event {
	case "init":
		p.t.SessionID = e.ConversationID
		p.t.Model = e.Init.Model
	case "step_update":
		if e.Step == nil {
			return fmt.Errorf("antigravity stream: step_update has no payload")
		}
		p.step(e.Step)
	case "result":
		if e.Result == nil {
			return fmt.Errorf("antigravity stream: result has no payload")
		}
		p.t.Result = e.Result
		if e.Result.ConversationID != "" {
			p.t.SessionID = e.Result.ConversationID
		}
	}
	return nil
}

func (p *Parser) step(u *update) {
	conversation := u.ConversationID
	if conversation == "" {
		conversation = p.t.SessionID
	}
	key := stepKey{conversation, u.StepIndex}
	st, seen := p.states[key]
	now := time.Now()
	if !seen {
		st = &stepState{traceIndex: -1}
		p.states[key] = st
		kind := executor.StepMessage
		name := "assistant"
		isTool := u.StepType == "tool" || len(u.SubagentInfo) > 0
		if isTool {
			kind = executor.StepTool
			name = u.ToolName
			if name == "" && u.ToolInfo != nil {
				name = u.ToolInfo.Name
			}
			if name == "" {
				name = "subagent"
			}
			p.t.ToolUses++
			if p.onToolUse != nil {
				p.onToolUse(p.t.ToolUses)
			}
		}
		if (isTool || u.StepType == "agent_response") && len(p.t.Steps) < executor.MaxSteps {
			st.traceIndex = len(p.t.Steps)
			p.t.Steps = append(p.t.Steps, executor.Step{Kind: kind, Name: name, Start: now})
		}
	}
	// Per-step usage is a snapshot; apply only its delta. Result usage may
	// include earlier invocations when --conversation resumes a session.
	if u.Usage != nil {
		p.t.HasStepUsage = true
		p.t.Usage.InputTokens += u.Usage.InputTokens + u.Usage.CacheReadTokens - st.usage.InputTokens - st.usage.CacheReadTokens
		p.t.Usage.OutputTokens += u.Usage.OutputTokens - st.usage.OutputTokens
		st.usage = *u.Usage
	}
	if st.done {
		return
	}
	if st.traceIndex >= 0 {
		trace := &p.t.Steps[st.traceIndex]
		if u.TextDelta != "" {
			trace.Output = executor.CapOutput(trace.Output + u.TextDelta)
		}
		if info := u.ToolInfo; info != nil {
			if len(info.Parameters) > 0 {
				trace.Input = executor.CapOutput(string(info.Parameters))
			}
			if len(info.Output) > 0 {
				var text string
				if json.Unmarshal(info.Output, &text) != nil {
					text = string(info.Output)
				}
				trace.Output = executor.CapOutput(text)
			}
			if info.Error != nil {
				trace.IsError = true
				trace.Output = executor.CapOutput(trace.Output + "\n" + info.Error.Type + ": " + info.Error.Message)
			}
		}
		if len(u.SubagentInfo) > 0 {
			trace.Input = executor.CapOutput(string(u.SubagentInfo))
		}
		if u.State == "DONE" {
			trace.End = now
		}
	}
	st.done = u.State == "DONE"
}

// Transcript returns the parsed state so far.
func (p *Parser) Transcript() Transcript { return p.t }
