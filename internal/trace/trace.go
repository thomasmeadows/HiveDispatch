// Package trace records what HiveDispatch's agents do as a tree of runs —
// a supervisor turn and its model and tool calls, a ticket and its triage,
// executor run and CLI steps — and hands each run to an Exporter.
//
// A nil *Tracer and a nil *Span are no-ops, so call sites never check
// whether tracing is on. Tracing never fails the work it watches: Start and
// End return nothing to handle, and exporters drop what they cannot send.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Kind is a run's type, as LangSmith names them.
type Kind string

// The run kinds HiveDispatch uses.
const (
	KindChain Kind = "chain"
	KindLLM   Kind = "llm"
	KindTool  Kind = "tool"
)

// Run is one node of a trace as an exporter sees it. Start hands over the
// run with its inputs; End hands over the whole run again, now with its
// outputs, error and end time.
type Run struct {
	ID          string // UUIDv7
	TraceID     string // the root run's ID
	ParentID    string // "" for a root
	DottedOrder string // LangSmith's ordering key: the ancestry, root first
	Name        string
	Kind        Kind
	Start       time.Time
	End         time.Time // zero until ended
	Inputs      map[string]any
	Outputs     map[string]any
	Error       string
	Metadata    map[string]any
}

// Exporter is the boundary to a tracing backend. Implementations must be
// safe for concurrent use and must not block for long: Start and End are
// called inline with the work being traced.
type Exporter interface {
	Start(r Run)
	End(r Run)
	// Close sends anything still queued, giving up when ctx is done.
	Close(ctx context.Context) error
}

// DefaultMaxString caps every string a run carries.
const DefaultMaxString = 64 << 10

// Options shape what a Tracer sends.
type Options struct {
	HideInputs  bool             // send {} for inputs
	HideOutputs bool             // send {} for outputs (token usage is kept)
	MaxString   int              // 0 = DefaultMaxString
	Now         func() time.Time // nil = time.Now
}

// Tracer starts spans and hands finished runs to its exporter.
type Tracer struct {
	exp  Exporter
	opts Options
}

