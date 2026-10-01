import json
import os
import signal
import subprocess
import sys
import time


def test_sigterm_lets_agent_run_clean_up_its_cli(tmp_path):
    """The worker stops a run with SIGTERM to the whole group. hivegraph must
    let agent-run finish its own cleanup (killing the CLI) rather than
    SIGKILL it, and must report the run as killed."""
    started, cleaned = tmp_path / "started", tmp_path / "cleaned"
    reply = tmp_path / "reply.json"
    reply.write_text(json.dumps({"status": "failed", "stop_cause": "killed", "summary": "interrupted"}))
    fake = tmp_path / "hivedispatch"
    fake.write_text(
        "#!/bin/sh\n"
        f"trap 'sleep 0.5; touch {cleaned}; cat {reply}; exit 0' TERM\n"
        "cat > /dev/null\n"
        f"touch {started}\n"
        "while :; do sleep 0.05; done\n"
    )
    fake.chmod(0o755)
    task = {
        "prompt": "x",
        "workspace": str(tmp_path),
        "hivedispatch": str(fake),
        "git_dir": "",
        "chat_model": {"provider": "openai", "model": "m", "base_url": "http://127.0.0.1:9"},
    }
    env = {k: v for k, v in os.environ.items() if not k.startswith(("LANGSMITH_", "LANGCHAIN_"))}
    p = subprocess.Popen(
        [sys.executable, "-m", "hivegraph.cli", "run"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        text=True,
        env=env,
        start_new_session=True,
    )
    p.stdin.write(json.dumps(task))
    p.stdin.close()
    deadline = time.time() + 60
    while not started.exists():
        assert time.time() < deadline, "agent-run never started"
        time.sleep(0.05)
    os.killpg(p.pid, signal.SIGTERM)
    out = p.stdout.read()
    p.wait(timeout=30)
    assert cleaned.exists(), "agent-run was killed before it could clean up its CLI"
    result = json.loads(out.strip().splitlines()[-1])
    assert result["type"] == "result" and result["status"] == "failed" and result["stop_cause"] == "killed", (
        result
    )
