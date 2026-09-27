package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/discover"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
	"gopkg.in/yaml.v3"
)

// RepoDir is the HiveDispatch folder at a repository's root.
const RepoDir = repoconfig.Dir

// RepoFileName is the tracker settings file inside RepoDir.
const RepoFileName = "repo.yaml"

// lookupOrigin reads a checkout's origin URL and default branch; tests
// replace it so they never run git.
var lookupOrigin = discover.Origin

// repoFile is the schema of .hive-dispatch/repo.yaml. It is decoded
// strictly: a typo, or a worker-level key such as jira.email, is an error
// rather than a silently ignored setting.
type repoFile struct {
	Project       string `yaml:"project"`
	Tracker       string `yaml:"tracker"`
	Name          string `yaml:"name"`
	URL           string `yaml:"url"`
	DefaultBranch string `yaml:"default_branch"`
	Jira          struct {
		BaseURL  string       `yaml:"base_url"`
		JQL      string       `yaml:"jql"`
		Fields   JiraFields   `yaml:"fields"`
		Statuses JiraStatuses `yaml:"statuses"`
	} `yaml:"jira"`
	GitHub struct {
		Labels  GitHubLabels  `yaml:"labels"`
		Project GitHubProject `yaml:"project"`
	} `yaml:"github"`
}

// repoFilePath is dir/.hive-dispatch/repo.yaml.
func repoFilePath(dir string) string { return filepath.Join(dir, RepoDir, RepoFileName) }

// readRepo reads dir's repo.yaml and fills origin facts; it does not merge
// accounts, apply defaults or validate.
func readRepo(ctx context.Context, dir string) (RepoConfig, error) {
	file := repoFilePath(dir)
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return RepoConfig{}, fmt.Errorf("%s: not enrolled — no %s/%s; run `hivedispatch init -github %s` (or -jira)", dir, RepoDir, RepoFileName, dir)
	}
	if err != nil {
		return RepoConfig{}, err
	}
	var f repoFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return RepoConfig{}, fmt.Errorf("%s: %w", file, err)
	}
	r := RepoConfig{
		Path: dir, Name: f.Name, URL: f.URL, DefaultBranch: f.DefaultBranch,
		Project: f.Project, Tracker: f.Tracker,
		Jira:   JiraConfig{BaseURL: f.Jira.BaseURL, JQL: f.Jira.JQL, Fields: f.Jira.Fields, Statuses: f.Jira.Statuses},
		GitHub: GitHubConfig{Labels: f.GitHub.Labels, Project: f.GitHub.Project},
	}
	if r.URL == "" || r.DefaultBranch == "" {
		// A missing origin leaves URL empty; Validate reports it with the
		// file's other problems.
		if url, branch, err := lookupOrigin(ctx, dir); err == nil {
			def(&r.URL, url)
			def(&r.DefaultBranch, branch)
		}
	}
	def(&r.Name, discover.ParseRepoName(r.URL))
	return r, nil
}

// withAccounts merges the worker's accounts into r and applies defaults.
func (c *Config) withAccounts(r RepoConfig) RepoConfig {
	r.Jira.Email, r.Jira.Token = c.Jira.Email, c.Jira.Token
	r.GitHub.APIURL, r.GitHub.Token = c.GitHub.APIURL, c.GitHub.Token
	r.applyDefaults()
	return r
}

// Scan finds git repositories under roots (the code dirs when roots is
// empty), never descending into the workroot.
func (c *Config) Scan(roots []string) ([]discover.Found, error) {
	if len(roots) == 0 {
		roots = c.CodeDirs
	}
	return discover.Scan(roots, c.ScanDepth, []string{c.Workroot})
}

// resolveRepos fills Repos: explicit paths in order, then enrolled
// repositories under the code dirs. A second checkout of a repository
// already listed (same owner/repo) is recorded in Shadowed and skipped.
// Problems are kept for Validate.
func (c *Config) resolveRepos(ctx context.Context) {
	var dirs []string
	for _, ref := range c.RepoPaths {
		dirs = append(dirs, absPath(expandHome(ref.Path)))
	}
	if len(c.CodeDirs) > 0 {
		found, err := c.Scan(nil)
		if err != nil {
			c.problems = append(c.problems, "code_dirs: "+err.Error())
		}
		for _, f := range found {
			if f.Enrolled {
				dirs = append(dirs, f.Path)
			}
		}
	}
	seenPath := map[string]bool{}
	byName := map[string]string{}
	c.Repos, c.Shadowed = nil, map[string]string{}
	for _, dir := range dirs {
		if seenPath[dir] {
			continue
		}
		seenPath[dir] = true
		r, err := readRepo(ctx, dir)
		if err != nil {
			c.problems = append(c.problems, err.Error())
			continue
		}
		name := strings.ToLower(r.Name)
		if kept, dup := byName[name]; dup && name != "" {
			c.Shadowed[dir] = kept
			continue
		}
		byName[name] = dir
		c.Repos = append(c.Repos, c.withAccounts(r))
	}
}

// LoadRepo reads and validates the one repository at dir, for commands
// that act on a single repository whether or not it is enrolled yet.
func (c *Config) LoadRepo(dir string) (RepoConfig, error) {
	r, err := readRepo(context.Background(), absPath(dir))
	if err != nil {
		return RepoConfig{}, err
	}
	r = c.withAccounts(r)
	one := *c
	one.Repos = []RepoConfig{r}
	problems := one.repoProblems(r)
	problems = append(problems, one.accountProblems()...)
	return r, problemsError(problems)
}

// Enrolled reports whether the worker would pick up the repository at dir:
// it is listed under repos: or lies under a code dir.
func (c *Config) Enrolled(dir string) bool {
	dir = absPath(dir)
	for _, ref := range c.RepoPaths {
		if absPath(expandHome(ref.Path)) == dir {
			return true
		}
	}
	dir = resolved(dir)
	for _, root := range c.CodeDirs {
		if rel, err := filepath.Rel(resolved(root), dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolved is p with symlinks evaluated, or p when that fails.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return filepath.Clean(p)
}
