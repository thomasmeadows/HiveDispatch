package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/command-line-interfaces/claudecli"
	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	"github.com/thomasmeadows/hivedispatch/internal/gitops"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/trace"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// releaseTimeout bounds the cleanup calls made after ctx may be cancelled.
const releaseTimeout = 30 * time.Second

// Handle takes one ticket from polled to a terminal state. Every path that
// claims the ticket also releases it and leaves a comment explaining what
// happened.
//
// Handle waits for a free agent of the ticket's repository (the one its
// hive:agent:<name> label names, if any); the poll loop reserves one itself
// and calls handle.
func (d *Dispatcher) Handle(ctx context.Context, t tracker.Ticket) (Outcome, error) {
	repo, ok := d.repoFor(t.Key)
	if !ok {
		d.log().Warn("no repo configured for ticket", "ticket", t.Key)
		return OutcomeSkipped, nil
	}
	cands, err := candidates(repo, t, config.RoleCoding)
	if err != nil {
		d.log().Warn("ticket skipped", "ticket", t.Key, "err", err)
		return OutcomeSkipped, nil
	}
	if len(cands) == 0 {
		d.log().Warn("ticket skipped: the repository has no coding agent", "ticket", t.Key)
		return OutcomeSkipped, nil
	}
	agent, ok := d.takeAgent(ctx, repo, cands, true)
	if !ok {
		return OutcomeSkipped, ctx.Err()
	}
	defer d.releaseAgent(repo, agent)
	return d.handle(ctx, t, repo, agent)
}

// handle works t with agent, which the caller has reserved, in the way the
// agent's role works its column.
func (d *Dispatcher) handle(ctx context.Context, t tracker.Ticket, repo config.RepoConfig, agent config.Agent) (out Outcome, err error) {
	role := roleOf(agent)
	ctx, span := d.Tracer.Start(ctx, role+" "+t.Key, trace.KindChain, map[string]any{"ticket": t.Key, "summary": t.Summary})
	for k, v := range map[string]string{"ticket": t.Key, "repo": repo.Name, "agent": agent.Name, "role": role, "machine_id": d.Cfg.MachineID, "executor": agent.Executor} {
		span.SetMetadata(k, v)
	}
	defer func() { span.End(map[string]any{"outcome": string(out)}, err) }()
	switch role {
	case config.RolePlanning:
		return d.handlePlanning(ctx, t, repo, agent)
	case config.RoleReview:
		return d.handleReview(ctx, t, repo, agent)
	}
	return d.handleCoding(ctx, t, repo, agent)
}

