"""HiveDispatch's own coding agent: a LangGraph loop of a chat model and the
workspace tools. It runs inside the workflow's code node when
code_with: langgraph and returns the same AgentResult the CLIs do, so the
workflow's routing, events and summaries are unchanged."""

import functools
import json
import threading
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import TypedDict

from langchain_core.messages import AIMessage, HumanMessage, SystemMessage, ToolMessage
from langchain_core.tools import StructuredTool
from langgraph.graph import END, START, StateGraph
from langgraph.prebuilt import ToolNode

from .. import stop
from ..agentrun import AgentResult
from . import prompts
from .tools import Workspace

MAX_NUDGES = 2
KEEP = 20
STEP_OUT_CAP = 16 * 1024
TRIMMED = "[output trimmed]"


@dataclass
class CoderRun:
    result: AgentResult
    messages: list  # the whole conversation, for the next round
    steps_used: int  # tool calls so far in this run, across rounds


class _State(TypedDict):
    messages: list


def trim(messages: list, keep: int = KEEP) -> list:
    """Replaces tool outputs older than the last `keep` messages, so a long
    fix loop does not grow the context without bound."""
    cut = len(messages) - keep
    out = []
    for i, m in enumerate(messages):
        if i < cut and isinstance(m, ToolMessage) and m.content != TRIMMED:
            m = ToolMessage(content=TRIMMED, tool_call_id=m.tool_call_id, name=m.name)
        out.append(m)
    return out


def _is_rate_limit(exc: Exception) -> bool:
    return getattr(exc, "status_code", None) == 429 or "RateLimit" in type(exc).__name__


def _stamp(t: datetime) -> str:
    return t.astimezone(UTC).isoformat().replace("+00:00", "Z")


def _text(msg) -> str:
    c = msg.content
    if isinstance(c, str):
        return c
    return "".join(p.get("text", "") for p in c if isinstance(p, dict))


