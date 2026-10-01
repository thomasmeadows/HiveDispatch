"""What each node says."""

PLAN = """You are planning a code change another agent will make. Read the ticket and reply with a short,
numbered plan (at most 8 steps) naming the files to touch and how to verify the change. No preamble.

Ticket:
{prompt}

Repository guidance:
{guidance}
"""

CODE_FIRST = """{prompt}

## Plan

{plan}
"""

CODE_FIX = """The repository's checks fail after your change. Fix the code so they pass;
do not weaken the checks.

{report}
"""

CODE_REVIEW = """A review of your change asks for the following. Address each point.

{findings}
"""

REVIEW = """Review this diff against the plan. Reply with JSON only:
{{"verdict": "approve" | "changes", "findings": "what must change, or empty"}}
Ask for changes only for real defects or missed plan steps, not style.

Plan:
{plan}

Diff:
{diff}
"""
