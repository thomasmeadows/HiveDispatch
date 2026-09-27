package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/discover"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor"
	"github.com/thomasmeadows/hivedispatch/internal/yamlfile"
)

// overview is the dashboard's summary of the worker config.
func (s *Server) overview(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{
		"config_path": s.o.ConfigPath,
	}
	if _, err := os.Stat(s.o.ConfigPath); err != nil {
		out["config_exists"] = false
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["config_exists"] = true
	cfg, err := config.LoadUnvalidated(s.o.ConfigPath)
	if err != nil {
		out["config_problem"] = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	if err := cfg.Validate(); err != nil {
		out["config_problem"] = err.Error()
	}
	repos := make([]map[string]string, 0, len(cfg.Repos))
	for _, r := range cfg.Repos {
		repos = append(repos, map[string]string{"project": r.Project, "tracker": r.Tracker, "name": r.Name, "path": r.Path})
	}
	out["agent_id"] = cfg.AgentID
	out["max_concurrent"] = cfg.MaxConcurrent
	out["poll_interval"] = cfg.PollInterval.String()
	out["state_store"] = cfg.StateStore
	out["code_dirs"] = cfg.CodeDirs
	out["repos"] = repos
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	if s.o.Check == nil {
		writeError(w, http.StatusNotImplemented, errors.New("check is not available"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": s.o.Check(r.Context())})
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	if s.o.ListRuns == nil {
		writeError(w, http.StatusNotImplemented, errors.New("run records are not available"))
		return
	}
	runs, err := s.o.ListRuns(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].UpdatedAt.After(runs[j].UpdatedAt) })
	if runs == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// knownRepos is every repository the operator can act on: what a scan of
// the code dirs (else the home directory) finds, plus explicit repos:
// entries. It is also the allowlist for repo file reads and writes.
func (s *Server) knownRepos() (rows []config.ScanRow, roots []string, home bool, err error) {
	cfg, err := config.LoadWorker(s.o.ConfigPath)
	if err != nil {
		return nil, nil, false, fmt.Errorf("the worker config must load before repositories can be listed: %w", err)
	}
	roots, home, err = cfg.ScanRoots(nil)
	if err != nil {
		return nil, nil, false, err
	}
	rows, err = cfg.ScanRows(roots)
	if err != nil {
		return nil, nil, false, err
	}
	for _, ref := range cfg.RepoPaths {
		dir, err := filepath.Abs(ref.Path)
		if err != nil || slices.ContainsFunc(rows, func(r config.ScanRow) bool { return r.Path == dir }) {
			continue
		}
		_, statErr := os.Stat(filepath.Join(dir, config.RepoDir, config.RepoFileName))
		rows = append(rows, cfg.Describe(discover.Found{Path: dir, Enrolled: statErr == nil}))
	}
	return rows, roots, home, nil
}

func (s *Server) repos(w http.ResponseWriter, _ *http.Request) {
	rows, roots, home, err := s.knownRepos()
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roots": roots, "home_default": home, "repos": rows})
}

// knownRepo returns the row for dir, or an error when dir is not one of
// knownRepos — no file outside an operator's repositories is served.
func (s *Server) knownRepo(dir string) (config.ScanRow, error) {
	if dir == "" {
		return config.ScanRow{}, errors.New("path is required")
	}
	rows, _, _, err := s.knownRepos()
	if err != nil {
		return config.ScanRow{}, err
	}
	for _, r := range rows {
		if r.Path == filepath.Clean(dir) {
			return r, nil
		}
	}
	return config.ScanRow{}, errForbidden{fmt.Errorf("%s is not a repository found by scan", dir)}
}

type errForbidden struct{ error }

func repoError(w http.ResponseWriter, err error) {
	var f errForbidden
	if errors.As(err, &f) {
		writeError(w, http.StatusForbidden, err)
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

func (s *Server) enrol(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path    string `json:"path"`
		Tracker string `json:"tracker"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if in.Tracker != "github" && in.Tracker != "jira" {
		writeError(w, http.StatusBadRequest, errors.New("tracker must be github or jira"))
		return
	}
	row, err := s.knownRepo(in.Path)
	if err != nil {
		repoError(w, err)
		return
	}
	written, err := config.WriteRepoStarter(row.Path, in.Tracker)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"written": written, "dir": filepath.Join(row.Path, config.RepoDir)})
}

// setupRepo runs `hivedispatch init -<tracker> DIR` for an enrolled
// repository: it creates the tracker's labels or claim fields.
func (s *Server) setupRepo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	row, err := s.knownRepo(in.Path)
	if err != nil {
		repoError(w, err)
		return
	}
	if !row.Enrolled || (row.Tracker != "github" && row.Tracker != "jira") {
		writeError(w, http.StatusConflict, errors.New("fill in .hive-dispatch/repo.yaml (project and tracker) first"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.o.Exe, "init", "-"+row.Tracker, "-config", s.o.ConfigPath, row.Path)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exit_code": code, "output": out.String()})
}

// file is one editable config file: where it is, a starter for when it is
// missing, and its loader.
type file struct {
	path     string
	starter  string
	validate func(tmp string) error
}

func (s *Server) file(r *http.Request) (file, error) {
	getenv := s.o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	switch kind := r.PathValue("kind"); kind {
	case "worker":
		return file{path: s.o.ConfigPath, starter: config.Starter, validate: func(tmp string) error {
			if _, err := config.Load(tmp); err != nil {
				return err
			}
			c, err := supervisor.LoadConfig(tmp, getenv)
			if err != nil {
				return err
			}
			_, err = supervisor.NewModel(c, getenv)
			return err
		}}, nil
	case "repo", "policy":
		row, err := s.knownRepo(r.URL.Query().Get("path"))
		if err != nil {
			return file{}, err
		}
		if kind == "policy" {
			return file{path: filepath.Join(row.Path, repoconfig.Dir, repoconfig.PolicyFile), starter: config.PolicyStarter, validate: func(tmp string) error {
				raw, err := os.ReadFile(tmp)
				if err != nil {
					return err
				}
				_, err = repoconfig.Parse(raw)
				return err
			}}, nil
		}
		return file{path: filepath.Join(row.Path, config.RepoDir, config.RepoFileName), starter: config.RepoStarter("github"), validate: func(tmp string) error {
			raw, err := os.ReadFile(tmp)
			if err != nil {
				return err
			}
			worker, err := config.LoadWorker(s.o.ConfigPath)
			if err != nil {
				return fmt.Errorf("worker config: %w", err)
			}
			_, err = worker.ParseRepo(row.Path, raw)
			return err
		}}, nil
	default:
		return file{}, fmt.Errorf("unknown config file %q (want worker, repo or policy)", kind)
	}
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.file(r)
	if err != nil {
		repoError(w, err)
		return
	}
	out := map[string]any{"path": f.path, "starter": f.starter}
	raw, err := os.ReadFile(f.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		out["exists"] = false
		writeJSON(w, http.StatusOK, out)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out["exists"] = true
	out["raw"] = string(raw)
	var data map[string]any
	if err := yaml.Unmarshal(raw, &data); err != nil {
		out["parse_error"] = err.Error()
	} else {
		out["data"] = data
	}
	writeJSON(w, http.StatusOK, out)
}

// postFile stages an edit — raw content, or form fields patched into the
// current file — and returns its diff and remaining problems; with apply it
// also writes the file (keeping a .bak).
func (s *Server) postFile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Content *string        `json:"content"`
		Set     map[string]any `json:"set"`
		Apply   bool           `json:"apply"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	f, err := s.file(r)
	if err != nil {
		repoError(w, err)
		return
	}
	var content []byte
	switch {
	case in.Content != nil && in.Set != nil:
		writeError(w, http.StatusBadRequest, errors.New("send content or set, not both"))
		return
	case in.Content != nil:
		content = []byte(*in.Content)
	case in.Set != nil:
		cur, err := os.ReadFile(f.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if content, err = yamlfile.Patch(cur, in.Set); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	default:
		writeError(w, http.StatusBadRequest, errors.New("send content or set"))
		return
	}
	staged, err := yamlfile.Stage(f.path, content, f.validate)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	changed := !bytes.Equal(staged.Old, staged.New)
	if in.Apply && changed {
		if err := staged.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": f.path, "diff": staged.Diff, "problems": staged.Problems,
		"changed": changed, "applied": in.Apply && changed, "content": string(staged.New),
	})
}
