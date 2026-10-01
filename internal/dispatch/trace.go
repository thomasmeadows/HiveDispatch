package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/trace"
)

// tracedExecutor records each executor call as a run, and the CLI's steps
// as its children.
type tracedExecutor struct {
	inner  executor.Executor
	tracer *trace.Tracer
	agent  config.Agent
}

var _ executor.Executor = tracedExecutor{}

// Name implements executor.Executor.
func (e tracedExecutor) Name() string { return e.inner.Name() }

func (e tracedExecutor) start(ctx context.Context, name string, inputs map[string]any) (context.Context, *trace.Span) {
	ctx, span := e.tracer.Start(ctx, name, trace.KindChain, inputs)
	span.SetMetadata("executor", e.inner.Name())
	span.SetMetadata("agent", e.agent.Name)
	if e.agent.Model != "" {
		span.SetMetadata("model", e.agent.Model)
	}
	return ctx, span
}

// Plan implements executor.Executor.
func (e tracedExecutor) Plan(ctx context.Context, t executor.Task) (executor.Footprint, error) {
	ctx, span := e.start(ctx, "plan footprint", map[string]any{"prompt": t.Prompt})
	fp, err := e.inner.Plan(ctx, t)
	span.End(map[string]any{"files": fp.Files}, err)
	return fp, err
}

// Advise implements executor.Executor.
func (e tracedExecutor) Advise(ctx context.Context, a executor.Advice) (json.RawMessage, error) {
	ctx, span := e.start(ctx, "advise "+string(a.Kind), map[string]any{"prompt": a.Prompt, "schema": json.RawMessage(a.Schema)})
	raw, err := e.inner.Advise(ctx, a)
	span.End(map[string]any{"answer": raw}, err)
	return raw, err
}

// Run implements executor.Executor.
func (e tracedExecutor) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	ctx, span := e.start(ctx, "run "+e.inner.Name(), map[string]any{
		"prompt": t.Prompt, "resume": t.ResumeToken != "", "step_budget": t.StepBudget,
	})
	res, err := e.inner.Run(ctx, t)
	if !res.StepsTraced { // a graph that traced itself has its own, finer tree
		for _, st := range res.Steps {
			e.step(ctx, st, res.Usage.Model)
		}
	}
	if u := res.Usage; u.InputTokens > 0 || u.OutputTokens > 0 {
		span.SetUsage(u.Model, u.InputTokens, u.OutputTokens)
	} else if u.Model != "" {
		span.SetMetadata("ls_model_name", u.Model)
	}
	if res.Usage.CostUSD > 0 {
		span.SetMetadata("cost_usd", res.Usage.CostUSD)
	}
	span.SetMetadata("steps", len(res.Steps))
	spanErr := err
	if spanErr == nil && res.Status == executor.StatusFailed {
		// A failed run is not an error to the caller, but it is one to read
		// in the trace.
		spanErr = errors.New(string(res.StopCause) + ": " + res.Summary)
	}
	span.End(map[string]any{
		"status": string(res.Status), "stop_cause": string(res.StopCause), "summary": res.Summary,
		"question": res.Question, "changed_files": res.ChangedFiles,
	}, spanErr)
	return res, err
}

// step records one CLI step under the run span ctx carries. A step whose
// result never arrived ends when the run did.
func (e tracedExecutor) step(ctx context.Context, st executor.Step, model string) {
	kind := trace.KindTool
	inputs := map[string]any{"input": st.Input}
	outputs := map[string]any{"output": st.Output}
	if st.Kind == executor.StepMessage {
		kind = trace.KindLLM
		inputs = nil
		outputs = map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": st.Output}}}}
	}
	if st.Start.IsZero() {
		st.Start = time.Now()
	}
	_, span := e.tracer.StartAt(ctx, st.Name, kind, inputs, st.Start)
	if kind == trace.KindLLM && model != "" {
		span.SetMetadata("ls_model_name", model)
	}
	end := st.End
	var err error
	switch {
	case end.IsZero():
		end = time.Now()
		if end.Before(st.Start) {
			end = st.Start
		}
		err = errors.New("no result: the run ended first")
	case st.IsError:
		err = errors.New("the tool reported an error")
	}
	span.EndAt(outputs, err, end)
}