// handleCoding implements a Ready ticket: triage (unless it was planned or
// sent back by a review), run the coding agent, open or update the PR.
func (d *Dispatcher) handleCoding(ctx context.Context, t tracker.Ticket, repo config.RepoConfig, agent config.Agent) (Outcome, error) {
	now := d.now()
	if out, err := d.claim(ctx, t, now); out != "" || err != nil {
		return out, err
	}
	d.log().Info("picked up ticket", "ticket", t.Key, "agent", agent.Name, "summary", t.Summary)
	defer d.release(ctx, t.Key)

	run, err := d.loadRun(ctx, t)
	if err != nil {
		return OutcomeSkipped, err
	}
	priorPhase := run.Phase // where the last run stopped, before we overwrite it
	run.Agent = d.Cfg.MachineID + "/" + agent.Name
	if run.ResumeToken != "" && run.Executor != "" && run.Executor != agent.Executor {
		// A session id means nothing to another CLI: this agent starts over.
		d.log().Info("previous session belongs to another executor; starting fresh", "ticket", t.Key, "was", run.Executor, "agent", agent.Name)
		run.ResumeToken = ""
	}
	d.setPhase(ctx, run, state.PhaseClaimed)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.heartbeat(runCtx, t.Key, cancel)

	// The worktree comes first: triage needs a checkout to inspect, and a
	// dispatched run works in the same one.
	ws, err := d.Workspaces.Prepare(ctx, repo, t.Key, run.Branch)
	if err != nil {
		run.Attempts++
		res := executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseError, Summary: "workspace: " + err.Error()}
		return d.finishFailed(ctx, t, run, res, false), nil
	}

	// A run that completed and pushed but never got its PR died between
	// pushed and pr_opened: finish it, do not redo the work.
	if run.LastStatus == string(executor.StatusCompleted) && priorPhase == state.PhasePushed && run.PRURL == "" {
		d.event(ctx, run, "finish", "pushed branch without a PR")
		return d.finishCompleted(d.background(ctx), t, repo, run, executor.Result{Status: executor.StatusCompleted, Summary: "Finished a previous run: the branch was pushed but the pull request had not been opened."}, true)
	}

	// Address a review, resume after a human reply, start from a plan, or
	// triage afresh.
	var taskPrompt string
	switch {
	case run.ReviewFix:
		taskPrompt = prompt.RenderReviewFix(t, repo, run.Branch, run.Review)
		run.ReviewFix, run.Review = false, ""
		d.event(ctx, run, "review_fix", "")
	case run.Planned && !d.canResume(t, run):
		taskPrompt = prompt.Render(t, repo, run.Branch)
		d.event(ctx, run, "from_plan", "")
	case d.canResume(t, run):
		taskPrompt = prompt.RenderResume(t, run.QuestionAt, isOurs)
		d.event(ctx, run, "resume", "")
	default:
		dec, err := d.triage(ctx, triage.Input{
			Ticket: t, Repo: repo, Branch: run.Branch, RepoPath: ws.Path,
			Attempts: run.Attempts, LastStopCause: run.StopCause,
		})
		if err != nil {
			var budget *claudecli.BudgetError
			if errors.As(err, &budget) {
				d.pauseFor(budget.ResetsAt)
			}
			return OutcomeSkipped, fmt.Errorf("triage %s: %w", t.Key, err)
		}
		switch dec.Kind {
		case triage.KindNeedsInfo:
			run.LastStatus = string(executor.StatusNeedsInput)
			run.QuestionAt = now
			run.Phase = state.PhaseBlocked // waiting on a human
			d.save(ctx, run)
			d.comment(ctx, t.Key, reportTriageNeedsInfo(dec.Question))
			d.transition(ctx, t.Key, tracker.StateNeedsInfo)
			d.event(ctx, run, "triage_needs_info", dec.Question)
			return d.finished("work needs info", t.Key, OutcomeNeedsInfo, dec.Question), nil
		case triage.KindReject:
			run.LastStatus = "rejected"
			d.setPhase(ctx, run, state.PhaseDone)
			d.comment(ctx, t.Key, reportRejected(dec.Reason))
			d.transition(ctx, t.Key, tracker.StateNeedsHuman)
			d.event(ctx, run, "triage_reject", dec.Reason)
			return d.finished("work needs human", t.Key, OutcomeRejected, dec.Reason), nil
		case triage.KindDispatch:
			taskPrompt = dec.Prompt
		default:
			return OutcomeSkipped, fmt.Errorf("triage %s: unknown decision %q", t.Key, dec.Kind)
		}
	}

	d.transition(ctx, t.Key, tracker.StateInProgress)
	return d.execute(runCtx, t, repo, agent, run, ws, taskPrompt)
}

// triage asks the Triager for a decision, traced as its own run.
func (d *Dispatcher) triage(ctx context.Context, in triage.Input) (triage.Decision, error) {
	ctx, span := d.Tracer.Start(ctx, "triage", trace.KindChain, map[string]any{
		"ticket": in.Ticket.Key, "attempts": in.Attempts, "last_stop_cause": in.LastStopCause,
	})
	dec, err := d.Triager.Decide(ctx, in)
	span.End(map[string]any{"kind": string(dec.Kind), "reason": dec.Reason, "question": dec.Question, "prompt": dec.Prompt}, err)
	return dec, err
}

