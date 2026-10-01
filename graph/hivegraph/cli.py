"""hivegraph run: read a task on stdin, run the workflow, write events."""

import argparse
import signal
import sys

from . import stop
from .events import Emitter
from .task import Task


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="hivegraph")
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("run", help="run the workflow for the task on stdin")
    parser.parse_args(argv)
    # The worker stops a run with SIGTERM to the whole process group; see
    # stop.py for why this only sets a flag.
    signal.signal(signal.SIGTERM, stop.request)
    emitter = Emitter()
    try:
        task = Task.from_json(sys.stdin.read())
    except Exception as exc:  # any bad input is a failed run
        emitter.result("failed", "error", f"hivegraph: bad task: {exc}", "", "", [], False)
        return 0
    from .graph import run_task  # imported late: langgraph is slow to import

    return run_task(task, emitter)


if __name__ == "__main__":
    sys.exit(main())
