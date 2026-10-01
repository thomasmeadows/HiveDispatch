package langsmith

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/trace"
)

type batch struct {
	Post  []map[string]any `json:"post"`
	Patch []map[string]any `json:"patch"`
}

type server struct {
	mu      sync.Mutex
	batches []batch
	headers []http.Header
	status  []int // statuses to answer with, in order; then 200
	*httptest.Server
}

func newServer(t *testing.T, statuses ...int) *server {
	s := &server{status: statuses}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/runs/batch" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		code := http.StatusOK
		if len(s.status) > 0 {
			code, s.status = s.status[0], s.status[1:]
		}
		if code == http.StatusOK || code == http.StatusConflict {
			var b batch
			if err := json.Unmarshal(body, &b); err != nil {
				t.Errorf("bad body %s: %v", body, err)
			}
			s.batches = append(s.batches, b)
			s.headers = append(s.headers, r.Header.Clone())
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) all() ([]map[string]any, []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var posts, patches []map[string]any
	for _, b := range s.batches {
		posts = append(posts, b.Post...)
		patches = append(patches, b.Patch...)
	}
	return posts, patches
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func cfg(s *server) Config {
	return Config{Endpoint: s.URL, APIKey: "k", Project: "proj", Interval: time.Hour, BatchSize: 100, QueueSize: 100, Log: quiet(), RetryWait: time.Millisecond}
}

func TestMergesStartAndEndIntoOnePost(t *testing.T) {
	s := newServer(t)
	c := cfg(s)
	c.WorkspaceID = "ws"
	exp := New(c)
	tr := trace.New(exp, trace.Options{})
	ctx, root := tr.Start(context.Background(), "ticket", trace.KindChain, map[string]any{"key": "HD-1"})
	_, child := tr.Start(ctx, "chat", trace.KindLLM, nil)
	child.SetUsage("deepseek/deepseek-flash", 3, 4)
	child.End(map[string]any{"text": "hi"}, nil)
	root.End(nil, errors.New("boom"))
	if err := exp.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	posts, patches := s.all()
	if len(posts) != 2 || len(patches) != 0 {
		t.Fatalf("posts %d patches %d, want 2 and 0", len(posts), len(patches))
	}
	r, c2 := posts[0], posts[1]
	if r["name"] != "ticket" || r["run_type"] != "chain" || r["session_name"] != "proj" || r["error"] != "boom" {
		t.Fatalf("root %v", r)
	}
	if r["trace_id"] != r["id"] || r["parent_run_id"] != nil || r["end_time"] == nil {
		t.Fatalf("root links %v", r)
	}
	if r["inputs"].(map[string]any)["key"] != "HD-1" {
		t.Fatalf("root inputs %v", r["inputs"])
	}
	if c2["parent_run_id"] != r["id"] || c2["trace_id"] != r["id"] || !strings.HasPrefix(c2["dotted_order"].(string), r["dotted_order"].(string)+".") {
		t.Fatalf("child links %v", c2)
	}
	md := c2["extra"].(map[string]any)["metadata"].(map[string]any)
	if md["ls_model_name"] != "deepseek-flash" || md["ls_provider"] != "deepseek" {
		t.Fatalf("child metadata %v", md)
	}
	if u := c2["outputs"].(map[string]any)["usage_metadata"].(map[string]any); u["total_tokens"] != float64(7) {
		t.Fatalf("usage %v", u)
	}
	h := s.headers[0]
	if h.Get("x-api-key") != "k" || h.Get("x-tenant-id") != "ws" || h.Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", h)
	}
}

func TestLongRunPostsThenPatches(t *testing.T) {
	s := newServer(t)
	exp := New(cfg(s))
	tr := trace.New(exp, trace.Options{})
	_, sp := tr.Start(context.Background(), "ticket", trace.KindChain, map[string]any{"big": "input"})
	exp.Flush(context.Background())
	sp.End(map[string]any{"out": "done"}, nil)
	if err := exp.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	posts, patches := s.all()
	if len(posts) != 1 || len(patches) != 1 {
		t.Fatalf("posts %d patches %d", len(posts), len(patches))
	}
	if posts[0]["end_time"] != nil || posts[0]["outputs"] != nil {
		t.Fatalf("post carries an end: %v", posts[0])
	}
	p := patches[0]
	if p["id"] != posts[0]["id"] || p["dotted_order"] != posts[0]["dotted_order"] || p["trace_id"] != posts[0]["trace_id"] || p["end_time"] == nil {
		t.Fatalf("patch %v", p)
	}
	if p["outputs"].(map[string]any)["out"] != "done" {
		t.Fatalf("patch outputs %v", p["outputs"])
	}
	if p["inputs"] != nil {
		t.Fatalf("patch resends inputs: %v", p["inputs"])
	}
}

func TestBatchSizeTriggersFlush(t *testing.T) {
	s := newServer(t)
	c := cfg(s)
	c.BatchSize = 2
	exp := New(c)
	tr := trace.New(exp, trace.Options{})
	_, a := tr.Start(context.Background(), "a", trace.KindChain, nil)
	a.End(nil, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if posts, _ := s.all(); len(posts) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a full batch was not flushed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := exp.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOverflowDrops(t *testing.T) {
	s := newServer(t)
	c := cfg(s)
	c.QueueSize = 2
	c.BatchSize = 1000
	exp := New(c)
	tr := trace.New(exp, trace.Options{})
	for range 5 {
		_, sp := tr.Start(context.Background(), "x", trace.KindChain, nil)
		sp.End(nil, nil)
	}
	if err := exp.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exp.Dropped() != 8 {
		t.Fatalf("dropped %d, want 8", exp.Dropped())
	}
	if posts, _ := s.all(); len(posts) != 1 {
		t.Fatalf("posts %d, want 1", len(posts))
	}
}

func TestRetriesOnceThenDrops(t *testing.T) {
	s := newServer(t, http.StatusBadGateway)
	exp := New(cfg(s))
	tr := trace.New(exp, trace.Options{})
	_, sp := tr.Start(context.Background(), "x", trace.KindChain, nil)
	sp.End(nil, nil)
	exp.Flush(context.Background())
	if posts, _ := s.all(); len(posts) != 1 {
		t.Fatalf("retry did not deliver: %d", len(posts))
	}

	s2 := newServer(t, http.StatusInternalServerError, http.StatusInternalServerError)
	exp2 := New(cfg(s2))
	tr2 := trace.New(exp2, trace.Options{})
	_, sp2 := tr2.Start(context.Background(), "x", trace.KindChain, nil)
	sp2.End(nil, nil)
	if err := exp2.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exp2.Dropped() != 2 { // its start and its end
		t.Fatalf("dropped %d, want 2", exp2.Dropped())
	}
}

func TestConflictIsSuccess(t *testing.T) {
	s := newServer(t, http.StatusConflict)
	exp := New(cfg(s))
	tr := trace.New(exp, trace.Options{})
	_, sp := tr.Start(context.Background(), "x", trace.KindChain, nil)
	sp.End(nil, nil)
	if err := exp.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exp.Dropped() != 0 || len(s.batches) != 1 {
		t.Fatalf("dropped %d batches %d", exp.Dropped(), len(s.batches))
	}
}

func TestCloseHonoursContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	t.Cleanup(func() { close(block); srv.Close() })
	exp := New(Config{Endpoint: srv.URL, APIKey: "k", Project: "p", Interval: time.Hour, Log: quiet()})
	tr := trace.New(exp, trace.Options{})
	_, sp := tr.Start(context.Background(), "x", trace.KindChain, nil)
	sp.End(nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = exp.Close(ctx)
	if time.Since(start) > 2*time.Second {
		t.Fatal("Close ignored its context")
	}
}

func TestStartAfterCloseIsDropped(t *testing.T) {
	s := newServer(t)
	exp := New(cfg(s))
	if err := exp.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr := trace.New(exp, trace.Options{})
	_, sp := tr.Start(context.Background(), "late", trace.KindChain, nil)
	sp.End(nil, nil)
	if posts, _ := s.all(); len(posts) != 0 {
		t.Fatalf("sent after close: %v", posts)
	}
}

func TestFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		enabled bool
		wantErr bool
		project string
	}{
		{"off by default", map[string]string{"LANGSMITH_API_KEY": "k"}, false, false, ""},
		{"on", map[string]string{"LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "k"}, true, false, "hivedispatch"},
		{"project", map[string]string{"LANGSMITH_TRACING": "1", "LANGSMITH_API_KEY": "k", "LANGSMITH_PROJECT": "mine"}, true, false, "mine"},
		{"no key", map[string]string{"LANGSMITH_TRACING": "true"}, false, true, ""},
		{"false", map[string]string{"LANGSMITH_TRACING": "false", "LANGSMITH_API_KEY": "k"}, false, false, ""},
		{"legacy names", map[string]string{"LANGCHAIN_TRACING_V2": "true", "LANGCHAIN_API_KEY": "k", "LANGCHAIN_PROJECT": "old"}, true, false, "old"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			getenv := func(k string) string { return c.env[k] }
			tr, err := FromEnv(getenv, quiet())
			if (err != nil) != c.wantErr {
				t.Fatalf("err %v", err)
			}
			if tr.Enabled() != c.enabled {
				t.Fatalf("enabled %v", tr.Enabled())
			}
			if err := tr.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := Describe(getenv)
			if c.enabled && !strings.Contains(got, `project "`+c.project+`"`) {
				t.Fatalf("describe %q", got)
			}
			if strings.Contains(got, "k\"") || strings.Contains(got, "=k") {
				t.Fatalf("describe leaks the key: %q", got)
			}
		})
	}
}

func TestFromEnvHides(t *testing.T) {
	s := newServer(t)
	env := map[string]string{
		"LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "k", "LANGSMITH_ENDPOINT": s.URL + "/",
		"LANGSMITH_HIDE_INPUTS": "true", "LANGSMITH_HIDE_OUTPUTS": "true",
	}
	tr, err := FromEnv(func(k string) string { return env[k] }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	_, sp := tr.Start(context.Background(), "x", trace.KindChain, map[string]any{"a": "secret"})
	sp.End(map[string]any{"b": "secret"}, nil)
	if err := tr.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	posts, _ := s.all()
	if len(posts) != 1 {
		t.Fatalf("posts %d", len(posts))
	}
	b, _ := json.Marshal(posts[0])
	if strings.Contains(string(b), "secret") {
		t.Fatalf("hidden content sent: %s", b)
	}
}
