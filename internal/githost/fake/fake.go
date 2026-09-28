// Package fake is an in-memory githost.GitHost for tests.
package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

// Host stores PRs in memory.
type Host struct {
	Err error // returned by every call when set

	mu      sync.Mutex
	prs     map[string]*githost.PR // repo + "#" + head
	opened  []githost.Request
	reviews []string // "repo#number: body"
}

// SetHead moves the PR for head to commit sha (a new push).
func (h *Host) SetHead(repo, head, sha string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pr, ok := h.prs[repo+"#"+head]; ok {
		pr.HeadSHA = sha
	}
}

// Review implements githost.GitHost.
func (h *Host) Review(_ context.Context, repo string, number int, body string) error {
	if h.Err != nil {
		return h.Err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reviews = append(h.reviews, fmt.Sprintf("%s#%d: %s", repo, number, body))
	return nil
}

// Reviews returns every review posted, as "repo#number: body".
func (h *Host) Reviews() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.reviews...)
}

var _ githost.GitHost = (*Host)(nil)

// New returns an empty host.
func New() *Host {
	return &Host{prs: map[string]*githost.PR{}}
}

// FindPR implements githost.GitHost.
func (h *Host) FindPR(_ context.Context, repo, head string) (*githost.PR, error) {
	if h.Err != nil {
		return nil, h.Err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if pr, ok := h.prs[repo+"#"+head]; ok {
		c := *pr
		return &c, nil
	}
	return nil, nil
}

// OpenPR implements githost.GitHost.
func (h *Host) OpenPR(_ context.Context, repo string, req githost.Request) (*githost.PR, error) {
	if h.Err != nil {
		return nil, h.Err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened = append(h.opened, req)
	n := len(h.prs) + 1
	pr := &githost.PR{URL: fmt.Sprintf("https://example.test/%s/pull/%d", repo, n), Number: n, Draft: req.Draft, HeadSHA: "sha-1"}
	h.prs[repo+"#"+req.Head] = pr
	c := *pr
	return &c, nil
}

// Opened returns every request OpenPR has received.
func (h *Host) Opened() []githost.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]githost.Request(nil), h.opened...)
}
