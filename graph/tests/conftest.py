import json
import os
import sys
from pathlib import Path

import pytest

from hivegraph.task import ChatModel, Task

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))


def ok(summary="did it", session="s1", **kw):
    """An agent-run result: a completed CLI run with one step."""
    r = {
        "status": "completed",
        "stop_cause": "",
        "summary": summary,
        "question": "",
        "resume_token": session,
        "changed_files": ["a.go"],
        "retry_after": "",
        "steps": [
            {
                "kind": "tool",
                "name": "Bash",
                "input": "{}",
                "output": "ok",
                "is_error": False,
                "start": "2026-10-01T12:00:00Z",
                "end": "2026-10-01T12:00:01Z",
            }
        ],
        "usage": {"model": "claude-opus-5-5", "input_tokens": 10, "output_tokens": 2, "cost_usd": 0.01},
    }
    r.update(kw)
    return r


@pytest.fixture
def agent_script(tmp_path, monkeypatch):
    """set(results, **task fields) -> a Task whose hivedispatch is the fake
    agent-run; set.calls() lists what it was asked."""
    log = tmp_path / "calls.jsonl"
    script = tmp_path / "script.json"
    fake = tmp_path / "hivedispatch"
    fake.write_text(f'#!/bin/sh\nexec {sys.executable} {HERE / "fake_agent_run.py"} "$@"\n')
    fake.chmod(0o755)
    monkeypatch.setenv("FAKE_AGENT_SCRIPT", str(script))
    monkeypatch.setenv("FAKE_AGENT_LOG", str(log))

    def set_(results, **task_kw):
        script.write_text(json.dumps(results))
        Path(str(script) + ".n").unlink(missing_ok=True)
        kw = dict(
            prompt="Add a flag",
            workspace=str(tmp_path),
            hivedispatch=str(fake),
            base_ref="HEAD",
            git_dir="",
            chat_model=ChatModel(provider="fake", model="fake"),
        )
        kw.update(task_kw)
        return Task(**kw)

    def calls():
        if not log.exists():
            return []
        return [json.loads(line) for line in log.read_text().splitlines()]

    set_.calls = calls
    return set_


def events(out):
    return [json.loads(line) for line in out.getvalue().splitlines()]


@pytest.fixture(autouse=True)
def no_tracing(monkeypatch):
    for k in list(os.environ):
        if k.startswith(("LANGSMITH_", "LANGCHAIN_")):
            monkeypatch.delenv(k)