// loadRun loads t's run record, fresh when the key now names a different
// ticket, with the ticket's name and branch fixed on first use.
func (d *Dispatcher) loadRun(ctx context.Context, t tracker.Ticket) (*state.Run, error) {
	run, err := d.Store.Load(ctx, t.Key)
	if err != nil {
		return nil, fmt.Errorf("load run %s: %w", t.Key, err)
	}
	if run.URL != "" && t.URL != "" && run.URL != t.URL {
		// The key now names a different ticket (e.g. a Jira project and a
		// GitHub repo sharing a prefix). Nothing in the old record — least
		// of all its resume token — applies to this one.
		d.log().Warn("run record belongs to another ticket with the same key; starting fresh", "ticket", t.Key, "was", run.URL, "now", t.URL)
		run = &state.Run{Ticket: t.Key}
	}
	run.URL = t.URL
	// The name and branch are fixed when the ticket is first worked, so a
	// retitled ticket keeps its branch and pull request.
	if run.Name == "" {
		run.Name = tracker.Name(t.Key, t.Summary)
	}
	if run.Branch == "" {
		run.Branch = gitops.BranchName(run.Name)
	}
	return run, nil
}

// claim takes t's claim for this machine. A non-empty outcome (or an
// error) means it did not, and the caller stops.
func (d *Dispatcher) claim(ctx context.Context, t tracker.Ticket, now time.Time) (Outcome, error) {
	if t.Claim.Fresh(now, d.Cfg.ClaimTimeout) && t.Claim.AgentID != d.Cfg.MachineID {
		return OutcomeSkipped, nil
	}
	won, err := d.Tracker.Claim(ctx, t.Key, d.Cfg.MachineID, now)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("claim %s: %w", t.Key, err)
	}
	if !won {
		return OutcomeClaimLost, nil
	}
	return "", nil
}

// canResume reports whether the last run asked a question that a human has
// since answered.
func (d *Dispatcher) canResume(t tracker.Ticket, run *state.Run) bool {
	if run.LastStatus != string(executor.StatusNeedsInput) || run.ResumeToken == "" || run.QuestionAt.IsZero() {
		return false
	}
	for _, c := range t.Comments {
		if c.Created.After(run.QuestionAt) && !isOurs(c) {
			return true
		}
	}
	return false
}

// execute runs the executor and reports the result. ctx is the run context:
// cancelled on shutdown or on losing the claim.
func (d *Dispatcher) execute(ctx context.Context, t tracker.Ticket, repo config.RepoConfig, agent config.Agent, run *state.Run, ws gitops.Workspace, taskPrompt string) (Outcome, error) {
	run.Attempts++
	run.Executor = agent.Executor
	d.setPhase(ctx, run, state.PhaseWorking)
	d.log().Info("work started", "ticket", t.Key, "agent", agent.Name, "attempt", d.attempt(run), "resume", run.ResumeToken != "")

	execCtx, cancelExec := context.WithTimeout(ctx, d.Cfg.RunTimeout)
	res, err := d.executorFor(agent).Run(execCtx, executor.Task{
		TicketKey: t.Key, Prompt: taskPrompt, Workspace: ws.Path,
		ResumeToken: run.ResumeToken, StepBudget: d.Cfg.StepBudget, Model: agent.Model, CodeWith: agent.CodeWith,
	})
	cancelExec()
	if err != nil {
		res = executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseError, Summary: err.Error()}
	}
	if res.Status == executor.StatusFailed && res.StopCause == executor.CauseNone {
		res.StopCause = executor.CauseError
	}
	if res.ResumeToken != "" {
		run.ResumeToken = res.ResumeToken
	}
	run.LastStatus = string(res.Status)
	run.StopCause = string(res.StopCause)
	if res.Log != "" {
		if _, err := d.Store.WriteLog(ctx, t.Key, d.now().UTC().Format("2006-01-02T15-04-05"), res.Log); err != nil {
			d.log().Warn("write log failed", "ticket", t.Key, "err", err)
		}
	}

	// Safety commit and push: the guarantee that every stop point is safe.
	bg := d.background(ctx)
	msg := "hive: checkpoint"
	if res.Status != executor.StatusCompleted {
		msg = fmt.Sprintf("hive: WIP (%s)", res.StopCause)
	}
	pushed, ferr := d.Workspaces.Finalize(bg, ws, msg)
	if ferr != nil {
		d.log().Error("finalize failed", "ticket", t.Key, "err", ferr)
	}
	if pushed {
		d.setPhase(bg, run, state.PhasePushed)
	}

	switch res.Status {
	case executor.StatusCompleted:
		var pr *githost.PR
		if pushed {
			pr, err = d.ensurePR(bg, repo, t, run)
			if err != nil {
				d.comment(bg, t.Key, reportPRFailed(err, run.Branch))
				d.transition(bg, t.Key, tracker.StateReady)
				d.event(bg, run, "pr_failed", err.Error())
				return OutcomeFailed, fmt.Errorf("open PR for %s: %w", t.Key, err)
			}
			if pr != nil {
				run.PRURL = pr.URL
				d.setPhase(bg, run, state.PhasePROpened)
			}
		}
		d.comment(bg, t.Key, reportCompleted(res, pr, run.Branch, pushed))
		d.transition(bg, t.Key, tracker.StateInReview)
		d.setPhase(bg, run, state.PhaseDone)
		d.event(bg, run, "completed", res.Summary)
		where := []any{"branch", run.Branch}
		if pr != nil {
			where = []any{"pr", pr.URL}
		}
		return d.finished("work finished", t.Key, OutcomeCompleted, res.Summary, where...), nil
	case executor.StatusNeedsInput:
		run.QuestionAt = d.now()
		run.Phase = state.PhaseBlocked
		d.save(bg, run)
		d.comment(bg, t.Key, reportNeedsInput(res.Question))
		d.transition(bg, t.Key, tracker.StateNeedsInfo)
		d.event(bg, run, "needs_input", res.Question)
		return d.finished("work needs info", t.Key, OutcomeNeedsInput, res.Question), nil
	default:
		if res.StopCause == executor.CauseBudget {
			d.pauseFor(res.RetryAfter)
		}
		return d.finishFailed(bg, t, run, res, pushed), nil
	}
}

