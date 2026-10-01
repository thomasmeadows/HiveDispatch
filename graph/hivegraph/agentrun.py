"""The code node's CLI: `hivedispatch agent-run`, which runs Claude Code or
Codex through the worker's own executors and prints the result as JSON."""

import json
import subprocess
from dataclasses import dataclass, field, fields


@dataclass
class AgentResult:
    status: str = "failed"
    stop_cause: str = "error"
    summary: str = ""
    question: str = ""
    resume_token: str = ""
    changed_files: list[str] = field(default_factory=list)
    retry_after: str = ""
    steps: list[dict] = field(default_factory=list)
    usage: dict = field(default_factory=dict)


def run_agent(task, prompt: str, resume: str) -> AgentResult:
    argv = [
        task.hivedispatch,
        "agent-run",
        "-executor",
        task.code_with,
        "-workspace",
        task.workspace,
        "-step-budget",
        str(task.step_budget),
    ]
    if task.worker_config:
        argv += ["-config", task.worker_config]
    if task.model:
        argv += ["-model", task.model]
    if resume:
        argv += ["-resume", resume]
    try:
        p = subprocess.run(argv, input=prompt, capture_output=True, text=True, errors="replace")
    except OSError as exc:
        return AgentResult(summary=f"agent-run: {exc}")
    if p.returncode != 0:
        return AgentResult(summary=f"agent-run exited {p.returncode}: {p.stderr.strip()[-1000:]}")
    try:
        data = json.loads(p.stdout)
    except json.JSONDecodeError:
        return AgentResult(summary=f"agent-run printed no result: {(p.stdout + p.stderr).strip()[-1000:]}")
    known = {f.name for f in fields(AgentResult)}
    return AgentResult(**{k: v for k, v in data.items() if k in known and v is not None})
