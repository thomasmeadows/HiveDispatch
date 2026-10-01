"""The repository's checks (policy.yaml `checks:`), run after each code step."""

import os
import subprocess
from dataclasses import dataclass, field

TAIL = 8 * 1024


@dataclass
class CheckFailure:
    command: str
    output: str


@dataclass
class CheckReport:
    passed: bool
    failures: list[CheckFailure] = field(default_factory=list)

    def describe(self) -> str:
        return "\n\n".join(f"$ {f.command}\n{f.output}" for f in self.failures)


def _env(task) -> dict[str, str]:
    env = dict(os.environ)
    if task.path:
        env["PATH"] = os.pathsep.join([*task.path, env.get("PATH", "")])
    return env


def _text(b) -> str:
    if b is None:
        return ""
    return b.decode(errors="replace") if isinstance(b, bytes) else b


def run_checks(task) -> CheckReport:
    failures = []
    for cmd in task.checks:
        try:
            p = subprocess.run(
                ["sh", "-c", cmd],
                cwd=task.workspace,
                env=_env(task),
                capture_output=True,
                text=True,
                errors="replace",
                timeout=task.check_timeout_seconds,
            )
        except subprocess.TimeoutExpired as exc:
            out = (_text(exc.stdout) + _text(exc.stderr))[-TAIL:]
            failures.append(
                CheckFailure(cmd, (out + f"\n(timed out after {task.check_timeout_seconds}s)").strip())
            )
            continue
        if p.returncode != 0:
            out = (p.stdout + p.stderr)[-TAIL:]
            failures.append(CheckFailure(cmd, (out + f"\n(exit {p.returncode})").strip()))
    return CheckReport(passed=not failures, failures=failures)
