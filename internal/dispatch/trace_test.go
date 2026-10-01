package dispatch

import (
	"errors"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/trace"
	tracefake "github.com/thomasmeadows/hivedispatch/internal/trace/fake"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func (h *harness) traced() *tracefake.Exporter {
	rec := &tracefake.Exporter{}
	h.d.Tracer = trace.New(rec, trace.Options{})
	return rec
}

func TestCodingRunIsTraced(t *testing.T) {
	h := newHarness(t)
	rec := h.traced()
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	h.ex.Default = executor.Result{
		Status: executor.StatusCompleted, Summary: "did it", ChangedFiles: []string{"a.go"},
		Steps: []executor.Step{
			{Kind: executor.StepMessage, Name: "assistant", Output: "Looking.", Start: t0, End: t0},
			{Kind: executor.StepTool, Name: "Bash", Input: `{"command":"false"}`, Output: "exit 1", IsError: true, Start: t0, End: t0.Add(2 * time.Second)},
			{Kind: executor.StepTool, Name: "Edit", Input: `{}`, Start: t0.Add(3 * time.Second)}, // never answered
		},
		Usage: executor.Usage{Model: "claude-opus-5-5", InputTokens: 40, OutputTokens: 9, CostUSD: 0.25},
	}
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("outcome %s", out)
	}

	roots := rec.Named("coding HIVE-1")
	if len(roots) != 1 {
		t.Fatalf("ticket spans: %+v", rec.Ended())
	}
	root := roots[0]
	if root.ParentID != "" || root.Outputs["outcome"] != string(OutcomeCompleted) {
		t.Fatalf("root %+v", root)
	}
	for k, v := range map[string]string{"ticket": "HIVE-1", "repo": "o/r", "agent": "default", "role": "coding", "machine_id": "worker-a"} {
		if root.Metadata[k] != v {
			t.Errorf("root metadata %s = %v, want %s (all: %v)", k, root.Metadata[k], v, root.Metadata)
		}
	}
	kids := rec.Children(root.ID)
	if len(kids) != 2 || kids[0].Name != "triage" || kids[1].Name != "run fake" {
		t.Fatalf("root children %+v", kids)
	}
	if kids[0].Outputs["kind"] != "dispatch" {
		t.Errorf("triage outputs %v", kids[0].Outputs)
	}
	run := kids[1]
	if run.Outputs["status"] != "completed" || run.Outputs["summary"] != "did it" || run.Inputs["prompt"] != "do the thing" {
		t.Errorf("run span %+v", run)
	}
	if run.Metadata["cost_usd"] != 0.25 || run.Metadata["ls_model_name"] != "claude-opus-5-5" {
		t.Errorf("run metadata %v", run.Metadata)
	}
	steps := rec.Children(run.ID)
	if len(steps) != 3 {
		t.Fatalf("steps %+v", steps)
	}
	if steps[0].Kind != trace.KindLLM || steps[0].Name != "assistant" {
		t.Errorf("message step %+v", steps[0])
	}
	bash := steps[1]
	if bash.Kind != trace.KindTool || bash.Error == "" || !bash.Start.Equal(t0) || bash.End.Sub(bash.Start) != 2*time.Second || bash.Outputs["output"] != "exit 1" {
		t.Errorf("bash step %+v", bash)
	}
	if edit := steps[2]; edit.Error == "" || edit.End.Before(edit.Start) {
		t.Errorf("unanswered step %+v", edit)
	}
}

func TestFailedRunIsTraced(t *testing.T) {
	h := newHarness(t)
	rec := h.traced()
	h.ex.Err["HIVE-1"] = errors.New("cli vanished")
	h.handle(t)
	runs := rec.Named("run fake")
	if len(runs) != 1 || runs[0].Error != "cli vanished" {
		t.Fatalf("run span %+v", runs)
	}
	if root := rec.Named("coding HIVE-1"); len(root) != 1 || root[0].Outputs["outcome"] != string(OutcomeFailed) {
		t.Fatalf("root %+v", root)
	}
}

func TestPlanningIsTraced(t *testing.T) {
	h := newHarness(t)
	rec := h.traced()
	h.setAgents(planner, coder)
	h.tr.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one", Status: string(tracker.StatePlanning)})
	h.once(t)
	roots := rec.Named("planning HIVE-1")
	if len(roots) != 1 || roots[0].Metadata["agent"] != "planner" {
		t.Fatalf("roots %+v", roots)
	}
	kids := rec.Children(roots[0].ID)
	if len(kids) != 1 || kids[0].Name != "advise plan" {
		t.Fatalf("children %+v", kids)
	}
	ans, _ := kids[0].Outputs["answer"].(map[string]any)
	if ans["decision"] != "plan" {
		t.Errorf("advice outputs %v", kids[0].Outputs)
	}
}

func TestNoTracerChangesNothing(t *testing.T) {
	h := newHarness(t)
	if h.d.executorFor(coder) != h.ex {
		t.Fatal("without a tracer the executor must not be wrapped")
	}
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("outcome %s", out)
	}
}
