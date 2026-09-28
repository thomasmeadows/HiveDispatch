// Package githost defines pull-request operations on a git hosting service.
package githost

import "context"

// PR is an open pull request.
type PR struct {
	URL     string
	Number  int
	Draft   bool
	HeadSHA string // the commit the PR is at, so a review can tell a new version
}

// Request describes a pull request to open.
type Request struct {
	Title string
	Body  string
	Head  string // branch with the changes
	Base  string // target branch
	Draft bool
}

// GitHost finds and opens pull requests. repo is "owner/name".
type GitHost interface {
	// FindPR returns the open PR whose head is head, or nil, nil.
	FindPR(ctx context.Context, repo, head string) (*PR, error)
	OpenPR(ctx context.Context, repo string, req Request) (*PR, error)
	// Review posts a review comment on pull request number.
	Review(ctx context.Context, repo string, number int, body string) error
}