def run(task, prompt: str, messages: list, steps_used: int, model, now=lambda: datetime.now(UTC)) -> CoderRun:
    """One code round: continue the conversation in `messages` with `prompt`
    until the agent finishes, asks a human, or a limit stops it."""
    ws = Workspace(task.workspace, task.allowed_commands, task.checks, task.path, task.check_timeout_seconds)
    outcome: dict = {}
    steps: list[dict] = []
    usage = {
        "model": task.model or task.chat_model.model,
        "input_tokens": 0,
        "output_tokens": 0,
        "cost_usd": 0.0,
    }
    used = [steps_used]
    # ToolNode runs a reply's tool calls in parallel threads. They share one
    # worktree, the step budget and the step log, so they run one at a time,
    # never overlapping.
    lock = threading.Lock()

    def tool(fn, name, doc):
        # functools.wraps carries fn's signature (via __wrapped__), which
        # StructuredTool.from_function reads to build the args schema.
        @functools.wraps(fn)
        def wrapped(*args, **kwargs):
            with lock:
                return body(*args, **kwargs)

        def body(*args, **kwargs):
            start = now()
            if stop.requested:
                outcome.update(status="failed", stop_cause="killed", summary="Stopped by the worker.")
                return "stopped"
            if task.step_budget and used[0] >= task.step_budget:
                outcome.update(
                    status="failed",
                    stop_cause="step_budget",
                    summary=f"Step budget of {task.step_budget} tool calls exhausted.",
                )
                return "step budget exhausted"
            used[0] += 1
            out = fn(*args, **kwargs)
            steps.append(
                {
                    "kind": "tool",
                    "name": name,
                    "input": json.dumps(kwargs)[:STEP_OUT_CAP],
                    "output": out[:STEP_OUT_CAP],
                    "is_error": out.startswith("error:"),
                    "start": _stamp(start),
                    "end": _stamp(now()),
                }
            )
            return out

        return StructuredTool.from_function(func=wrapped, name=name, description=doc)

    def finish(summary: str) -> str:
        """Finish with a short summary of the change and how it was verified."""
        outcome.update(status="completed", summary=summary)
        return "finished"

    def ask_human(question: str) -> str:
        """Ask the human one question and stop until they answer."""
        outcome.update(status="needs_input", question=question, summary=question)
        return "asked"

    tools = [
        tool(ws.read_file, "read_file", "Read a file's numbered lines. offset is the 0-based first line."),
        tool(ws.write_file, "write_file", "Create or overwrite a file with content."),
        tool(ws.edit_file, "edit_file", "Replace old with new in a file; old must match exactly once."),
        tool(ws.list_files, "list_files", "List files matching a glob (default **/*)."),
        tool(ws.search, "search", "Search text files for a Python regex; optional glob."),
        tool(ws.run_command, "run_command", "Run an allowed command in the worktree, without a shell."),
        tool(ask_human, "ask_human", "Ask the human one question and stop until they answer."),
        tool(finish, "finish", "Finish with a short summary of the change and how it was verified."),
    ]
    bound = model.bind_tools(tools)
    tool_node = ToolNode(tools)
    nudges = [0]
    system = SystemMessage(
        prompts.SYSTEM.format(
            allowed=", ".join(task.allowed_commands) or "(none)",
            checks=", ".join(task.checks) or "(none)",
            guidance=task.guidance or "(none)",
        )
    )

    def model_node(state: _State) -> _State:
        if stop.requested:
            outcome.update(status="failed", stop_cause="killed", summary="Stopped by the worker.")
            return state
        try:
            # Trim as the round goes, not only between rounds: one long round
            # of file reads would otherwise outgrow a small model's context.
            reply = bound.invoke([system, *trim(state["messages"])])
        except Exception as exc:  # a model failure ends the run with its cause
            cause = "budget" if _is_rate_limit(exc) else "error"
            outcome.update(status="failed", stop_cause=cause, summary=f"coding model failed: {exc}")
            return state
        u = getattr(reply, "usage_metadata", None) or {}
        usage["input_tokens"] += int(u.get("input_tokens", 0))
        usage["output_tokens"] += int(u.get("output_tokens", 0))
        msgs = [*state["messages"], reply]
        if not reply.tool_calls:
            if nudges[0] >= MAX_NUDGES:
                outcome.update(status="completed", summary=_text(reply).strip() or "Run completed.")
            else:
                nudges[0] += 1
                msgs.append(HumanMessage(prompts.NUDGE))
        return {"messages": msgs}

    def tools_node(state: _State) -> _State:
        out = tool_node.invoke({"messages": state["messages"]})
        return {"messages": [*state["messages"], *out["messages"]]}

    def after_model(state: _State) -> str:
        if outcome:
            return END
        last = state["messages"][-1]
        return "tools" if isinstance(last, AIMessage) and last.tool_calls else "model"

    def after_tools(state: _State) -> str:
        return END if outcome else "model"

    g = StateGraph(_State)
    g.add_node("model", model_node)
    g.add_node("tools", tools_node)
    g.add_edge(START, "model")
    g.add_conditional_edges("model", after_model, ["tools", "model", END])
    g.add_conditional_edges("tools", after_tools, ["model", END])
    history = [*trim(list(messages)), HumanMessage(prompt)]
    final = g.compile().invoke({"messages": history}, {"recursion_limit": 10_000})
    if not outcome:
        outcome.update(
            status="failed", stop_cause="error", summary="the coding agent stopped without an outcome"
        )
    res = AgentResult(
        status=outcome["status"],
        stop_cause=outcome.get("stop_cause", ""),
        summary=outcome.get("summary", ""),
        question=outcome.get("question", ""),
        changed_files=list(ws.changed),
        steps=steps,
        usage=usage,
    )
    return CoderRun(result=res, messages=final["messages"], steps_used=used[0])
