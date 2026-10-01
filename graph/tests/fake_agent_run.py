#!/usr/bin/env python3
"""Stands in for `hivedispatch agent-run`: prints scripted results in order
and logs each call's argv and prompt."""

import json
import os
import sys

with open(os.environ["FAKE_AGENT_SCRIPT"]) as f:
    script = json.load(f)
counter = os.environ["FAKE_AGENT_SCRIPT"] + ".n"
n = 0
if os.path.exists(counter):
    with open(counter) as f:
        n = int(f.read())
with open(counter, "w") as f:
    f.write(str(n + 1))
with open(os.environ["FAKE_AGENT_LOG"], "a") as log:
    log.write(json.dumps({"argv": sys.argv[1:], "prompt": sys.stdin.read()}) + "\n")
item = script[min(n, len(script) - 1)]
if item == "garbage":
    print("not json")
    sys.exit(0)
if item == "crash":
    print("agent-run: cannot start claude", file=sys.stderr)
    sys.exit(1)
print(json.dumps(item))
