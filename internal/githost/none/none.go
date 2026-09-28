// Package none is a githost.GitHost for workers without GitHub credentials:
// branches are still pushed, but no pull request is opened, and the ticket
// comment tells the human to open it.
package none

import (
	"context"

	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

// Host never finds or opens pull requests.
type Host struct{}

var _ githost.GitHost = Host{}

// FindPR implements githost.GitHost.
func (Host) FindPR(context.Context, string, string) (*githost.PR, error) { return nil, nil }

// OpenPR implements githost.GitHost.
func (Host) OpenPR(context.Context, string, githost.Request) (*githost.PR, error) {
	return nil, nil
}

// Review implements githost.GitHost; without a host there is no pull request.
func (Host) Review(context.Context, string, int, string) error { return nil }
