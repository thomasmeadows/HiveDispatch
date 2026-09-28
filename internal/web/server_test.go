package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/fake"
)

const repoYAML = "project: HD\ntracker: github\nurl: https://github.com/o/app\ndefault_branch: main\n"

type env struct {
	srv     *httptest.Server
	cfgPath string
	code    string
	repo    string // an enrolled repository under code
	plain   string // an unenrolled one
}

// newEnv writes a worker config whose code dir holds one enrolled and one
// plain repository, and serves it. origin facts are in repo.yaml, so no git
// runs.
func newEnv(t *testing.T, o Options) *env {
	t.Helper()
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	dir := t.TempDir()
	e := &env{cfgPath: filepath.Join(dir, "config.yaml"), code: filepath.Join(dir, "code")}
	e.repo, e.plain = filepath.Join(e.code, "app"), filepath.Join(e.code, "plain")
	for _, d := range []string{e.repo, e.plain} {
		mustMkdir(t, filepath.Join(d, ".git"))
	}
	mustMkdir(t, filepath.Join(e.repo, ".hive-dispatch"))
	mustWrite(t, filepath.Join(e.repo, ".hive-dispatch", "repo.yaml"), repoYAML)
	mustWrite(t, e.cfgPath, "# my worker\nagent_id: w1 # this machine\nworkroot: "+filepath.Join(dir, "work")+"\ncode_dirs: ["+e.code+"]\n")
	o.ConfigPath = e.cfgPath
	if o.Assets == nil {
		o.Assets = fstest.MapFS{"index.html": {Data: []byte("<h1>ui</h1>")}}
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(s)
	t.Cleanup(func() { e.srv.Close(); s.Close() })
	return e
}

func mustMkdir(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *env) get(t *testing.T, path string, into any) int {
	t.Helper()
	res, err := http.Get(e.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return decodeBody(t, res, into)
}

func (e *env) post(t *testing.T, path string, body any, into any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", e.srv.URL)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return decodeBody(t, res, into)
}

func decodeBody(t *testing.T, res *http.Response, into any) int {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
	}
	return res.StatusCode
}

func TestServesFrontEnd(t *testing.T) {
	e := newEnv(t, Options{})
	res, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "<h1>ui</h1>") {
		t.Errorf("GET / = %d %q", res.StatusCode, body)
	}
	if code := e.get(t, "/api/nope", nil); code != http.StatusNotFound {
		t.Errorf("unknown endpoint = %d", code)
	}
}

func TestEmbeddedAssetsServe(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("embedded index = %d %q", rec.Code, rec.Body.String())
	}
}

func TestHostAndOriginChecks(t *testing.T) {
	s, err := New(Options{Assets: fstest.MapFS{"index.html": {Data: []byte("x")}}})
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, host string, hdr map[string]string) int {
		req := httptest.NewRequest(method, "http://"+host+"/api/chat/cancel", strings.NewReader("{}"))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code
	}
	jsonHdr := map[string]string{"Content-Type": "application/json"}
	for _, host := range []string{"localhost:7878", "127.0.0.1:7878", "[::1]:7878"} {
		if code := do(http.MethodPost, host, jsonHdr); code != http.StatusOK {
			t.Errorf("POST via %s = %d", host, code)
		}
	}
	if code := do(http.MethodGet, "evil.example:7878", nil); code != http.StatusForbidden {
		t.Errorf("rebinding host = %d, want 403", code)
	}
	if code := do(http.MethodPost, "localhost:7878", map[string]string{"Content-Type": "text/plain"}); code != http.StatusForbidden {
		t.Errorf("non-JSON POST = %d, want 403", code)
	}
	if code := do(http.MethodPost, "localhost:7878", map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", code)
	}
	if code := do(http.MethodPost, "localhost:7878", map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", code)
	}
	if code := do(http.MethodPost, "localhost:7878", map[string]string{"Content-Type": "application/json", "Origin": "http://localhost:7878"}); code != http.StatusOK {
		t.Errorf("same-origin POST = %d", code)
	}
}

