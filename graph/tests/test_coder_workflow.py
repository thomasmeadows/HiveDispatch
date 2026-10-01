import io
import json
import subprocess

from conftest import events
from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver
from test_coder_agent import ToolFake, call

from hivegraph.checks import CheckFailure, CheckReport
from hivegraph.events import Emitter
from hivegraph.graph import run_task
from hivegraph.task import ChatModel, Task


def approve():
    return AIMessage(json.dumps({"verdict": "approve", "findings": ""}))


def mk_task(tmp_path):
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    (tmp_path / "a.txt").write_text("hello\n")
    return Task(
        prompt="change hello",
        workspace=str(tmp_path),
        code_with="langgraph",
        allowed_commands=["echo"],
        checks=["true"],
        git_dir="",
        base_ref="HEAD",
        step_budget=50,
        chat_model=ChatModel(model="m"),
    )


class Recording(ToolFake):
    """Records the conversation each model call sees."""

    seen: list = []

    def _generate(self, messages, *a, **kw):
        type(self).seen.append([m.content for m in messages])
        return super()._generate(messages, *a, **kw)


def test_code_with_langgraph_runs_the_coder(tmp_path):
    task = mk_task(tmp_path)
    coder = ToolFake(
        messages=iter(
            [
                call("edit_file", {"path": "a.txt", "old": "hello", "new": "bye"}, "1"),
                call("finish", {"summary": "done by coder"}, "2"),
            ]
        )
    )
    out = io.StringIO()
    run_task(
        task,
        Emitter(out),
        model=ToolFake(messages=iter([AIMessage("plan"), approve()])),
        coder_model=coder,
        checks=lambda t: CheckReport(True),
    )
    evs = events(out)
    r = [e for e in evs if e["type"] == "result"][0]
    assert r["status"] == "completed" and "done by coder" in r["summary"] and r["changed_files"] == ["a.txt"]
    assert [e["name"] for e in evs if e["type"] == "step"] == ["edit_file", "finish"]
    assert any(e["type"] == "usage" for e in evs)


def test_fix_round_continues_the_conversation(tmp_path):
    task = mk_task(tmp_path)
    Recording.seen = []
    coder = Recording(
        messages=iter([call("finish", {"summary": "first"}, "1"), call("finish", {"summary": "second"}, "2")])
    )
    checks = iter([CheckReport(False, [CheckFailure("true", "FAIL x")]), CheckReport(True)])
    out = io.StringIO()
    run_task(
        task,
        Emitter(out),
        model=ToolFake(messages=iter([AIMessage("plan"), approve()])),
        coder_model=coder,
        checks=lambda t: next(checks),
    )
    assert len(Recording.seen) == 2
    second = "\n".join(str(c) for c in Recording.seen[1])
    assert "change hello" in second and "FAIL x" in second
    assert "second" in [e for e in events(out) if e["type"] == "result"][0]["summary"]


def test_resume_keeps_the_coder_conversation(tmp_path):
    task = mk_task(tmp_path)
    saver = InMemorySaver()
    first = ToolFake(messages=iter([call("ask_human", {"question": "Which word?"}, "1")]))
    out = io.StringIO()
    run_task(
        task,
        Emitter(out),
        model=ToolFake(messages=iter([AIMessage("plan")])),
        coder_model=first,
        checks=lambda t: CheckReport(True),
        checkpointer=saver,
    )
    r1 = [e for e in events(out) if e["type"] == "result"][0]
    assert r1["status"] == "needs_input" and r1["question"] == "Which word?"

    task.resume_token, task.prompt = r1["resume_token"], "Use bye."
    Recording.seen = []
    second = Recording(messages=iter([call("finish", {"summary": "used bye"}, "2")]))
    out2 = io.StringIO()
    run_task(
        task,
        Emitter(out2),
        model=ToolFake(messages=iter([approve()])),
        coder_model=second,
        checks=lambda t: CheckReport(True),
        checkpointer=saver,
    )
    r2 = [e for e in events(out2) if e["type"] == "result"][0]
    assert r2["status"] == "completed" and "used bye" in r2["summary"]
    convo = "\n".join(str(c) for c in Recording.seen[0])
    assert "change hello" in convo and "Use bye." in convo


def test_coder_model_choice(monkeypatch):
    from hivegraph import model as m

    seen = {}
    monkeypatch.setattr(m, "ChatOpenAI", lambda **kw: seen.update(kw) or object())
    t = Task(model="deepseek-pro", chat_model=ChatModel(model="deepseek-flash", base_url="u", api_key_env=""))
    m.coder_model(t)
    assert seen["model"] == "deepseek-pro" and seen["base_url"] == "u"
    t.model = ""
    m.coder_model(t)
    assert seen["model"] == "deepseek-flash"
