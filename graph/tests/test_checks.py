from hivegraph.checks import run_checks
from hivegraph.task import Task


def test_checks_pass_fail_and_timeout(tmp_path):
    assert run_checks(Task(workspace=str(tmp_path), checks=["true"])).passed
    r = run_checks(Task(workspace=str(tmp_path), checks=["echo out; echo err >&2; exit 2", "true"]))
    assert not r.passed and len(r.failures) == 1
    assert "out" in r.failures[0].output and "err" in r.failures[0].output
    r = run_checks(Task(workspace=str(tmp_path), checks=["sleep 5"], check_timeout_seconds=1))
    assert not r.passed and "timed out after 1s" in r.failures[0].output


def test_checks_output_is_the_tail(tmp_path):
    t = Task(workspace=str(tmp_path), checks=["head -c 20000 /dev/zero | tr '\\0' a; echo END; exit 1"])
    out = run_checks(t).failures[0].output
    assert len(out) <= 8 * 1024 + 20 and "END" in out[-20:]


def test_path_is_extended(tmp_path):
    bindir = tmp_path / "bin"
    bindir.mkdir()
    tool = bindir / "mytool"
    tool.write_text("#!/bin/sh\nexit 0\n")
    tool.chmod(0o755)
    assert run_checks(Task(workspace=str(tmp_path), checks=["mytool"], path=[str(bindir)])).passed
