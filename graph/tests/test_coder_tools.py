import os
import subprocess

import pytest

from hivegraph.coder.tools import Workspace


@pytest.fixture
def ws(tmp_path):
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    (tmp_path / "a.txt").write_text("one\ntwo\nthree\n")
    return Workspace(str(tmp_path), allowed=["echo", "git status"], checks=["echo checked && true"], path=[])


def test_read_numbers_lines_and_pages(ws):
    assert ws.read_file("a.txt") == "1\tone\n2\ttwo\n3\tthree"
    assert ws.read_file("a.txt", offset=1, limit=1) == "2\ttwo"


def test_write_and_edit_track_changes(ws, tmp_path):
    assert ws.write_file("sub/new.go", "package x\n").startswith("wrote")
    assert (tmp_path / "sub" / "new.go").read_text() == "package x\n"
    assert ws.edit_file("a.txt", "two", "TWO").startswith("edited")
    assert "TWO" in (tmp_path / "a.txt").read_text()
    assert ws.changed == ["sub/new.go", "a.txt"]


def test_edit_must_match_exactly_once(ws):
    assert ws.edit_file("a.txt", "nope", "x") == "error: old text not found in a.txt"
    ws.write_file("b.txt", "x x\n")
    assert ws.edit_file("b.txt", "x", "y") == "error: old text matches 2 times in b.txt; include more context"


@pytest.mark.parametrize("bad", ["../escape.txt", "/etc/passwd", ".git/config", "sub/../../x"])
def test_paths_outside_the_worktree_are_refused(ws, bad):
    assert ws.read_file(bad).startswith("error:")
    assert ws.write_file(bad, "x").startswith("error:")


def test_symlink_out_is_refused(ws, tmp_path):
    outside = tmp_path.parent / f"outside-{tmp_path.name}.txt"
    outside.write_text("secret")
    os.symlink(outside, tmp_path / "link.txt")
    assert ws.read_file("link.txt").startswith("error:")
    assert ws.write_file("link.txt", "x").startswith("error:")
    assert outside.read_text() == "secret"


def test_list_and_search_skip_git(ws):
    ws.write_file("src/m.go", "func Main() {}\n")
    files = ws.list_files().splitlines()
    assert "a.txt" in files and "src/m.go" in files and not any(f.startswith(".git") for f in files)
    assert ws.search("func Main") == "src/m.go:1: func Main() {}"
    assert ws.search("([").startswith("error:")


def test_allowed_command_runs(ws):
    out = ws.run_command("echo hello world")
    assert "hello world" in out and "exit 0" in out


@pytest.mark.parametrize("cmd", ["rm -rf .", "echoo hi", "git push", "sh -c 'echo hi'"])
def test_disallowed_commands_are_refused(ws, cmd):
    out = ws.run_command(cmd)
    assert out.startswith("error: not allowed") and "echo" in out


def test_shell_syntax_cannot_smuggle(ws, tmp_path):
    out = ws.run_command(f"echo hi; touch {tmp_path}/pwned")
    assert not (tmp_path / "pwned").exists()
    assert "hi;" in out  # the ; is a literal argument to echo
    ws.run_command(f"echo $(touch {tmp_path}/pwned2)")
    assert not (tmp_path / "pwned2").exists()
    ws.run_command(f"echo x > {tmp_path}/pwned3")
    assert not (tmp_path / "pwned3").exists()
    assert ws.run_command("echo 'unbalanced").startswith("error:")


def test_checks_run_through_the_shell(ws):
    out = ws.run_command("echo checked && true")
    assert "checked" in out and "exit 0" in out


def test_command_timeout(tmp_path):
    w = Workspace(str(tmp_path), allowed=["sleep"], checks=[], path=[], command_timeout=1)
    assert "timed out after 1s" in w.run_command("sleep 5")
