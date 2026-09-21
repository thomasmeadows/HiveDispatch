// Package fake is a scripted model.Model for tests.
package fake

import (
	"context"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

// Model returns Responses in order and records every Request. When the
// script is exhausted it returns an empty end_turn so a runaway loop
// terminates. Fail, when set, is returned once by the next Chat.
type Model struct {
	mu        sync.Mutex
	Responses []model.Response
	Fail      error
	Calls     []model.Request
}

var _ model.Model = (*Model)(nil)

// New returns a Model scripted with responses.
func New(responses ...model.Response) *Model {
	return &Model{Responses: responses}
}

// Name implements model.Model.
func (m *Model) Name() string { return "fake" }

// Chat implements model.Model.
func (m *Model) Chat(_ context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, req)
	if m.Fail != nil {
		err := m.Fail
		m.Fail = nil
		return model.Response{}, err
	}
	if len(m.Responses) == 0 {
		return model.Response{Message: model.Message{Role: model.RoleAssistant}, StopReason: model.StopEndTurn}, nil
	}
	r := m.Responses[0]
	m.Responses = m.Responses[1:]
	return r, nil
}

// Text is a convenience: an end_turn response with only text.
func Text(s string) model.Response {
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: s}, StopReason: model.StopEndTurn}
}

// Call is a convenience: a tool_use response with one call.
func Call(id, name, args string) model.Response {
	return model.Response{
		Message:    model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: id, Name: name, Args: []byte(args)}}},
		StopReason: model.StopToolUse,
	}
}
