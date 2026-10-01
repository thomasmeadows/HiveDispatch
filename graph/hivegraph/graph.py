"""The workflow: plan → code → checks ⇄ fix → review ⇄ fix → finish.

The code node runs Claude Code or Codex through `hivedispatch agent-run`;
the plan and review nodes ask a cheap chat model, which is never allowed to
stop a run on its own: when it fails, the graph codes without a plan or
finishes without a review, and says so.
"""

import contextlib
import json
import os
import subprocess
import uuid
from datetime import UTC, datetime
from typing import TypedDict

from langgraph.graph import END, START, StateGraph

from . import prompts, stop
from .agentrun import run_agent
from .checks import run_checks

MAX_DIFF = 100 * 1024


class State(TypedDict, total=False):
    prompt: str  # what the human asked: the ticket, or their reply on resume
    plan: str
    cli_session: str  # the code CLI's resume token
    fix_round: int
    review_round: int
    feedback: str  # what the next code step must address
    checks_passed: bool
    checks_report: str
    review_findings: str
    review_skipped: str
    summary: str
    status: str  # completed | needs_input | failed
    stop_cause: str
    question: str
    changed_files: list[str]


def now() -> datetime:
    return datetime.now(UTC)


def parse_time(s: str) -> datetime:
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except (ValueError, AttributeError):
        return now()


def _text(msg) -> str:
    c = msg.content
    if isinstance(c, str):
        return c
    return "".join(p.get("text", "") for p in c if isinstance(p, dict))


def parse_review(text: str) -> tuple[str, str]:
    """verdict, findings. Anything unparsable is an approval."""
    start, end = text.find("{"), text.rfind("}")
    if start < 0 or end < start:
        return "approve", ""
    try:
        data = json.loads(text[start : end + 1])
    except json.JSONDecodeError:
        return "approve", ""
    if not isinstance(data, dict):
        return "approve", ""
    findings = str(data.get("findings", "") or "")
    if data.get("verdict") != "changes" or not findings.strip():
        return "approve", ""
    return "changes", findings


def git_diff(task) -> str:
    p = subprocess.run(
        ["git", "-C", task.workspace, "diff", task.base_ref],
        capture_output=True,
        text=True,
        errors="replace",
    )
    diff = p.stdout
    # Files the CLI created are untracked until the worker commits; diff
    # them against nothing so the review sees them, without touching the index.
    untracked = subprocess.run(
        ["git", "-C", task.workspace, "ls-files", "--others", "--exclude-standard", "-z"],
        capture_output=True,
        text=True,
        errors="replace",
    ).stdout
    for path in filter(None, untracked.split("\0")):
        if len(diff) > MAX_DIFF:
            break
        d = subprocess.run(
            ["git", "-C", task.workspace, "diff", "--no-index", "--", "/dev/null", path],
            capture_output=True,
            text=True,
            errors="replace",
        )
        diff += d.stdout
    if len(diff) > MAX_DIFF:
        diff = diff[:MAX_DIFF] + "\n…[diff truncated]"
    return diff


