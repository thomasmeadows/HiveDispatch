"""JSONL events on stdout: the protocol between hivegraph and the Go worker."""

import json
import sys
from datetime import UTC, datetime
from typing import TextIO


def stamp(t: datetime) -> str:
    return t.astimezone(UTC).isoformat().replace("+00:00", "Z")


class Emitter:
    """Writes one JSON object per line and flushes, so the worker sees each
    event as it happens."""

    def __init__(self, out: TextIO | None = None):
        self.out = out or sys.stdout

    def _write(self, event: dict) -> None:
        self.out.write(json.dumps(event) + "\n")
        self.out.flush()

    def node(self, name: str, start: datetime, end: datetime) -> None:
        self._write({"type": "node", "name": name, "start": stamp(start), "end": stamp(end)})

    def step(
        self,
        kind: str,
        name: str,
        input: str,
        output: str,
        is_error: bool,
        start: datetime,
        end: datetime | None,
    ) -> None:
        self._write(
            {
                "type": "step",
                "kind": kind,
                "name": name,
                "input": input,
                "output": output,
                "is_error": is_error,
                "start": stamp(start),
                "end": stamp(end) if end else "",
            }
        )

    def usage(self, model: str, input_tokens: int, output_tokens: int, cost_usd: float = 0.0) -> None:
        self._write(
            {
                "type": "usage",
                "model": model,
                "input_tokens": input_tokens,
                "output_tokens": output_tokens,
                "cost_usd": cost_usd,
            }
        )

    def result(
        self,
        status: str,
        stop_cause: str,
        summary: str,
        question: str,
        resume_token: str,
        changed_files: list[str],
        steps_traced: bool,
    ) -> None:
        self._write(
            {
                "type": "result",
                "status": status,
                "stop_cause": stop_cause,
                "summary": summary,
                "question": question,
                "resume_token": resume_token,
                "changed_files": list(changed_files),
                "steps_traced": steps_traced,
            }
        )
