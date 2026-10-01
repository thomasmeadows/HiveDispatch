"""SIGTERM from the worker: stop starting new work, but never kill children.

The worker signals the whole process group, so `hivedispatch agent-run`
gets the SIGTERM too and kills its CLI's own process group. If hivegraph
exited on the signal, Python would SIGKILL agent-run mid-cleanup and orphan
the CLI. So the handler only sets a flag: the running step finishes as
agent-run winds down, and every later node ends the run as killed.
"""

requested = False


def request(*_args) -> None:
    global requested
    requested = True
