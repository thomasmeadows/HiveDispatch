// Package langsmith exports traces to LangSmith through its batch runs API
// (POST /runs/batch, the endpoint LangSmith's own SDKs use).
//
// Runs are queued and sent from a background goroutine, so tracing never
// blocks the work it records. When LangSmith is down or slow, runs are
// dropped and counted rather than held without bound.
package langsmith

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/trace"
)

// DefaultEndpoint is LangSmith's US API.
const DefaultEndpoint = "https://api.smith.langchain.com"

// DefaultProject is the project runs go to when none is set.
const DefaultProject = "hivedispatch"

// Config wires an Exporter. Zero values take the defaults.
type Config struct {
	Endpoint    string // API base; DefaultEndpoint
	APIKey      string // x-api-key
	WorkspaceID string // x-tenant-id, when a key spans several workspaces
	Project     string // session_name; DefaultProject
	Client      *http.Client
	Interval    time.Duration // how often to flush; 1s
	BatchSize   int           // flush as soon as this many operations wait; 100
	QueueSize   int           // operations held before dropping; 10000
	Timeout     time.Duration // per request from the background flusher; 10s
	RetryWait   time.Duration // before the one retry; 1s
	Log         *slog.Logger  // nil = slog.Default()
}

type op struct {
	run   trace.Run
	ended bool
}

// Exporter is a trace.Exporter that posts to LangSmith.
type Exporter struct {
	cfg Config

	mu      sync.Mutex
	queue   []op
	dropped int
	closed  bool

	sendMu sync.Mutex // one batch on the wire at a time
	wake   chan struct{}
	stop   chan struct{}
	done   chan struct{}
}

var _ trace.Exporter = (*Exporter)(nil)

// New starts an Exporter's background flusher. Close stops it.
func New(cfg Config) *Exporter {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Project == "" {
		cfg.Project = DefaultProject
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 10000
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.RetryWait <= 0 {
		cfg.RetryWait = time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	e := &Exporter{cfg: cfg, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go e.loop()
	return e
}

// Start implements trace.Exporter.
func (e *Exporter) Start(r trace.Run) { e.enqueue(op{run: r}) }

// End implements trace.Exporter.
func (e *Exporter) End(r trace.Run) { e.enqueue(op{run: r, ended: true}) }

func (e *Exporter) enqueue(o op) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || len(e.queue) >= e.cfg.QueueSize {
		e.dropped++
		return
	}
	e.queue = append(e.queue, o)
	if len(e.queue) >= e.cfg.BatchSize {
		select {
		case e.wake <- struct{}{}:
		default:
		}
	}
}

// Dropped is how many operations (a run's start or its end) were never
// delivered.
func (e *Exporter) Dropped() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dropped
}

func (e *Exporter) loop() {
	defer close(e.done)
	tick := time.NewTicker(e.cfg.Interval)
	defer tick.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-tick.C:
		case <-e.wake:
		}
		ctx, cancel := context.WithTimeout(context.Background(), e.cfg.Timeout)
		e.Flush(ctx)
		cancel()
	}
}

// Close stops the flusher and sends what is queued, giving up when ctx is
// done. Runs started after Close are dropped.
func (e *Exporter) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.mu.Unlock()
	close(e.stop)
	select {
	case <-e.done:
	case <-ctx.Done():
	}
	e.Flush(ctx)
	if n := e.Dropped(); n > 0 {
		e.cfg.Log.Warn("tracing: some runs never reached LangSmith", "dropped", n)
	}
	return nil
}

// Flush sends everything queued now, in batches.
func (e *Exporter) Flush(ctx context.Context) {
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	e.mu.Lock()
	ops := e.queue
	e.queue = nil
	e.mu.Unlock()
	for len(ops) > 0 && ctx.Err() == nil {
		n := min(len(ops), e.cfg.BatchSize)
		e.send(ctx, ops[:n])
		ops = ops[n:]
	}
	if len(ops) > 0 {
		e.mu.Lock()
		e.dropped += len(ops)
		e.mu.Unlock()
	}
}

// wireRun is a run in LangSmith's batch format.
type wireRun struct {
	ID          string         `json:"id"`
	TraceID     string         `json:"trace_id"`
	DottedOrder string         `json:"dotted_order"`
	ParentRunID string         `json:"parent_run_id,omitempty"`
	Name        string         `json:"name,omitempty"`
	RunType     string         `json:"run_type,omitempty"`
	StartTime   string         `json:"start_time,omitempty"`
	EndTime     string         `json:"end_time,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	Outputs     map[string]any `json:"outputs,omitempty"`
	Error       string         `json:"error,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
	SessionName string         `json:"session_name,omitempty"`
}

const timeFormat = "2006-01-02T15:04:05.000000Z"

func (e *Exporter) wire(r trace.Run, ended, withStart bool) wireRun {
	w := wireRun{ID: r.ID, TraceID: r.TraceID, DottedOrder: r.DottedOrder, ParentRunID: r.ParentID, SessionName: e.cfg.Project}
	if withStart {
		w.Name, w.RunType = r.Name, string(r.Kind)
		w.StartTime = r.Start.UTC().Format(timeFormat)
		w.Inputs = r.Inputs
		if w.Inputs == nil {
			w.Inputs = map[string]any{}
		}
	}
	if ended {
		w.EndTime = r.End.UTC().Format(timeFormat)
		w.Outputs = r.Outputs
		if w.Outputs == nil {
			w.Outputs = map[string]any{}
		}
		w.Error = r.Error
	}
	if len(r.Metadata) > 0 {
		w.Extra = map[string]any{"metadata": r.Metadata}
	}
	return w
}

// send posts one batch. A run started and ended within the batch goes as
// one post; an end whose start went earlier goes as a patch.
func (e *Exporter) send(ctx context.Context, ops []op) {
	body := struct {
		Post  []wireRun `json:"post"`
		Patch []wireRun `json:"patch"`
	}{Post: []wireRun{}, Patch: []wireRun{}}
	posted := map[string]int{} // run id -> index in Post
	for _, o := range ops {
		if !o.ended {
			posted[o.run.ID] = len(body.Post)
			body.Post = append(body.Post, e.wire(o.run, false, true))
			continue
		}
		if i, ok := posted[o.run.ID]; ok {
			body.Post[i] = e.wire(o.run, true, true)
			continue
		}
		body.Patch = append(body.Patch, e.wire(o.run, true, false))
	}
	raw, err := json.Marshal(body)
	if err != nil {
		e.fail(len(ops), err)
		return
	}
	err = e.post(ctx, raw)
	if err != nil && ctx.Err() == nil {
		t := time.NewTimer(e.cfg.RetryWait)
		select {
		case <-t.C:
			err = e.post(ctx, raw)
		case <-ctx.Done():
			t.Stop()
		}
	}
	if err != nil {
		e.fail(len(ops), err)
	}
}

func (e *Exporter) fail(n int, err error) {
	e.mu.Lock()
	e.dropped += n
	total := e.dropped
	e.mu.Unlock()
	e.cfg.Log.Warn("tracing: LangSmith batch not delivered", "err", err, "operations", n, "dropped_total", total)
}

func (e *Exporter) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.Endpoint+"/runs/batch", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", e.cfg.APIKey)
	if e.cfg.WorkspaceID != "" {
		req.Header.Set("x-tenant-id", e.cfg.WorkspaceID)
	}
	resp, err := e.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	// 409: these runs were already ingested, by an earlier attempt.
	if resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