def build(task, emitter, model, agent=run_agent, checks=run_checks) -> StateGraph:
    def timed(name, fn):
        def node(state: State) -> State:
            start = now()
            if stop.requested and name != "finish":
                out: State = {
                    "status": "failed",
                    "stop_cause": "killed",
                    "feedback": "",
                    "summary": "Stopped by the worker.",
                }
            else:
                out = fn(state)
            emitter.node(name, start, now())
            return out

        return node

    def plan(state: State) -> State:
        try:
            msg = model.invoke(
                prompts.PLAN.format(prompt=state["prompt"], guidance=task.guidance or "(none)")
            )
            return {"plan": _text(msg).strip() or "(no plan)"}
        except Exception as exc:  # the plan is optional
            return {"plan": f"(no plan: the planning model failed: {exc})"}

    def code(state: State) -> State:
        prompt = state.get("feedback") or prompts.CODE_FIRST.format(
            prompt=state["prompt"], plan=state.get("plan", "")
        )
        r = agent(task, prompt, state.get("cli_session", ""))
        for s in r.steps:
            end = s.get("end")
            emitter.step(
                s.get("kind", "tool"),
                s.get("name", ""),
                s.get("input", ""),
                s.get("output", ""),
                bool(s.get("is_error")),
                parse_time(s.get("start", "")),
                parse_time(end) if end else None,
            )
        u = r.usage or {}
        if u.get("input_tokens") or u.get("output_tokens") or u.get("cost_usd"):
            emitter.usage(
                u.get("model", ""),
                int(u.get("input_tokens", 0)),
                int(u.get("output_tokens", 0)),
                float(u.get("cost_usd", 0.0)),
            )
        changed = sorted(set(state.get("changed_files", [])) | set(r.changed_files or []))
        return {
            "cli_session": r.resume_token or state.get("cli_session", ""),
            "status": r.status,
            "stop_cause": r.stop_cause,
            "question": r.question,
            "summary": r.summary,
            "changed_files": changed,
            "feedback": "",
        }

    def run_checks_node(state: State) -> State:
        report = checks(task)
        out: State = {"checks_passed": report.passed, "checks_report": report.describe()}
        if not report.passed and state.get("fix_round", 0) < task.max_fix_rounds:
            out["fix_round"] = state.get("fix_round", 0) + 1
            out["feedback"] = prompts.CODE_FIX.format(report=report.describe())
        return out

    def review(state: State) -> State:
        try:
            msg = model.invoke(prompts.REVIEW.format(plan=state.get("plan", ""), diff=git_diff(task)))
        except Exception as exc:  # the review is optional
            return {
                "review_skipped": f"review skipped: the review model failed: {exc}",
                "review_findings": "",
            }
        verdict, findings = parse_review(_text(msg))
        out: State = {"review_findings": findings if verdict == "changes" else ""}
        if verdict == "changes" and state.get("review_round", 0) < task.max_review_rounds:
            out["review_round"] = state.get("review_round", 0) + 1
            out["feedback"] = prompts.CODE_REVIEW.format(findings=findings)
        return out

    def finish(state: State) -> State:
        if state.get("status") != "completed":
            return {}
        parts = [state.get("summary", "").strip() or "Run completed."]
        if task.checks and not state.get("checks_passed", True):
            parts.append("Checks still failing after the fix rounds:\n\n" + state.get("checks_report", ""))
        if state.get("review_findings"):
            parts.append("Unresolved review findings:\n\n" + state["review_findings"])
        if state.get("review_skipped"):
            parts.append(state["review_skipped"] + ".")
        return {"summary": "\n\n".join(parts)}

    def after_start(state: State) -> str:
        return "code" if state.get("plan") else "plan"

    def after_code(state: State) -> str:
        return "checks" if state.get("status") == "completed" else "finish"

    def after_checks(state: State) -> str:
        if state.get("checks_passed"):
            return "review"
        return "code" if state.get("feedback") else "finish"

    def after_review(state: State) -> str:
        return "code" if state.get("feedback") else "finish"

    g = StateGraph(State)
    g.add_node("plan", timed("plan", plan))
    g.add_node("code", timed("code", code))
    g.add_node("checks", timed("checks", run_checks_node))
    g.add_node("review", timed("review", review))
    g.add_node("finish", timed("finish", finish))
    g.add_conditional_edges(START, after_start, ["plan", "code"])
    g.add_edge("plan", "code")
    g.add_conditional_edges("code", after_code, ["checks", "finish"])
    g.add_conditional_edges("checks", after_checks, ["review", "code", "finish"])
    g.add_conditional_edges("review", after_review, ["code", "finish"])
    g.add_edge("finish", END)
    return g


def _tracing_enabled() -> bool:
    try:
        from langsmith.utils import tracing_is_enabled

        return bool(tracing_is_enabled())
    except Exception:
        return False


def _tracing_context(**kw):
    from langsmith.run_helpers import tracing_context

    return tracing_context(**kw)


def _flush_traces() -> None:
    from langchain_core.tracers.langchain import wait_for_all_tracers

    wait_for_all_tracers()


def run_task(task, emitter, model=None, agent=run_agent, checks=run_checks, checkpointer=None) -> int:
    """Runs the workflow and always emits exactly one result event.

    Checkpoints go to <git dir>/hivegraph/checkpoints.sqlite, so a ticket
    resumed after a human reply continues its thread. With LangSmith tracing
    on, the run is attached under the worker's span (LANGSMITH_PARENT).
    """
    thread = task.resume_token or str(uuid.uuid4())
    traced = _tracing_enabled()
    try:
        with contextlib.ExitStack() as stack:
            if checkpointer is None and task.git_dir:
                from langgraph.checkpoint.sqlite import SqliteSaver

                path = os.path.join(task.git_dir, "hivegraph", "checkpoints.sqlite")
                os.makedirs(os.path.dirname(path), exist_ok=True)
                checkpointer = stack.enter_context(SqliteSaver.from_conn_string(path))
            parent = os.environ.get("LANGSMITH_PARENT")
            if traced and parent:
                stack.enter_context(_tracing_context(parent=parent))
            if model is None:
                from .model import chat_model

                model = chat_model(task)
            graph = build(task, emitter, model, agent, checks).compile(checkpointer=checkpointer)
            config = {
                "configurable": {"thread_id": thread},
                "recursion_limit": 100,
                "run_name": f"hivegraph {task.ticket}".strip(),
                "metadata": {"ticket": task.ticket},
            }
            # A new turn on the thread: the prompt is the new message, and the
            # loop counters start again.
            inputs: State = {
                "prompt": task.prompt,
                "fix_round": 0,
                "review_round": 0,
                "feedback": "",
                "status": "",
                "review_findings": "",
                "review_skipped": "",
            }
            final = graph.invoke(inputs, config)
    except Exception as exc:  # every failure is reported as a result
        emitter.result("failed", "error", f"hivegraph: {type(exc).__name__}: {exc}", "", thread, [], False)
        return 0
    finally:
        if traced:
            _flush_traces()
    status = final.get("status") or "failed"
    emitter.result(
        status,
        final.get("stop_cause", "") if status == "failed" else "",
        final.get("summary", ""),
        final.get("question", ""),
        thread,
        final.get("changed_files", []),
        traced,
    )
    return 0
