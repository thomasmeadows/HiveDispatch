// Package ghissues implements tracker.Tracker over GitHub Issues.
//
// GitHub has no workflow states and no custom fields, so state is carried
// by one hive:* label at a time and the claim by a hidden HTML comment at
// the end of the issue body. Ticket keys are "<PROJECT>-<number>", where
// PROJECT is the repo's configured project key, so the rest of HiveDispatch
// sees the same key shape as with Jira. Optionally a GitHub Projects (v2)
// board mirrors the labels: each transition also moves the issue's card to
// the column configured for the new state (see project.go).
package ghissues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Client is a GitHub Issues tracker over one or more repositories.
type Client struct {
	cfg   config.GitHubConfig
	repos []config.RepoConfig
	api   *url.URL
	http  *http.Client
	now   func() time.Time

	projMu sync.Mutex
	proj   *projectRef // resolved Projects v2 board, when cfg.Project is set

	beforeReadBack func(key string)
}

var _ tracker.Tracker = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

func withNow(f func() time.Time) Option {
	return func(c *Client) { c.now = f }
}

func withBeforeReadBack(f func(key string)) Option {
	return func(c *Client) { c.beforeReadBack = f }
}

// New returns a Client for the repositories in repos.
func New(cfg config.GitHubConfig, repos []config.RepoConfig, opts ...Option) (*Client, error) {
	api, err := url.Parse(strings.TrimRight(cfg.APIURL, "/"))
	if err != nil || api.Scheme == "" || api.Host == "" {
		return nil, fmt.Errorf("ghissues: invalid api_url %q", cfg.APIURL)
	}
	c := &Client{cfg: cfg, repos: repos, api: api, http: &http.Client{Timeout: 30 * time.Second}, now: time.Now}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// keyToRef maps "PROJECT-12" to the repo it belongs to and the issue number.
func (c *Client) keyToRef(key string) (repo string, number int, err error) {
	project, num, ok := strings.Cut(key, "-")
	if !ok {
		return "", 0, fmt.Errorf("ghissues: %q is not PROJECT-N", key)
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return "", 0, fmt.Errorf("ghissues: %q is not PROJECT-N", key)
	}
	for _, r := range c.repos {
		if strings.EqualFold(r.Project, project) {
			return r.Name, n, nil
		}
	}
	return "", 0, fmt.Errorf("ghissues: no repo configured with project %q", project)
}

// refToKey maps a repo and issue number back to a ticket key.
func (c *Client) refToKey(repo string, number int) (string, bool) {
	for _, r := range c.repos {
		if strings.EqualFold(r.Name, repo) {
			return fmt.Sprintf("%s-%d", strings.ToUpper(r.Project), number), true
		}
	}
	return "", false
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github: %s %s: %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// Is lets a 404 satisfy errors.Is(err, tracker.ErrNotFound).
func (e *APIError) Is(target error) bool {
	return target == tracker.ErrNotFound && e.Status == http.StatusNotFound
}

// do performs one JSON request against path (absolute URL or API path) and
// returns the response headers (for pagination).
func (c *Client) do(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("github: bad path %q: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api.ResolveReference(ref).String(), rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Message string `json:"message"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		return resp.Header, &APIError{Status: resp.StatusCode, Method: method, Path: ref.Path, Message: msg}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.Header, fmt.Errorf("github: decode %s %s: %w", method, path, err)
		}
	}
	return resp.Header, nil
}

// nextLink extracts the rel="next" URL from a Link header, or "".
func nextLink(h http.Header) string {
	for _, part := range strings.Split(h.Get("Link"), ",") {
		part = strings.TrimSpace(part)
		if !strings.HasSuffix(part, `rel="next"`) {
			continue
		}
		start, end := strings.Index(part, "<"), strings.Index(part, ">")
		if start >= 0 && end > start {
			return part[start+1 : end]
		}
	}
	return ""
}

func issuePath(repo string, number int, suffix string) string {
	return fmt.Sprintf("/repos/%s/issues/%d%s", repo, number, suffix)
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }
