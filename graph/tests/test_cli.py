import json
import subprocess
import sys


def test_bad_task_is_a_failed_result():
    p = subprocess.run(
        [sys.executable, "-m", "hivegraph.cli", "run"], input="not json", capture_output=True, text=True
    )
    assert p.returncode == 0
    r = json.loads(p.stdout.strip().splitlines()[-1])
    assert r["type"] == "result" and r["status"] == "failed" and "bad task" in r["summary"]