// finishCompleted opens (or finds) the PR for a pushed branch and reports.
// On a PR failure the ticket returns to Ready and the run stays at phase
// pushed, so the next attempt only retries this step.
func (d *Dispatcher) finishCompleted(ctx context.Context, t tracker.Ticket, repo config.RepoConfig, run *state.Run, res executor.Result, pushed bool) (Outcome, error) {
	var pr *githost.PR
	if pushed {
		var err error
		pr, err = d.ensurePR(ctx, repo, t, run)
		if err != nil {
			run.LastStatus = string(executor.StatusCompleted)
			d.setPhase(ctx, run, state.PhasePushed)
			d.comment(ctx, t.Key, reportPRFailed(err, run.Branch))
			d.transition(ctx, t.Key, tracker.StateReady)
			d.event(ctx, run, "pr_failed", err.Error())
			return OutcomeFailed, fmt.Errorf("open PR for %s: %w", t.Key, err)
		}
		if pr != nil {
			run.PRURL = pr.URL
			d.setPhase(ctx, run, state.PhasePROpened)
		}
	}
	d.comment(ctx, t.Key, reportCompleted(res, pr, run.Branch, pushed))
	d.transition(ctx, t.Key, tracker.StateInReview)
	d.setPhase(ctx, run, state.PhaseDone)
	d.event(ctx, run, "completed", res.Summary)
	return OutcomeCompleted, nil
}

func (d *Dispatcher) finishFailed(ctx context.Context, t tracker.Ticket, run *state.Run, res executor.Result, pushed bool) Outcome {
	run.LastStatus = string(executor.StatusFailed)
	run.StopCause = string(res.StopCause)
	exhausted := run.Attempts >= d.Cfg.MaxAttempts
	d.save(ctx, run)
	d.comment(ctx, t.Key, reportFailed(res, run.Attempts, d.Cfg.MaxAttempts, run.Branch, pushed, exhausted, d.now()))
	msg := "work failed"
	if exhausted {
		d.transition(ctx, t.Key, tracker.StateNeedsHuman)
		msg = "work needs human"
	} else {
		d.transition(ctx, t.Key, tracker.StateReady)
	}
	d.event(ctx, run, "failed", res.Summary)
	return d.finished(msg, t.Key, OutcomeFailed, res.Summary, "cause", res.StopCause, "attempt", d.attempt(run))
}