func TestOverviewRunsAndCheck(t *testing.T) {
	now := time.Now()
	e := newEnv(t, Options{
		ListRuns: func(context.Context) ([]state.Run, error) {
			return []state.Run{{Ticket: "HD-1", UpdatedAt: now.Add(-time.Hour)}, {Ticket: "HD-2", UpdatedAt: now}}, nil
		},
		Check: func(context.Context) string { return "config ok" },
	})
	var ov map[string]any
	if code := e.get(t, "/api/overview", &ov); code != 200 || ov["agent_id"] != "w1" || ov["config_exists"] != true {
		t.Errorf("overview = %d %v", code, ov)
	}
	repos, _ := ov["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("overview repos = %v (problem %v)", ov["repos"], ov["config_problem"])
	}
	if agents, _ := repos[0].(map[string]any)["agents"].([]any); len(agents) != 1 {
		t.Errorf("overview agents = %v, want the default one", repos[0])
	}
	var runs []state.Run
	if code := e.get(t, "/api/runs", &runs); code != 200 || len(runs) != 2 || runs[0].Ticket != "HD-2" {
		t.Errorf("runs = %d %+v, want newest first", code, runs)
	}
	var chk map[string]string
	if code := e.get(t, "/api/check", &chk); code != 200 || chk["output"] != "config ok" {
		t.Errorf("check = %d %v", code, chk)
	}
	failing := newEnv(t, Options{ListRuns: func(context.Context) ([]state.Run, error) { return nil, errors.New("fetch failed") }})
	var errBody map[string]string
	if code := failing.get(t, "/api/runs", &errBody); code != http.StatusBadGateway || errBody["error"] != "fetch failed" {
		t.Errorf("failing runs = %d %v", code, errBody)
	}
}

func TestOverviewWithoutConfig(t *testing.T) {
	e := newEnv(t, Options{})
	if err := os.Remove(e.cfgPath); err != nil {
		t.Fatal(err)
	}
	var ov map[string]any
	if code := e.get(t, "/api/overview", &ov); code != 200 || ov["config_exists"] != false {
		t.Errorf("overview = %d %v", code, ov)
	}
	var f map[string]any
	if code := e.get(t, "/api/files/worker", &f); code != 200 || f["exists"] != false || !strings.Contains(f["starter"].(string), "agent_id") {
		t.Errorf("missing worker file = %d %v", code, f)
	}
}

func TestReposListsScan(t *testing.T) {
	e := newEnv(t, Options{})
	var out struct {
		Roots []string `json:"roots"`
		Repos []struct {
			Path     string `json:"path"`
			Enrolled bool   `json:"enrolled"`
			PickedUp bool   `json:"picked_up"`
			Problem  string `json:"problem"`
			Project  string `json:"project"`
		} `json:"repos"`
	}
	if code := e.get(t, "/api/repos", &out); code != 200 {
		t.Fatalf("repos = %d", code)
	}
	if len(out.Repos) != 2 {
		t.Fatalf("repos = %+v", out.Repos)
	}
	for _, r := range out.Repos {
		switch filepath.Base(r.Path) {
		case "app":
			if !r.Enrolled || !r.PickedUp || r.Project != "HD" || r.Problem != "" {
				t.Errorf("app = %+v", r)
			}
		case "plain":
			if r.Enrolled {
				t.Errorf("plain = %+v", r)
			}
		}
	}
}

func TestWorkerFilePreviewAndApply(t *testing.T) {
	e := newEnv(t, Options{})
	var f struct {
		Raw  string         `json:"raw"`
		Data map[string]any `json:"data"`
	}
	if code := e.get(t, "/api/files/worker", &f); code != 200 || f.Data["agent_id"] != "w1" {
		t.Fatalf("GET worker = %d %+v", code, f)
	}
	var res struct {
		Diff, Problems, Content string
		Changed, Applied        bool
	}
	body := map[string]any{"set": map[string]any{"agent_id": "w2", "max_concurrent": 2}}
	if code := e.post(t, "/api/files/worker", body, &res); code != 200 || !res.Changed || res.Applied || !strings.Contains(res.Diff, "+agent_id: w2 # this machine") {
		t.Fatalf("preview = %d %+v", code, res)
	}
	if raw, _ := os.ReadFile(e.cfgPath); !strings.Contains(string(raw), "agent_id: w1") {
		t.Fatal("a preview wrote the file")
	}
	body["apply"] = true
	if code := e.post(t, "/api/files/worker", body, &res); code != 200 || !res.Applied || res.Problems != "" {
		t.Fatalf("apply = %d %+v", code, res)
	}
	raw, _ := os.ReadFile(e.cfgPath)
	if s := string(raw); !strings.Contains(s, "# my worker") || !strings.Contains(s, "agent_id: w2 # this machine") || !strings.Contains(s, "max_concurrent: 2") {
		t.Errorf("written = %q", s)
	}
	if bak, _ := os.ReadFile(e.cfgPath + ".bak"); !strings.Contains(string(bak), "agent_id: w1") {
		t.Errorf(".bak = %q", bak)
	}
	var bad map[string]string
	if code := e.post(t, "/api/files/worker", map[string]any{"content": "a: [b"}, &bad); code != http.StatusBadRequest {
		t.Errorf("bad YAML = %d %v", code, bad)
	}
}

