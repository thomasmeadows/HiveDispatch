// Package passthrough is a Triager that dispatches every ticket. It is the
// rules-based baseline the model-backed triager is measured against.
package passthrough

import (
	"context"

	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// Triager dispatches unconditionally.
type Triager struct{}

var _ triage.Triager = Triager{}

// Decide implements triage.Triager.
func (Triager) Decide(_ context.Context, in triage.Input) (triage.Decision, error) {
	return triage.Decision{
		Kind:   triage.KindDispatch,
		Reason: "passthrough",
		Prompt: prompt.Render(in.Ticket, in.Repo, in.Branch),
	}, nil
}
