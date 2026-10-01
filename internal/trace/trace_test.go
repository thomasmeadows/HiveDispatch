package trace_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/trace"
	"github.com/thomasmeadows/hivedispatch/internal/trace/fake"
)

func clock() func() time.Time {
	t := time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

func TestNilTracerIsNoop(t *testing.T) {
	var tr *trace.Tracer
	ctx, sp := tr.Start(context.Background(), "x", trace.KindChain, map[string]any{"a": 1})
	sp.SetMetadata("k", "v")
	sp.SetUsage("openai/gpt", 1, 2)
	sp.End(map[string]any{"b": 2}, errors.New("boom"))
	if trace.SpanFrom(ctx) != nil {
		t.Fatal("nil tracer put a span in the context")
	}
	if err := tr.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tr.Enabled() {
		t.Fatal("nil tracer is enabled")
	}
}

func TestParentingThroughContext(t *testing.T) {
	rec := &fake.Exporter{}
	tr := trace.New(rec, trace.Options{Now: clock()})
	ctx, root := tr.Start(context.Background(), "root", trace.KindChain, nil)
	cctx, child := tr.Start(ctx, "child", trace.KindLLM, nil)
	_, grand := tr.Start(cctx, "grand", trace.KindTool, nil)
	grand.End(nil, nil)
	child.End(nil, nil)
	root.End(map[string]any{"ok": true}, nil)

	runs := rec.Ended()
	if len(runs) != 3 {
		t.Fatalf("got %d ended runs, want 3", len(runs))
	}
	g, c, r := runs[0], runs[1], runs[2]
	if r.ParentID != "" || r.TraceID != r.ID {
		t.Fatalf("root: parent %q trace %q id %q", r.ParentID, r.TraceID, r.ID)
	}
	if c.ParentID != r.ID || c.TraceID != r.ID || g.ParentID != c.ID || g.TraceID != r.ID {
		t.Fatalf("bad links: %+v %+v %+v", r, c, g)
	}
	if !strings.HasPrefix(g.DottedOrder, c.DottedOrder+".") || !strings.HasPrefix(c.DottedOrder, r.DottedOrder+".") {
		t.Fatalf("dotted orders not nested: %q %q %q", r.DottedOrder, c.DottedOrder, g.DottedOrder)
	}
	if want := "20261001T120001123456Z" + r.ID; r.DottedOrder != want {
		t.Fatalf("root dotted order %q, want %q", r.DottedOrder, want)
	}
	if !r.End.After(r.Start) {
		t.Fatalf("root end %v not after start %v", r.End, r.Start)
	}
	if len(rec.Started()) != 3 {
		t.Fatalf("got %d started runs, want 3", len(rec.Started()))
	}
}

func TestIDsAreUUIDv7(t *testing.T) {
	rec := &fake.Exporter{}
	tr := trace.New(rec, trace.Options{})
	_, sp := tr.Start(context.Background(), "x", trace.KindChain, nil)
	sp.End(nil, nil)
	id := rec.Ended()[0].ID
	if len(id) != 36 || id[14] != '7' || !strings.ContainsRune("89ab", rune(id[19])) {
		t.Fatalf("id %q is not a UUIDv7", id)
	}
}

func TestEndTwiceExportsOnce(t *testing.T) {
	rec := &fake.Exporter{}
	tr := trace.New(rec, trace.Options{})
	_, sp := tr.Start(context.Background(), "x", trace.KindChain, nil)
	sp.End(nil, nil)
	sp.End(nil, errors.New("again"))
	if n := len(rec.Ended()); n != 1 {
		t.Fatalf("ended %d times", n)
	}
}

func TestErrorUsageAndMetadata(t *testing.T) {
	rec := &fake.Exporter{}
	tr := trace.New(rec, trace.Options{})
	_, sp := tr.Start(context.Background(), "chat", trace.KindLLM, nil)
	sp.SetMetadata("ticket", "HD-1")
	sp.SetUsage("deepseek/deepseek-flash", 10, 5)
	sp.End(map[string]any{"text": "hi"}, errors.New("boom"))
	r := rec.Ended()[0]
	if r.Error != "boom" {
		t.Fatalf("error %q", r.Error)
	}
	if r.Metadata["ticket"] != "HD-1" || r.Metadata["ls_provider"] != "deepseek" || r.Metadata["ls_model_name"] != "deepseek-flash" {
		t.Fatalf("metadata %v", r.Metadata)
	}
	u, ok := r.Outputs["usage_metadata"].(map[string]any)
	if !ok || u["input_tokens"] != 10 || u["output_tokens"] != 5 || u["total_tokens"] != 15 {
		t.Fatalf("usage %v", r.Outputs["usage_metadata"])
	}
}

func TestModelWithoutVendor(t *testing.T) {
	rec := &fake.Exporter{}
	tr := trace.New(rec, trace.Options{})
	_, sp := tr.Start(context.Background(), "chat", trace.KindLLM, nil)
	sp.SetUsage("fake", 1, 1)
	sp.End(nil, nil)
	m := rec.Ended()[0].Metadata
	if m["ls_model_name"] != "fake" || m["ls_provider"] != nil {
		t.Fatalf("metadata %v", m)
	}
}

func TestHideAndTruncate(t *testing.T) {
	rec := &fake.Exporter{}
	tr := trace.New(rec, trace.Options{HideInputs: true, MaxString: 8})
	_, sp := tr.Start(context.Background(), "x", trace.KindChain, map[string]any{"secret": "s"})
	sp.SetUsage("m", 1, 1)
	sp.End(map[string]any{
		"long":   "0123456789",
		"list":   []string{"abcdefghijk"},
		"nested": map[string]any{"s": "abcdefghijk"},
		"raw":    json.RawMessage(`{"path":"abcdefghijk"}`),
	}, nil)
	r := rec.Ended()[0]
	if len(r.Inputs) != 0 || len(rec.Started()[0].Inputs) != 0 {
		t.Fatalf("inputs not hidden: %v", r.Inputs)
	}
	const cut = "01234567…[truncated]"
	if r.Outputs["long"] != cut {
		t.Fatalf("long %q", r.Outputs["long"])
	}
	if l := r.Outputs["list"].([]any); l[0] != "abcdefgh…[truncated]" {
		t.Fatalf("list %v", l)
	}
	if n := r.Outputs["nested"].(map[string]any); n["s"] != "abcdefgh…[truncated]" {
		t.Fatalf("nested %v", n)
	}
	if raw := r.Outputs["raw"].(map[string]any); raw["path"] != "abcdefgh…[truncated]" {
		t.Fatalf("raw %v", raw)
	}
	if _, ok := r.Outputs["usage_metadata"]; !ok {
		t.Fatal("usage dropped")
	}

	rec2 := &fake.Exporter{}
	tr2 := trace.New(rec2, trace.Options{HideOutputs: true})
	_, sp2 := tr2.Start(context.Background(), "x", trace.KindLLM, map[string]any{"q": "visible"})
	sp2.SetUsage("m", 1, 1)
	sp2.End(map[string]any{"a": "hidden"}, nil)
	r2 := rec2.Ended()[0]
	if r2.Inputs["q"] != "visible" || r2.Outputs["a"] != nil {
		t.Fatalf("hide outputs: in %v out %v", r2.Inputs, r2.Outputs)
	}
	if _, ok := r2.Outputs["usage_metadata"]; !ok {
		t.Fatal("hiding outputs must keep token usage")
	}
}

func TestStartAtEndAt(t *testing.T) {
	rec := &fake.Exporter{}
	tr := trace.New(rec, trace.Options{})
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	_, sp := tr.StartAt(context.Background(), "step", trace.KindTool, nil, at)
	sp.EndAt(nil, nil, at.Add(3*time.Second))
	r := rec.Ended()[0]
	if !r.Start.Equal(at) || r.End.Sub(r.Start) != 3*time.Second {
		t.Fatalf("times %v %v", r.Start, r.End)
	}
	if !strings.HasPrefix(r.DottedOrder, "20261001T090000000000Z") {
		t.Fatalf("dotted order %q", r.DottedOrder)
	}
}

func TestCloseClosesExporter(t *testing.T) {
	rec := &fake.Exporter{}
	tr := trace.New(rec, trace.Options{})
	if err := tr.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !rec.Closed() {
		t.Fatal("exporter not closed")
	}
}

func TestSpanDottedOrder(t *testing.T) {
	var nilSpan *trace.Span
	if nilSpan.DottedOrder() != "" {
		t.Fatal("nil span has a dotted order")
	}
	rec := &fake.Exporter{}
	tr := trace.New(rec, trace.Options{})
	_, sp := tr.Start(context.Background(), "x", trace.KindChain, nil)
	got := sp.DottedOrder()
	sp.End(nil, nil)
	if got == "" || got != rec.Ended()[0].DottedOrder {
		t.Fatalf("DottedOrder %q, run %q", got, rec.Ended()[0].DottedOrder)
	}
}