func TestRepoFileProblemsAndAllowlist(t *testing.T) {
	e := newEnv(t, Options{})
	var res struct {
		Problems string
		Applied  bool
	}
	q := "/api/files/repo?path=" + e.repo
	if code := e.post(t, q, map[string]any{"content": "project: HD\ntracker: gitlab\n"}, &res); code != 200 || !strings.Contains(res.Problems, "tracker") {
		t.Errorf("bad tracker = %d %+v", code, res)
	}
	if code := e.post(t, "/api/files/policy?path="+e.repo, map[string]any{"content": "executor:\n  permission_mode: yolo\n"}, &res); code != 200 || !strings.Contains(res.Problems, "permission_mode") {
		t.Errorf("bad policy = %d %+v", code, res)
	}
	outside := t.TempDir()
	mustMkdir(t, filepath.Join(outside, ".git"))
	for _, p := range []string{"/api/files/repo?path=" + outside, "/api/files/policy?path=" + filepath.Join(e.repo, "..", "..")} {
		if code := e.get(t, p, nil); code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", p, code)
		}
		if code := e.post(t, p, map[string]any{"content": "a: 1\n", "apply": true}, nil); code != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403", p, code)
		}
	}
	if code := e.get(t, "/api/files/secrets", nil); code != http.StatusBadRequest {
		t.Errorf("unknown kind = %d", code)
	}
}

func TestEnrolWritesStarters(t *testing.T) {
	e := newEnv(t, Options{})
	var out map[string]any
	if code := e.post(t, "/api/repos/enrol", map[string]string{"path": e.plain, "tracker": "jira"}, &out); code != 200 || out["written"] != true {
		t.Fatalf("enrol = %d %v", code, out)
	}
	raw, err := os.ReadFile(filepath.Join(e.plain, ".hive-dispatch", "repo.yaml"))
	if err != nil || !strings.Contains(string(raw), "jira") {
		t.Errorf("repo.yaml = %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(e.plain, ".hive-dispatch", "policy.yaml")); err != nil {
		t.Error(err)
	}
	if code := e.post(t, "/api/repos/enrol", map[string]string{"path": e.plain, "tracker": "gitlab"}, nil); code != http.StatusBadRequest {
		t.Errorf("bad tracker = %d", code)
	}
	if code := e.post(t, "/api/repos/enrol", map[string]string{"path": t.TempDir(), "tracker": "github"}, nil); code != http.StatusForbidden {
		t.Errorf("unknown path = %d", code)
	}
}

func TestSetupRunsInit(t *testing.T) {
	exe := script(t, "echo \"args: $*\"\nexit 3\n")
	e := newEnv(t, Options{Exe: exe})
	var out struct {
		ExitCode int    `json:"exit_code"`
		Output   string `json:"output"`
	}
	if code := e.post(t, "/api/repos/setup", map[string]string{"path": e.repo}, &out); code != 200 || out.ExitCode != 3 || !strings.Contains(out.Output, "init -github -config "+e.cfgPath+" "+e.repo) {
		t.Errorf("setup = %d %+v", code, out)
	}
	if code := e.post(t, "/api/repos/setup", map[string]string{"path": e.plain}, nil); code != http.StatusConflict {
		t.Errorf("setup of an unenrolled repo = %d", code)
	}
}

// script writes an executable shell script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	p := filepath.Join(t.TempDir(), "hivedispatch")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func chatEnv(t *testing.T, m *fake.Model) *env {
	t.Helper()
	exe := script(t, "echo check ok\n")
	var cfgPath string
	e := newEnv(t, Options{Exe: exe, NewChat: func(ctx context.Context, confirm func(string) bool, events func(supervisor.Event)) (*supervisor.Session, error) {
		return supervisor.NewSession(ctx, supervisor.SessionOptions{
			WorkerConfigPath: cfgPath, Exe: exe, ChatModel: m, Confirm: confirm, Events: events,
		})
	}})
	cfgPath = e.cfgPath
	return e
}