// finished logs the end of work on a ticket: how it ended, and the result
// the ticket was told about. It returns out so callers can return it.
func (d *Dispatcher) finished(msg, key string, out Outcome, result string, extra ...any) Outcome {
	args := append([]any{"ticket", key, "result", result}, extra...)
	d.log().Info(msg, args...)
	return out
}

func (d *Dispatcher) attempt(run *state.Run) string {
	return fmt.Sprintf("%d/%d", run.Attempts, d.Cfg.MaxAttempts)
}

func (d *Dispatcher) ensurePR(ctx context.Context, repo config.RepoConfig, t tracker.Ticket, run *state.Run) (*githost.PR, error) {
	pr, err := d.Host.FindPR(ctx, repo.Name, run.Branch)
	if err != nil {
		return nil, err
	}
	if pr != nil {
		return pr, nil
	}
	body := fmt.Sprintf("Resolves %s.\n\n%s\n\nOpened by HiveDispatch worker %s.", t.Key, t.URL, run.Agent)
	return d.Host.OpenPR(ctx, repo.Name, githost.Request{
		Title: run.Name,
		Body:  body,
		Head:  run.Branch,
		Base:  repo.DefaultBranch,
	})
}

// heartbeat refreshes the claim until ctx ends. Losing the claim cancels
// the run: someone else now owns the ticket.
func (d *Dispatcher) heartbeat(ctx context.Context, key string, lost context.CancelFunc) {
	tick := time.NewTicker(d.Cfg.HeartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			err := d.Tracker.Heartbeat(ctx, key, d.Cfg.MachineID)
			if errors.Is(err, tracker.ErrNotClaimHolder) {
				d.log().Error("claim lost mid-run; cancelling", "ticket", key)
				lost()
				return
			}
			if err != nil && ctx.Err() == nil {
				d.log().Warn("heartbeat failed", "ticket", key, "err", err)
			}
		}
	}
}

// background returns a context that survives ctx's cancellation so cleanup
// (comments, transitions, release) still happens after a hard stop. Each
// tracker call is already bounded by the client's own request timeout.
func (d *Dispatcher) background(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

func (d *Dispatcher) release(ctx context.Context, key string) {
	bg, cancel := context.WithTimeout(d.background(ctx), releaseTimeout)
	defer cancel()
	err := d.Tracker.Release(bg, key, d.Cfg.MachineID)
	if err != nil && !errors.Is(err, tracker.ErrNotClaimHolder) {
		d.log().Error("release failed", "ticket", key, "err", err)
	}
}

func (d *Dispatcher) comment(ctx context.Context, key, body string) {
	if err := d.Tracker.Comment(ctx, key, body); err != nil {
		d.log().Error("comment failed", "ticket", key, "err", err)
	}
}

func (d *Dispatcher) transition(ctx context.Context, key string, to tracker.State) {
	if err := d.Tracker.Transition(ctx, key, to); err != nil {
		d.log().Error("transition failed", "ticket", key, "to", to, "err", err)
	}
}

func (d *Dispatcher) setPhase(ctx context.Context, run *state.Run, p state.Phase) {
	run.Phase = p
	d.save(ctx, run)
}

func (d *Dispatcher) save(ctx context.Context, run *state.Run) {
	if err := d.Store.Save(ctx, run); err != nil {
		d.log().Error("save run failed", "ticket", run.Ticket, "err", err)
	}
}

func (d *Dispatcher) event(ctx context.Context, run *state.Run, ev, msg string) {
	e := state.LogEntry{Time: d.now(), Agent: d.Cfg.MachineID, Event: ev, Phase: string(run.Phase), Cause: run.StopCause, Message: msg}
	if err := d.Store.AppendLog(ctx, run.Ticket, e); err != nil {
		d.log().Error("append log failed", "ticket", run.Ticket, "err", err)
	}
}
