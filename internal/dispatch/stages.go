package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/command-line-interfaces/claudecli"
	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// planSchema is the planning agent's answer.
const planSchema = `{"type":"object","properties":{"decision":{"type":"string","enum":["plan","needs_info"]},"plan":{"type":"string"},"question":{"type":"string"}},"required":["decision"],"additionalProperties":false}`

// reviewSchema is the review agent's answer.
const reviewSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["approve","request_changes"]},"summary":{"type":"string"}},"required":["verdict","summary"],"additionalProperties":false}`

// maxReviewDiff caps the diff put in a review prompt; the agent can read
// the files themselves.
const maxReviewDiff = 100 << 10

// handlePlanning plans a ticket in the Planning column: the planning agent
// reads the repository and either posts a plan (the ticket moves to Ready,
// and its coding agent skips triage) or asks one question (Needs Info).
func (d *Dispatcher) handlePlanning(ctx context.Context, t tracker.Ticket, repo config.RepoConfig, agent config.Agent) (Outcome, error) {
	now := d.now()
	if out, err := d.claim(ctx, t, now); out != "" || err != nil {
		return out, err
	}
	d.log().Info("planning ticket", "ticket", t.Key, "agent", agent.Name, "summary", t.Summary)
	defer d.release(ctx, t.Key)
	run, err := d.loadRun(ctx, t)
	if err != nil {
		return OutcomeSkipped, err
	}
	run.Agent = d.Cfg.MachineID + "/" + agent.Name
	ws, err := d.Workspaces.Prepare(ctx, repo, t.Key, run.Branch)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("plan %s: workspace: %w", t.Key, err)
	}
	var ans struct {
		Decision, Plan, Question string
	}
	if err := d.advise(ctx, agent, executor.Advice{
		Kind: executor.AdvicePlan, TicketKey: t.Key, Workspace: ws.Path, Model: agent.Model,
		Prompt: prompt.RenderPlanning(t, repo), Schema: planSchema,
	}, &ans); err != nil {
		d.event(ctx, run, "plan_failed", err.Error())
		return OutcomeSkipped, fmt.Errorf("plan %s: %w", t.Key, err)
	}
	switch {
	case ans.Decision == "needs_info" && strings.TrimSpace(ans.Question) != "":
		run.Planned = false
		d.save(ctx, run)
		d.comment(ctx, t.Key, reportPlanningNeedsInfo(agent.Name, ans.Question))
		d.transition(ctx, t.Key, tracker.StateNeedsInfo)
		d.event(ctx, run, "plan_needs_info", ans.Question)
		return d.finished("planning needs info", t.Key, OutcomeNeedsInfo, ans.Question), nil
	case ans.Decision == "plan" && strings.TrimSpace(ans.Plan) != "":
		run.Planned = true
		d.setPhase(ctx, run, state.PhasePlanned)
		d.comment(ctx, t.Key, reportPlan(agent.Name, ans.Plan))
		d.transition(ctx, t.Key, tracker.StateReady)
		d.event(ctx, run, "planned", "")
		return d.finished("planned", t.Key, OutcomePlanned, "plan posted"), nil
	}
	return OutcomeSkipped, fmt.Errorf("plan %s: the planning agent answered %q with no plan or question", t.Key, ans.Decision)
}

// handleReview reviews the pull request of a ticket In Review, once per
// version: the review agent's verdict is posted on the PR, and a request for
// changes sends the ticket back to Ready for its coding agent (until the
// rounds run out and a human takes over). An approval leaves it for a human
// to merge.
func (d *Dispatcher) handleReview(ctx context.Context, t tracker.Ticket, repo config.RepoConfig, agent config.Agent) (Outcome, error) {
	run, err := d.Store.Load(ctx, t.Key)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("load run %s: %w", t.Key, err)
	}
	if run.Branch == "" {
		return OutcomeSkipped, nil // not a pull request this worker opened
	}
	pr, err := d.Host.FindPR(ctx, repo.Name, run.Branch)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("review %s: find PR: %w", t.Key, err)
	}
	if pr == nil || (pr.HeadSHA != "" && pr.HeadSHA == run.ReviewedSHA) {
		return OutcomeSkipped, nil // merged or closed, or this version is reviewed
	}
	now := d.now()
	if out, err := d.claim(ctx, t, now); out != "" || err != nil {
		return out, err
	}
	d.log().Info("reviewing pull request", "ticket", t.Key, "agent", agent.Name, "pr", pr.URL)
	defer d.release(ctx, t.Key)
	ws, err := d.Workspaces.Prepare(ctx, repo, t.Key, run.Branch)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("review %s: workspace: %w", t.Key, err)
	}
	diff, err := d.Workspaces.Diff(ctx, ws)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("review %s: diff: %w", t.Key, err)
	}
	if len(diff) > maxReviewDiff {
		diff = diff[:maxReviewDiff] + "\n… (diff truncated; read the files for the rest)"
	}
	var ans struct {
		Verdict, Summary string
	}
	if err := d.advise(ctx, agent, executor.Advice{
		Kind: executor.AdviceReview, TicketKey: t.Key, Workspace: ws.Path, Model: agent.Model,
		Prompt: prompt.RenderReview(t, repo, run.Branch, pr.URL, diff), Schema: reviewSchema,
	}, &ans); err != nil {
		d.event(ctx, run, "review_failed", err.Error())
		return OutcomeSkipped, fmt.Errorf("review %s: %w", t.Key, err)
	}
	approved := ans.Verdict == "approve"
	if !approved && ans.Verdict != "request_changes" {
		return OutcomeSkipped, fmt.Errorf("review %s: unknown verdict %q", t.Key, ans.Verdict)
	}
	if err := d.Host.Review(ctx, repo.Name, pr.Number, reviewBody(agent.Name, approved, ans.Summary)); err != nil {
		d.log().Error("posting the review failed", "ticket", t.Key, "pr", pr.URL, "err", err)
	}
	run.ReviewedSHA = pr.HeadSHA
	if approved {
		d.save(ctx, run)
		d.comment(ctx, t.Key, reportApproved(agent.Name, pr.URL))
		d.event(ctx, run, "review_approved", ans.Summary)
		return d.finished("review passed", t.Key, OutcomeApproved, pr.URL), nil
	}
	run.ReviewRounds++
	if run.ReviewRounds > max(d.Cfg.MaxReviewRounds, 1) {
		d.save(ctx, run)
		d.comment(ctx, t.Key, reportReviewExhausted(agent.Name, pr.URL, ans.Summary, run.ReviewRounds-1))
		d.transition(ctx, t.Key, tracker.StateNeedsHuman)
		d.event(ctx, run, "review_exhausted", ans.Summary)
		return d.finished("work needs human", t.Key, OutcomeSentBack, "review rounds exhausted"), nil
	}
	run.ReviewFix, run.Review = true, ans.Summary
	d.save(ctx, run)
	d.comment(ctx, t.Key, reportChangesRequested(agent.Name, pr.URL, ans.Summary))
	d.transition(ctx, t.Key, tracker.StateReady)
	d.event(ctx, run, "review_changes_requested", ans.Summary)
	return d.finished("review asked for changes", t.Key, OutcomeSentBack, ans.Summary), nil
}

// advise runs a read-only agent under the run timeout and decodes its
// answer into out. A budget stop pauses new work, as for coding runs.
func (d *Dispatcher) advise(ctx context.Context, agent config.Agent, a executor.Advice, out any) error {
	ctx, cancel := context.WithTimeout(ctx, d.Cfg.RunTimeout)
	defer cancel()
	a.CodeWith = agent.CodeWith
	raw, err := d.executorFor(agent).Advise(ctx, a)
	if err != nil {
		var budget *claudecli.BudgetError
		if errors.As(err, &budget) {
			d.pauseFor(budget.ResetsAt)
		}
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parse answer: %w", err)
	}
	return nil
}
