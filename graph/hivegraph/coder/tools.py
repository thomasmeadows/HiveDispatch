"""The coding agent's tools: files confined to the worktree, and a shell
limited to the policy's allowed command prefixes plus its checks.

Every tool returns a string. Problems come back as "error: ..." for the
agent to read and act on; they are never raised.
"""

import os
import re
import shlex
import subprocess
from pathlib import Path

READ_CAP = 16 * 1024
OUT_TAIL = 8 * 1024
MAX_LIST = 500
MAX_HITS = 200


class Workspace:
    def __init__(self, root, allowed, checks, path, command_timeout=600):
        self.root = Path(root).resolve()
        self.allowed_text = [a.strip() for a in allowed if a.strip()]
        self.allowed = [shlex.split(a) for a in self.allowed_text]
        self.checks = [c.strip() for c in checks if c.strip()]
        self.path = list(path)
        self.command_timeout = command_timeout
        self.changed: list[str] = []

    def _resolve(self, path: str) -> Path | str:
        """The real path inside the worktree, or an error. Symlinks are
        followed, so one pointing outside is refused too."""
        p = (self.root / path).resolve()
        if p != self.root and self.root not in p.parents:
            return f"error: {path} is outside the worktree"
        rel = p.relative_to(self.root)
        if rel.parts and rel.parts[0] == ".git":
            return f"error: {path} is inside .git"
        return p

    def _mark(self, p: Path) -> None:
        rel = str(p.relative_to(self.root))
        if rel not in self.changed:
            self.changed.append(rel)

    def read_file(self, path: str, offset: int = 0, limit: int = 2000) -> str:
        """Read a file's numbered lines, starting at line offset (0-based)."""
        p = self._resolve(path)
        if isinstance(p, str):
            return p
        if not p.is_file():
            return f"error: {path} is not a file"
        lines = p.read_text(errors="replace").splitlines()
        out = "\n".join(
            f"{i + 1}\t{line}" for i, line in enumerate(lines[offset : offset + limit], start=offset)
        )
        if len(out) > READ_CAP:
            out = out[:READ_CAP] + "\n…[truncated: read a smaller range]"
        return out

    def write_file(self, path: str, content: str) -> str:
        """Create or overwrite a file with content."""
        p = self._resolve(path)
        if isinstance(p, str):
            return p
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content)
        self._mark(p)
        return f"wrote {path} ({len(content)} bytes)"

    def edit_file(self, path: str, old: str, new: str) -> str:
        """Replace old with new in a file; old must match exactly once."""
        p = self._resolve(path)
        if isinstance(p, str):
            return p
        if not p.is_file():
            return f"error: {path} is not a file"
        text = p.read_text(errors="replace")
        n = text.count(old) if old else 0
        if n == 0:
            return f"error: old text not found in {path}"
        if n > 1:
            return f"error: old text matches {n} times in {path}; include more context"
        p.write_text(text.replace(old, new, 1))
        self._mark(p)
        return f"edited {path}"

    def _files(self, glob: str):
        for p in sorted(self.root.glob(glob)):
            rel = p.relative_to(self.root)
            if rel.parts and rel.parts[0] == ".git":
                continue
            if p.is_file() and isinstance(self._resolve(str(rel)), Path):
                yield p, str(rel)

    def list_files(self, glob: str = "**/*") -> str:
        """List files matching a glob, outside .git."""
        out = []
        for _, rel in self._files(glob):
            out.append(rel)
            if len(out) >= MAX_LIST:
                out.append(f"…[more than {MAX_LIST} files: narrow the glob]")
                break
        return "\n".join(out)

    def search(self, pattern: str, glob: str = "**/*") -> str:
        """Search text files for a Python regex; returns path:line: text."""
        try:
            rx = re.compile(pattern)
        except re.error as exc:
            return f"error: bad pattern: {exc}"
        hits = []
        for p, rel in self._files(glob):
            try:
                text = p.read_text()
            except (UnicodeDecodeError, OSError):
                continue
            for n, line in enumerate(text.splitlines(), start=1):
                if rx.search(line):
                    hits.append(f"{rel}:{n}: {line}")
                    if len(hits) >= MAX_HITS:
                        return "\n".join(hits) + f"\n…[more than {MAX_HITS} hits]"
        return "\n".join(hits)

    def _env(self) -> dict[str, str]:
        env = dict(os.environ)
        if self.path:
            env["PATH"] = os.pathsep.join([*self.path, env.get("PATH", "")])
        return env

    def run_command(self, command: str) -> str:
        """Run an allowed command in the worktree. A command is split into
        words and run without a shell, so ;, |, &&, $(...) and redirects are
        literal arguments. The policy's checks run exactly as written."""
        cmd = command.strip()
        if cmd in self.checks:
            argv, via = ["sh", "-c", cmd], " (via sh)"
        else:
            try:
                argv = shlex.split(cmd)
            except ValueError as exc:
                return f"error: cannot parse command: {exc}"
            if not argv or not any(argv[: len(a)] == a for a in self.allowed):
                allowed = ", ".join(self.allowed_text + self.checks) or "nothing"
                return f"error: not allowed: {cmd!r}. Allowed command prefixes: {allowed}"
            via = ""
        try:
            p = subprocess.run(
                argv,
                cwd=self.root,
                env=self._env(),
                capture_output=True,
                text=True,
                errors="replace",
                timeout=self.command_timeout,
            )
        except subprocess.TimeoutExpired:
            return f"error: timed out after {self.command_timeout}s"
        except OSError as exc:
            return f"error: {exc}"
        out = (p.stdout + p.stderr)[-OUT_TAIL:]
        return f"{out}\n(exit {p.returncode}{via})".strip()
