// Package github implements githost.GitHost against the GitHub REST API.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

// Client talks to one GitHub API host.
type Client struct {
	api   *url.URL
	token string
	http  *http.Client
}

var _ githost.GitHost = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New returns a Client for cfg.
func New(cfg config.GitHubConfig, opts ...Option) (*Client, error) {
	api, err := url.Parse(strings.TrimRight(cfg.APIURL, "/"))
	if err != nil || api.Scheme == "" || api.Host == "" {
		return nil, fmt.Errorf("github: invalid api_url %q", cfg.APIURL)
	}
	c := &Client{api: api, token: cfg.Token, http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

type prJSON struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
}

func (p prJSON) toPR() *githost.PR {
	return &githost.PR{URL: p.HTMLURL, Number: p.Number, Draft: p.Draft}
}

// FindPR returns the open PR whose head branch is head, or nil.
func (c *Client) FindPR(ctx context.Context, repo, head string) (*githost.PR, error) {
	owner, _, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("github: repo %q is not owner/name", repo)
	}
	q := url.Values{"state": {"open"}, "head": {owner + ":" + head}, "per_page": {"1"}}
	var prs []prJSON
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/pulls?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return prs[0].toPR(), nil
}

// OpenPR creates a pull request.
func (c *Client) OpenPR(ctx context.Context, repo string, req githost.Request) (*githost.PR, error) {
	body := map[string]any{"title": req.Title, "body": req.Body, "head": req.Head, "base": req.Base, "draft": req.Draft}
	var pr prJSON
	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/pulls", body, &pr); err != nil {
		return nil, err
	}
	return pr.toPR(), nil
}

type errorJSON struct {
	Message string `json:"message"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("github: bad path %q: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api.ResolveReference(ref).String(), rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e errorJSON
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			msg = e.Message
			for _, d := range e.Errors {
				msg += "; " + d.Message
			}
		}
		return fmt.Errorf("github: %s %s: %d: %s", method, path, resp.StatusCode, msg)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// ProbePRWrite checks that the token may open pull requests on repo without
// creating one: a POST with a head branch that does not exist is answered
// 422 when the token is permitted and 403 when it is not.
func (c *Client) ProbePRWrite(ctx context.Context, repo, base string) error {
	body := map[string]any{"title": "hivedispatch permission probe", "head": "hivedispatch-permission-probe", "base": base}
	err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/pulls", body, nil)
	if err == nil {
		return nil // should not happen; treat as permitted
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, ": 422:"):
		return nil
	case strings.Contains(msg, ": 403:") || strings.Contains(msg, ": 404:"):
		return fmt.Errorf("the GitHub token cannot open pull requests on %s — grant it Pull requests: Read and write (fine-grained) or the repo scope (classic): %w", repo, err)
	}
	return err
}
