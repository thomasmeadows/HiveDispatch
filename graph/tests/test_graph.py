import io
import json

from conftest import events, ok
from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
from langchain_core.messages import AIMessage

from hivegraph.checks import CheckFailure, CheckReport
from hivegraph.events import Emitter
from hivegraph.graph import run_task


def model(*texts):
    return GenericFakeChatModel(messages=iter([AIMessage(content=t) for t in texts]))


APPROVE = json.dumps({"verdict": "approve", "findings": ""})


def checks_seq(*passes):
    it = iter(passes)

    def run(task):
        p = next(it)
        return CheckReport(passed=p, failures=[] if p else [CheckFailure("go test ./...", "FAIL x")])

    return run


def result_of(out):
    rs = [e for e in events(out) if e["type"] == "result"]
    assert len(rs) == 1
    return rs[0]


def test_happy_path(agent_script):
    task = agent_script([ok()], checks=["go test ./..."])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("1. do it", APPROVE), checks=checks_seq(True))
    r = result_of(out)
    assert r["status"] == "completed" and "did it" in r["summary"] and r["changed_files"] == ["a.go"]
    assert r["resume_token"]  # the thread id
    nodes = [e["name"] for e in events(out) if e["type"] == "node"]
    assert nodes == ["plan", "code", "checks", "review", "finish"]
    assert any(e["type"] == "step" and e["name"] == "Bash" for e in events(out))
    assert any(e["type"] == "usage" and e["input_tokens"] == 10 for e in events(out))
    assert "1. do it" in agent_script.calls()[0]["prompt"]


def test_fix_loop_then_green(agent_script):
    task = agent_script([ok(session="s1"), ok(session="s2")], checks=["go test ./..."])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan", APPROVE), checks=checks_seq(False, True))
    calls = agent_script.calls()
    assert len(calls) == 2 and "FAIL x" in calls[1]["prompt"]
    assert calls[1]["argv"][calls[1]["argv"].index("-resume") + 1] == "s1"
    assert result_of(out)["status"] == "completed"


def test_gives_up_after_max_fix_rounds_but_completes(agent_script):
    task = agent_script([ok()], checks=["go test ./..."], max_fix_rounds=1)
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan", APPROVE), checks=checks_seq(False, False))
    r = result_of(out)
    assert r["status"] == "completed" and "still failing" in r["summary"] and "go test ./..." in r["summary"]
    assert len(agent_script.calls()) == 2


def test_review_requests_changes_once(agent_script):
    task = agent_script([ok(), ok(summary="fixed")])
    changes = json.dumps({"verdict": "changes", "findings": "rename foo"})
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan", changes, APPROVE), checks=checks_seq(True, True))
    calls = agent_script.calls()
    assert len(calls) == 2 and "rename foo" in calls[1]["prompt"]
    assert "fixed" in result_of(out)["summary"]


def test_needs_input_stops(agent_script):
    task = agent_script([ok(status="needs_input", question="Which DB?", summary="asked")])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan"), checks=checks_seq())
    r = result_of(out)
    assert r["status"] == "needs_input" and r["question"] == "Which DB?"


def test_cli_failure_stops_with_its_cause(agent_script):
    task = agent_script([ok(status="failed", stop_cause="budget", summary="quota")])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan"), checks=checks_seq())
    r = result_of(out)
    assert r["status"] == "failed" and r["stop_cause"] == "budget"


class Broken(GenericFakeChatModel):
    def _generate(self, *a, **kw):
        raise RuntimeError("model down")


def test_model_failures_never_stop_the_run(agent_script):
    task = agent_script([ok()])
    out = io.StringIO()
    run_task(task, Emitter(out), model=Broken(messages=iter([])), checks=checks_seq(True))
    r = result_of(out)
    assert r["status"] == "completed" and "review skipped" in r["summary"]
    assert "Add a flag" in agent_script.calls()[0]["prompt"]


def test_unparsable_review_counts_as_approve(agent_script):
    task = agent_script([ok()])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan", "looks great!"), checks=checks_seq(True))
    assert len(agent_script.calls()) == 1 and result_of(out)["status"] == "completed"


def test_crash_inside_a_node_is_a_failed_result(agent_script):
    task = agent_script([ok()])

    def boom(task):
        raise ValueError("checks exploded")

    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan"), checks=boom)
    r = result_of(out)
    assert r["status"] == "failed" and r["stop_cause"] == "error" and "checks exploded" in r["summary"]
