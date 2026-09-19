// Package jira implements tracker.Tracker against Jira Cloud REST API v3.
//
// It calls REST directly rather than through a client library because the
// /search endpoint was removed (410) in favour of /search/jql, and most
// libraries have not migrated.
package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Client is a Jira Cloud tracker.
type Client struct {
	cfg  config.JiraConfig
	http *http.Client
	base *url.URL
	now  func() time.Time

	// beforeReadBack, if set, runs between Claim's write and read-back (tests).
	beforeReadBack func(key string)
}

var _ tracker.Tracker = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// withNow overrides the clock (tests).
func withNow(f func() time.Time) Option {
	return func(c *Client) { c.now = f }
}

// withBeforeReadBack runs f between Claim's write and read-back (tests).
func withBeforeReadBack(f func(key string)) Option {
	return func(c *Client) { c.beforeReadBack = f }
}

// New returns a Client for cfg.
func New(cfg config.JiraConfig, opts ...Option) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("jira: invalid base_url %q", cfg.BaseURL)
	}
	c := &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
		base: base,
		now:  time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// APIError is a non-2xx response from Jira.
type APIError struct {
	Status   int
	Method   string
	Path     string
	Messages []string
}

func (e *APIError) Error() string {
	msg := strings.Join(e.Messages, "; ")
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("jira: %s %s: %d: %s", e.Method, e.Path, e.Status, msg)
}

// Is lets a 404 satisfy errors.Is(err, tracker.ErrNotFound).
func (e *APIError) Is(target error) bool {
	return target == tracker.ErrNotFound && e.Status == http.StatusNotFound
}

// jiraErrorBody is Jira's standard error envelope.
type jiraErrorBody struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
}

// do performs one JSON request. body may be nil; out may be nil.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("jira: encode %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(buf)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("jira: bad path %q: %w", path, err)
	}
	u := c.base.ResolveReference(ref)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return fmt.Errorf("jira: build %s %s: %w", method, path, err)
	}
	req.SetBasicAuth(c.cfg.Email, c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("jira: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("jira: read %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.apiError(method, path, resp.StatusCode, raw)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("jira: decode %s %s: %w", method, path, err)
	}
	return nil
}

func (c *Client) apiError(method, path string, status int, raw []byte) error {
	e := &APIError{Status: status, Method: method, Path: path}
	var body jiraErrorBody
	if json.Unmarshal(raw, &body) == nil {
		e.Messages = append(e.Messages, body.ErrorMessages...)
		keys := make([]string, 0, len(body.Errors))
		for k := range body.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			e.Messages = append(e.Messages, k+": "+body.Errors[k])
		}
	} else if s := strings.TrimSpace(string(raw)); s != "" {
		e.Messages = []string{truncate(s, 200)}
	}
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// issuePath returns /rest/api/3/issue/{key}[suffix].
func issuePath(key, suffix string) string {
	return "/rest/api/3/issue/" + url.PathEscape(key) + suffix
}
