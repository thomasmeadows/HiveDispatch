import subprocess

from hivegraph.graph import git_diff
from hivegraph.task import Task


def git(cwd, *args):
    subprocess.run(["git", "-C", str(cwd), *args], check=True, capture_output=True)


def test_review_diff_includes_new_files(tmp_path):
    git(tmp_path, "init", "-q")
    (tmp_path / "old.go").write_text("package old\n")
    git(tmp_path, "add", "-A")
    git(tmp_path, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-qm", "init")
    base = subprocess.run(
        ["git", "-C", str(tmp_path), "rev-parse", "HEAD"], capture_output=True, text=True
    ).stdout.strip()
    (tmp_path / "old.go").write_text("package old\n\nfunc A() {}\n")
    (tmp_path / "new.go").write_text("package old\n\nfunc NewThing() {}\n")
    diff = git_diff(Task(workspace=str(tmp_path), base_ref=base))
    assert "func A()" in diff
    assert "new.go" in diff and "func NewThing()" in diff
