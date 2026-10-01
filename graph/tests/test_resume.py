import io
import json

from conftest import events, ok
from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver

from hivegraph.checks import CheckReport
from hivegraph.events import Emitter
from hivegraph.graph import run_task

APPROVE = json.dumps({"verdict": "approve", "findings": ""})


def fake_model(*texts):
    return GenericFakeChatModel(messages=iter([AIMessage(content=t) for t in texts]))


def test_resume_skips_plan_and_resumes_the_cli(agent_script):
    saver = InMemorySaver()
    task = agent_script([ok(status="needs_input", question="Which DB?", session="s1"), ok(session="s2")])
    out = io.StringIO()
    run_task(
        task,
        Emitter(out),
        model=fake_model("the plan"),
        checks=lambda t: CheckReport(True),
        checkpointer=saver,
    )
    first = [e for e in events(out) if e["type"] == "result"][0]
    assert first["status"] == "needs_input"

    task.resume_token, task.prompt = first["resume_token"], "Use Postgres."
    out2 = io.StringIO()
    run_task(
        task, Emitter(out2), model=fake_model(APPROVE), checks=lambda t: CheckReport(True), checkpointer=saver
    )
    nodes = [e["name"] for e in events(out2) if e["type"] == "node"]
    assert nodes[0] == "code" and "plan" not in nodes
    call = agent_script.calls()[1]
    assert call["argv"][call["argv"].index("-resume") + 1] == "s1"
    assert "Use Postgres." in call["prompt"] and "the plan" in call["prompt"]
    assert [e for e in events(out2) if e["type"] == "result"][0]["status"] == "completed"


def test_sqlite_checkpoints_land_in_the_git_dir(agent_script, tmp_path):
    task = agent_script([ok()], git_dir=str(tmp_path / "gitdir"))
    run_task(task, Emitter(io.StringIO()), model=fake_model("p", APPROVE), checks=lambda t: CheckReport(True))
    assert (tmp_path / "gitdir" / "hivegraph" / "checkpoints.sqlite").exists()


def test_parent_from_the_worker_when_tracing(agent_script, monkeypatch):
    seen = {}

    def spy(**kw):
        seen.update(kw)
        import contextlib

        return contextlib.nullcontext()

    monkeypatch.setattr("hivegraph.graph._tracing_enabled", lambda: True)
    monkeypatch.setattr("hivegraph.graph._tracing_context", spy)
    monkeypatch.setattr("hivegraph.graph._flush_traces", lambda: None)
    monkeypatch.setenv("LANGSMITH_PARENT", "20261001T120000000000Zabc")
    task = agent_script([ok()])
    out = io.StringIO()
    run_task(task, Emitter(out), model=fake_model("p", APPROVE), checks=lambda t: CheckReport(True))
    assert seen.get("parent") == "20261001T120000000000Zabc"
    assert [e for e in events(out) if e["type"] == "result"][0]["steps_traced"] is True
