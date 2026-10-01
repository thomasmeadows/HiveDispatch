from conftest import ok

from hivegraph.agentrun import run_agent


def test_parses_result_and_passes_flags(agent_script):
    task = agent_script([ok(session="s9")], code_with="codex", model="m", step_budget=7)
    r = run_agent(task, "prompt text", resume="s1")
    assert r.status == "completed" and r.resume_token == "s9" and r.steps[0]["name"] == "Bash"
    call = agent_script.calls()[0]
    assert call["prompt"] == "prompt text"
    argv = call["argv"]
    assert argv[0] == "agent-run" and argv[argv.index("-executor") + 1] == "codex"
    assert argv[argv.index("-resume") + 1] == "s1" and argv[argv.index("-step-budget") + 1] == "7"


def test_garbage_and_crash_are_failures(agent_script):
    task = agent_script(["garbage"])
    assert run_agent(task, "p", resume="").status == "failed"
    task = agent_script(["crash"])
    r = run_agent(task, "p", resume="")
    assert r.status == "failed" and "cannot start claude" in r.summary