// waitFor polls the chat state until cond holds.
func (e *env) waitFor(t *testing.T, cond func(chatStatus) bool) chatStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var st chatStatus
		e.get(t, "/api/chat", &st)
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out; chat = %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func lastOf(st chatStatus, typ string) (chatEvent, bool) {
	for i := len(st.Log) - 1; i >= 0; i-- {
		if st.Log[i].Type == typ {
			return st.Log[i], true
		}
	}
	return chatEvent{}, false
}

func TestChatReplies(t *testing.T) {
	e := chatEnv(t, fake.New(fake.Text("hello operator")))
	if code := e.post(t, "/api/chat", map[string]string{"message": "hi"}, nil); code != http.StatusAccepted {
		t.Fatalf("send = %d", code)
	}
	st := e.waitFor(t, func(s chatStatus) bool { _, ok := lastOf(s, "reply"); return ok && !s.Busy })
	if r, _ := lastOf(st, "reply"); r.Text != "hello operator" || st.Model != "fake" {
		t.Errorf("state = %+v", st)
	}
	if u, _ := lastOf(st, "user"); u.Text != "hi" {
		t.Errorf("user event = %+v", u)
	}
	var reset chatStatus
	if code := e.post(t, "/api/chat/reset", map[string]any{}, &reset); code != 200 || len(reset.Log) != 1 || reset.Log[0].Type != "reset" {
		t.Errorf("reset = %d %+v", code, reset)
	}
}

func TestChatConfirmApproveAndDecline(t *testing.T) {
	for _, approve := range []bool{true, false} {
		m := fake.New(fake.Call("c1", "write_config", `{"content":"agent_id: from-chat\n"}`), fake.Text("done"))
		e := chatEnv(t, m)
		if code := e.post(t, "/api/chat", map[string]string{"message": "set my agent id"}, nil); code != http.StatusAccepted {
			t.Fatalf("send = %d", code)
		}
		st := e.waitFor(t, func(s chatStatus) bool { _, ok := lastOf(s, "confirm"); return ok })
		c, _ := lastOf(st, "confirm")
		if !strings.Contains(c.Text, "+agent_id: from-chat") {
			t.Errorf("confirm prompt = %q", c.Text)
		}
		if code := e.post(t, "/api/chat", map[string]string{"message": "again"}, nil); code != http.StatusConflict {
			t.Errorf("send while busy = %d", code)
		}
		if code := e.post(t, "/api/chat/confirm", map[string]any{"id": c.ID, "approve": approve}, nil); code != 200 {
			t.Fatalf("confirm = %d", code)
		}
		st = e.waitFor(t, func(s chatStatus) bool { _, ok := lastOf(s, "reply"); return ok && !s.Busy })
		if d, _ := lastOf(st, "confirm_done"); d.Approved != approve {
			t.Errorf("confirm_done = %+v", d)
		}
		raw, _ := os.ReadFile(e.cfgPath)
		if got := strings.Contains(string(raw), "from-chat"); got != approve {
			t.Errorf("approve=%v: config = %q", approve, raw)
		}
		if code := e.post(t, "/api/chat/confirm", map[string]any{"id": c.ID, "approve": true}, nil); code != http.StatusNotFound {
			t.Errorf("answering twice = %d", code)
		}
	}
}

func TestChatCancelDeclinesPendingConfirm(t *testing.T) {
	e := chatEnv(t, fake.New(fake.Call("c1", "write_config", `{"content":"agent_id: x\n"}`), fake.Text("done")))
	e.post(t, "/api/chat", map[string]string{"message": "go"}, nil)
	e.waitFor(t, func(s chatStatus) bool { _, ok := lastOf(s, "confirm"); return ok })
	if code := e.post(t, "/api/chat/cancel", map[string]any{}, nil); code != 200 {
		t.Fatalf("cancel = %d", code)
	}
	st := e.waitFor(t, func(s chatStatus) bool { return !s.Busy })
	if ev, ok := lastOf(st, "error"); !ok || ev.Text != "interrupted" {
		t.Errorf("after cancel = %+v", st.Log)
	}
	if raw, _ := os.ReadFile(e.cfgPath); strings.Contains(string(raw), "agent_id: x") {
		t.Error("a cancelled confirm wrote the file")
	}
}

