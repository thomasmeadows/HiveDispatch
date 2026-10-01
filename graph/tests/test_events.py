import io
import json
from datetime import UTC, datetime

from hivegraph.events import Emitter
from hivegraph.task import Task


def test_emitter_writes_one_flushed_line_per_event():
    out = io.StringIO()
    e = Emitter(out)
    t0 = datetime(2026, 10, 1, 12, 0, 0, tzinfo=UTC)
    e.node("plan", t0, t0)
    e.step("tool", "Bash", "{}", "ok", False, t0, t0)
    e.usage("m", 3, 1)
    e.result("completed", "", "done", "", "th", ["a.go"], True)
    lines = [json.loads(line) for line in out.getvalue().splitlines()]
    assert [line["type"] for line in lines] == ["node", "step", "usage", "result"]
    assert lines[1]["start"] == "2026-10-01T12:00:00Z"
    assert lines[3] == {
        "type": "result",
        "status": "completed",
        "stop_cause": "",
        "summary": "done",
        "question": "",
        "resume_token": "th",
        "changed_files": ["a.go"],
        "steps_traced": True,
    }


def test_task_from_json_fills_defaults():
    t = Task.from_json(
        '{"prompt": "x", "workspace": "/w", "chat_model": {"provider": "deepseek", "model": "m"}}'
    )
    assert t.prompt == "x" and t.checks == [] and t.max_fix_rounds == 3 and t.max_review_rounds == 1
    assert t.code_with == "claude" and t.chat_model.model == "m" and t.chat_model.api_key_env == ""
