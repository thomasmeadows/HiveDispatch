package supervisor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

// Tool is something the model may call.
type Tool interface {
	Def() model.ToolDef
	Call(ctx context.Context, args json.RawMessage) (string, error)
}

// Event is what the loop reports as it goes, for the REPL to print.
type Event struct {
	Kind   string // "model_start" | "model_done" | "tool_start" | "tool_done" | "budget"
	Tool   string
	Args   json.RawMessage
	Result string
	Err    error
}

// Agent is the loop: model call, tool calls, repeat until the model
// answers in text or the step budget runs out.
type Agent struct {
	Model      model.Model
	Tools      []Tool
	System     func() string // called before every model call; callers cache what is expensive
	StepBudget int           // tool calls per user turn
	MaxTokens  int
	Events     func(Event)

	history []model.Message
}

// History returns the conversation so far, without the system prompt.
func (a *Agent) History() []model.Message {
	return append([]model.Message(nil), a.history...)
}

// SetHistory replaces the conversation (resume).
func (a *Agent) SetHistory(h []model.Message) {
	a.history = append([]model.Message(nil), h...)
}

func (a *Agent) emit(e Event) {
	if a.Events != nil {
		a.Events(e)
	}
}

func (a *Agent) defs() []model.ToolDef {
	defs := make([]model.ToolDef, 0, len(a.Tools))
	for _, t := range a.Tools {
		defs = append(defs, t.Def())
	}
	return defs
}

func (a *Agent) tool(name string) Tool {
	for _, t := range a.Tools {
		if t.Def().Name == name {
			return t
		}
	}
	return nil
}

// Turn sends one user message and runs tool calls until the model replies
// in text. On a model error the history is exactly as it was before the
// failing call, so the caller can retry or carry on. When ctx is
// cancelled partway through a batch of tool calls, the remaining calls in
// that batch are recorded as cancelled without being invoked, and Turn
// returns ctx.Err() once the batch is fully accounted for.
func (a *Agent) Turn(ctx context.Context, user string) (string, error) {
	a.history = append(a.history, model.Message{Role: model.RoleUser, Content: user})
	steps := 0
	lastText := ""
	for {
		a.emit(Event{Kind: "model_start"})
		resp, err := a.Model.Chat(ctx, model.Request{
			System: a.System(), Messages: a.history, Tools: a.defs(), MaxTokens: a.MaxTokens,
		})
		a.emit(Event{Kind: "model_done"})
		if err != nil {
			return "", err
		}
		msg := resp.Message
		msg.Role = model.RoleAssistant
		if msg.Content != "" || len(msg.ToolCalls) > 0 {
			a.history = append(a.history, msg)
		}
		if msg.Content != "" {
			lastText = msg.Content
		}
		if len(msg.ToolCalls) == 0 {
			if lastText == "" {
				return "(no reply)", nil
			}
			return lastText, nil
		}
		for _, tc := range msg.ToolCalls {
			steps++
			if ctx.Err() != nil {
				a.history = append(a.history, a.cancelled(ctx, tc))
				continue
			}
			a.history = append(a.history, a.call(ctx, tc))
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if steps >= a.StepBudget {
			a.emit(Event{Kind: "budget"})
			return fmt.Sprintf("%s\n\n(step budget of %d tool calls reached — say \"continue\" to keep going)", lastText, a.StepBudget), nil
		}
	}
}

func (a *Agent) call(ctx context.Context, tc model.ToolCall) model.Message {
	a.emit(Event{Kind: "tool_start", Tool: tc.Name, Args: tc.Args})
	res := model.Message{Role: model.RoleTool, ToolCallID: tc.ID}
	t := a.tool(tc.Name)
	var out string
	var err error
	if t == nil {
		err = fmt.Errorf("unknown tool %q", tc.Name)
	} else {
		out, err = t.Call(ctx, tc.Args)
	}
	if err != nil {
		res.IsError = true
		res.Content = err.Error()
	} else {
		res.Content = out
	}
	a.emit(Event{Kind: "tool_done", Tool: tc.Name, Args: tc.Args, Result: res.Content, Err: err})
	return res
}

// cancelled records a tool call that was never invoked because ctx was
// already done when its turn in the batch came up, keeping history
// consistent (every tool_use gets a matching result).
func (a *Agent) cancelled(ctx context.Context, tc model.ToolCall) model.Message {
	res := model.Message{Role: model.RoleTool, ToolCallID: tc.ID, IsError: true, Content: "cancelled"}
	a.emit(Event{Kind: "tool_done", Tool: tc.Name, Args: tc.Args, Result: res.Content, Err: ctx.Err()})
	return res
}