func TestChatWithoutModelReportsError(t *testing.T) {
	e := newEnv(t, Options{NewChat: func(context.Context, func(string) bool, func(supervisor.Event)) (*supervisor.Session, error) {
		return nil, errors.New("no model configured")
	}})
	var st chatStatus
	if e.get(t, "/api/chat", &st); st.Error != "no model configured" {
		t.Errorf("state = %+v", st)
	}
	if code := e.post(t, "/api/chat", map[string]string{"message": "hi"}, nil); code != http.StatusServiceUnavailable {
		t.Errorf("send = %d", code)
	}
}

func TestChatEventsStream(t *testing.T) {
	e := chatEnv(t, fake.New(fake.Text("streamed")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/chat/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	e.post(t, "/api/chat", map[string]string{"message": "hi"}, nil)
	got := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		b := make([]byte, 1024)
		for {
			n, err := res.Body.Read(b)
			buf.Write(b[:n])
			if strings.Contains(buf.String(), `"type":"reply"`) {
				got <- buf.String()
				return
			}
			if err != nil {
				got <- buf.String()
				return
			}
		}
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, `"text":"streamed"`) || !strings.Contains(s, "data: ") {
			t.Errorf("stream = %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply event streamed")
	}
}

type agentsBody struct {
	Path   string         `json:"path"`
	Exists bool           `json:"exists"`
	Agents []config.Agent `json:"agents"`
	Error  string         `json:"error"`
}

func TestAgentsDefaultAndSave(t *testing.T) {
	e := newEnv(t, Options{})
	q := "/api/repos/agents?path=" + e.repo
	var got agentsBody
	if code := e.get(t, q, &got); code != 200 || got.Exists || len(got.Agents) != 1 || got.Agents[0].Name != "default" {
		t.Fatalf("GET without a file = %d %+v", code, got)
	}
	file := filepath.Join(e.repo, ".hive-dispatch", "agents.yaml")
	body := map[string]any{"agents": []config.Agent{{Name: "claude-1", Executor: "claude"}, {Name: "codex-1", Executor: "codex", Model: "o3"}}}
	if code := e.post(t, q, body, &got); code != 200 || len(got.Agents) != 2 {
		t.Fatalf("POST = %d %+v", code, got)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "# The coding agents") || !strings.Contains(s, "name: codex-1\n    executor: codex\n    model: o3") {
		t.Errorf("agents.yaml = %q, want the starter's header and name-first entries", s)
	}
	if code := e.get(t, q, &got); code != 200 || !got.Exists || got.Agents[1].Model != "o3" {
		t.Errorf("GET after save = %d %+v", code, got)
	}
	dup := map[string]any{"agents": []config.Agent{{Name: "a", Executor: "claude"}, {Name: "A", Executor: "codex"}}}
	if code := e.post(t, q, dup, &got); code != http.StatusBadRequest || !strings.Contains(got.Error, "twice") {
		t.Errorf("duplicate names = %d %+v", code, got)
	}
	if after, _ := os.ReadFile(file); string(after) != s {
		t.Error("a rejected save changed the file")
	}
	if code := e.get(t, "/api/repos/agents?path="+t.TempDir(), nil); code != http.StatusForbidden {
		t.Errorf("outside the scan = %d", code)
	}
}

func TestWorkerFileChecksSupervisorBlock(t *testing.T) {
	e := newEnv(t, Options{Getenv: func(string) string { return "" }})
	raw, _ := os.ReadFile(e.cfgPath)
	var res struct{ Problems string }
	if code := e.post(t, "/api/files/worker", map[string]any{"content": string(raw) + "supervisor:\n  provider: bard\n"}, &res); code != 200 || !strings.Contains(res.Problems, "supervisor.provider") {
		t.Errorf("bad provider = %d %+v", code, res)
	}
	if code := e.post(t, "/api/files/worker", map[string]any{"content": string(raw) + "supervisor:\n  provider: deepseek\n"}, &res); code != 200 || !strings.Contains(res.Problems, "DEEPSEEK_API_KEY") {
		t.Errorf("missing key = %d %+v", code, res)
	}
	if code := e.get(t, "/api/files/supervisor", nil); code != http.StatusBadRequest {
		t.Errorf("the separate supervisor file is gone: %d", code)
	}
}
