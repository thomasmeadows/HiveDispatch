import subprocess

import pytest
from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

from hivegraph import stop
from hivegraph.coder.agent import run, trim
from hivegraph.task import ChatModel, Task


class ToolFake(GenericFakeChatModel):
    """A scripted chat model that accepts bind_tools."""

    def bind_tools(self, tools, **kw):
        return self


def call(name, args, i="c"):
    return AIMessage(
        content="",
        tool_calls=[{"name": name, "args": args, "id": f"{i}-{name}"}],
        usage_metadata={"input_tokens": 10, "output_tokens": 2, "total_tokens": 12},
    )


def model(*msgs):
    return ToolFake(messages=iter(msgs))


@pytest.fixture
def task(tmp_path):
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    (tmp_path / "a.txt").write_text("hello\n")
    return Task(
        workspace=str(tmp_path),
        allowed_commands=["echo"],
        checks=[],
        step_budget=10,
        chat_model=ChatModel(model="m"),
    )


def test_edits_then_finishes(task, tmp_path):
    r = run(
        task,
        "change hello",
        [],
        0,
        model(
            call("edit_file", {"path": "a.txt", "old": "hello", "new": "bye"}, "1"),
            call("run_command", {"command": "echo ok"}, "2"),
            call("finish", {"summary": "Changed hello to bye."}, "3"),
        ),
    )
    assert (tmp_path / "a.txt").read_text() == "bye\n"
    res = r.result
    assert (
        res.status == "completed"
        and res.summary == "Changed hello to bye."
        and res.changed_files == ["a.txt"]
    )
    assert [s["name"] for s in res.steps] == ["edit_file", "run_command", "finish"]
    assert res.usage["input_tokens"] == 30 and r.steps_used == 3
    assert isinstance(r.messages[0], HumanMessage) and r.messages[0].content == "change hello"


def test_ask_human_is_needs_input(task):
    r = run(task, "x", [], 0, model(call("ask_human", {"question": "Which DB?"})))
    assert r.result.status == "needs_input" and r.result.question == "Which DB?"


def test_refusals_are_seen_by_the_agent(task):
    r = run(
        task,
        "x",
        [],
        0,
        model(call("run_command", {"command": "rm -rf ."}, "1"), call("finish", {"summary": "ok"}, "2")),
    )
    tool_msgs = [m for m in r.messages if isinstance(m, ToolMessage)]
    assert tool_msgs[0].content.startswith("error: not allowed")
    assert r.result.steps[0]["is_error"] is True


def test_step_budget_spans_rounds(task):
    task.step_budget = 3
    r = run(
        task,
        "x",
        [],
        2,
        model(
            call("list_files", {}, "1"), call("list_files", {}, "2"), call("finish", {"summary": "s"}, "3")
        ),
    )
    assert r.result.status == "failed" and r.result.stop_cause == "step_budget" and r.steps_used == 3


def test_no_tool_calls_nudges_twice_then_completes(task):
    r = run(task, "x", [], 0, model(AIMessage("thinking"), AIMessage("still"), AIMessage("I changed it.")))
    assert r.result.status == "completed" and r.result.summary == "I changed it."
    nudges = [m for m in r.messages if isinstance(m, HumanMessage) and "finish" in m.content]
    assert len(nudges) == 2


class RateLimited(ToolFake):
    def _generate(self, *a, **kw):
        err = RuntimeError("Error code: 429 - rate limit")
        err.status_code = 429
        raise err


class Broken(ToolFake):
    def _generate(self, *a, **kw):
        raise RuntimeError("model down")


def test_model_errors_map_to_causes(task):
    r = run(task, "x", [], 0, RateLimited(messages=iter([])))
    assert r.result.status == "failed" and r.result.stop_cause == "budget"
    r = run(task, "x", [], 0, Broken(messages=iter([])))
    assert r.result.status == "failed" and r.result.stop_cause == "error" and "model down" in r.result.summary


def test_stop_flag_is_killed(task, monkeypatch):
    monkeypatch.setattr(stop, "requested", True)
    r = run(task, "x", [], 0, model(call("finish", {"summary": "s"})))
    assert r.result.status == "failed" and r.result.stop_cause == "killed"


def test_continues_a_conversation(task):
    first = run(task, "x", [], 0, model(call("finish", {"summary": "s"}, "1")))
    second = run(
        task, "fix the tests", first.messages, first.steps_used, model(call("finish", {"summary": "t"}, "2"))
    )
    humans = [m.content for m in second.messages if isinstance(m, HumanMessage)]
    assert humans == ["x", "fix the tests"] and second.steps_used == 2


def test_trim_replaces_old_tool_outputs():
    msgs = []
    for i in range(30):
        msgs.append(AIMessage(content="", tool_calls=[{"name": "read_file", "args": {}, "id": str(i)}]))
        msgs.append(ToolMessage(content=f"big output {i}", tool_call_id=str(i)))
    out = trim(msgs, keep=20)
    tools = [m for m in out if isinstance(m, ToolMessage)]
    assert tools[0].content == "[output trimmed]" and tools[-1].content == "big output 29"
    assert len(out) == len(msgs)