// New returns a Tracer that exports to exp.
func New(exp Exporter, o Options) *Tracer {
	if o.MaxString <= 0 {
		o.MaxString = DefaultMaxString
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Tracer{exp: exp, opts: o}
}

// Enabled reports whether spans go anywhere.
func (t *Tracer) Enabled() bool { return t != nil }

// Close flushes the exporter.
func (t *Tracer) Close(ctx context.Context) error {
	if t == nil {
		return nil
	}
	return t.exp.Close(ctx)
}

// Span is a run in progress.
type Span struct {
	t *Tracer

	mu    sync.Mutex
	run   Run
	usage map[string]any
	ended bool
}

type spanKey struct{}

// SpanFrom returns the span ctx carries, or nil.
func SpanFrom(ctx context.Context) *Span {
	sp, _ := ctx.Value(spanKey{}).(*Span)
	return sp
}

// Start begins a span now, as a child of the span ctx carries, and returns
// a context carrying the new span.
func (t *Tracer) Start(ctx context.Context, name string, kind Kind, inputs map[string]any) (context.Context, *Span) {
	if t == nil {
		return ctx, nil
	}
	return t.StartAt(ctx, name, kind, inputs, t.opts.Now())
}

// StartAt is Start with an explicit start time, for steps reported after
// the fact.
func (t *Tracer) StartAt(ctx context.Context, name string, kind Kind, inputs map[string]any, at time.Time) (context.Context, *Span) {
	if t == nil {
		return ctx, nil
	}
	at = at.UTC()
	id := newUUIDv7(at)
	r := Run{ID: id, TraceID: id, Name: name, Kind: kind, Start: at, Metadata: map[string]any{}}
	r.DottedOrder = dottedStamp(at) + id
	if p := SpanFrom(ctx); p != nil && p.t == t {
		p.mu.Lock()
		r.TraceID, r.ParentID = p.run.TraceID, p.run.ID
		r.DottedOrder = p.run.DottedOrder + "." + r.DottedOrder
		p.mu.Unlock()
	}
	if t.opts.HideInputs {
		r.Inputs = map[string]any{}
	} else {
		r.Inputs = t.clean(inputs)
	}
	sp := &Span{t: t, run: r}
	t.exp.Start(sp.snapshot())
	return context.WithValue(ctx, spanKey{}, sp), sp
}

// SetMetadata attaches a key to the run; it is sent when the span ends.
func (s *Span) SetMetadata(k string, v any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run.Metadata[k] = s.t.cleanValue(v)
}

// SetUsage records an LLM call's model ("vendor/model" or "model") and
// token counts, in the shape LangSmith prices runs by.
func (s *Span) SetUsage(model string, in, out int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if vendor, name, ok := strings.Cut(model, "/"); ok {
		s.run.Metadata["ls_provider"] = vendor
		s.run.Metadata["ls_model_name"] = name
	} else if model != "" {
		s.run.Metadata["ls_model_name"] = model
	}
	s.usage = map[string]any{"input_tokens": in, "output_tokens": out, "total_tokens": in + out}
	s.run.Metadata["usage_metadata"] = s.usage
}

// End finishes the span now. Only the first End counts.
func (s *Span) End(outputs map[string]any, err error) {
	if s == nil {
		return
	}
	s.EndAt(outputs, err, s.t.opts.Now())
}

// EndAt is End with an explicit end time.
func (s *Span) EndAt(outputs map[string]any, err error, at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.run.End = at.UTC()
	if s.t.opts.HideOutputs {
		s.run.Outputs = map[string]any{}
	} else {
		s.run.Outputs = s.t.clean(outputs)
	}
	if s.usage != nil {
		if s.run.Outputs == nil {
			s.run.Outputs = map[string]any{}
		}
		s.run.Outputs["usage_metadata"] = s.usage
	}
	if err != nil {
		s.run.Error = s.t.cleanValue(err.Error()).(string)
	}
	s.mu.Unlock()
	s.t.exp.End(s.snapshot())
}

// snapshot copies the run so the exporter never shares the span's maps.
func (s *Span) snapshot() Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.run
	r.Metadata = copyMap(s.run.Metadata)
	r.Inputs = copyMap(s.run.Inputs)
	r.Outputs = copyMap(s.run.Outputs)
	return r
}

func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// clean truncates every string in m and turns raw JSON into values, so
// what is exported is plain JSON no larger than the cap per string.
func (t *Tracer) clean(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = t.cleanValue(v)
	}
	return out
}

func (t *Tracer) cleanValue(v any) any {
	switch x := v.(type) {
	case string:
		return truncate(x, t.opts.MaxString)
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(x, &decoded) != nil {
			return truncate(string(x), t.opts.MaxString)
		}
		return t.cleanValue(decoded)
	case []byte:
		return truncate(string(x), t.opts.MaxString)
	case error:
		return truncate(x.Error(), t.opts.MaxString)
	case fmt.Stringer:
		return truncate(x.String(), t.opts.MaxString)
	case map[string]any:
		return t.clean(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = t.cleanValue(e)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = truncate(e, t.opts.MaxString)
		}
		return out
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}

// dottedStamp is LangSmith's %Y%m%dT%H%M%S%fZ: UTC, microseconds.
func dottedStamp(t time.Time) string {
	return fmt.Sprintf("%s%06dZ", t.Format("20060102T150405"), t.Nanosecond()/1000)
}

// newUUIDv7 is an RFC 9562 version 7 UUID for time at: 48 bits of Unix
// milliseconds, then random bits, so IDs sort by start time.
func newUUIDv7(at time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[6:]); err != nil {
		panic("trace: crypto/rand: " + err.Error()) // rand.Read does not fail on supported platforms
	}
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(at.UnixMilli()))
	copy(b[:6], ms[2:])
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
