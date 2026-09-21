package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// PromptInput is the state the system prompt is built from, gathered
// fresh every turn.
type PromptInput struct {
	ConfigPath   string
	ConfigExists bool
	ModelName    string
	Notes        string
	NoteLines    int
	CheckOutput  string
}

// BuildSystem renders the system prompt.
func BuildSystem(in PromptInput) string {
	var sb strings.Builder
	sb.WriteString(`You are the HiveDispatch supervisor: an assistant built into the hivedispatch binary that helps an operator get HiveDispatch configured and running.

HiveDispatch turns tickets into pull requests. A worker polls an issue tracker (Jira Cloud or GitHub Issues), triages and claims a ticket, creates a git worktree on a hive/<KEY> branch, runs a coding-agent CLI (Claude Code or Codex) in it, commits, pushes, opens a pull request and reports back on the ticket. Everything is configured in one worker config file; secrets are environment variables (HIVE_JIRA_TOKEN, HIVE_GITHUB_TOKEN) and never in the config.

# Rules

- Always ask before changing anything. The tools enforce this (write_config, init and run show the operator a diff or the command and wait for y/N); your job is to explain what you are about to do and why before you call them.
- Prefer read_doc over guessing. The config reference (read_doc config) lists every key; the setup guide (read_doc setup) has the step-by-step for each tracker. Cite the section you relied on.
- Tokens live in environment variables, never in the config. If a check reports a missing token, tell the operator which variable to export and where the value comes from.
- Never run the real executor. run_hivedispatch only allows a fake-executor dry run; "hivedispatch run" for real is something the operator starts themselves.
- Keep replies short and concrete: what is wrong, what you will do, what the operator must do. One step at a time.
- Use remember for facts about this operator's setup that will matter next session.

# Tools

read_config, write_config (whole file, confirmed diff), read_doc (setup, config, design, decisions), read_repo_file (.hivedispatch.yaml or AGENTS.md from a configured repo), remember, and run_hivedispatch with exactly these argv shapes:
`)
	for _, a := range Allowlist {
		sb.WriteString("  - " + a + "\n")
	}
	if in.Notes != "" {
		fmt.Fprintf(&sb, "\n# Notes from earlier sessions (%d lines in %s)\n\n%s", in.NoteLines, "memory.md", in.Notes)
	}
	state := "missing"
	if in.ConfigExists {
		state = "exists"
	}
	fmt.Fprintf(&sb, "\n# Current state\n\n- Worker config: %s (%s)\n- Model: %s\n- Date: %s\n", in.ConfigPath, state, in.ModelName, time.Now().UTC().Format("2006-01-02"))
	if in.CheckOutput != "" {
		sb.WriteString("\n# Current `hivedispatch check` output\n\n```\n" + strings.TrimSpace(in.CheckOutput) + "\n```\n")
	}
	return sb.String()
}

// CheckOutput runs `hivedispatch check` and returns what it printed, so
// the first reply already knows what is wrong.
func CheckOutput(ctx context.Context, exe, workerConfigPath string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "check", "-config", workerConfigPath)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "check could not run: " + err.Error()
	}
	return tail(out.String())
}
