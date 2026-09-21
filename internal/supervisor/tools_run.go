package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

const (
	runTimeout  = 5 * time.Minute
	runMaxBytes = 32 << 10
)

// Allowlist is every argv shape run_hivedispatch may execute, in canonical
// flag order.
var Allowlist = []string{
	"version",
	"check", "check -live",
	"init", "init -jira", "init -github",
	"status", "status -json",
	"run -once -executor fake", "run -once -executor fake -placeholder",
}

// effects explains what a mutating shape does, for the confirmation.
var effects = map[string]string{
	"init":                                  "writes a starter worker config (never overwrites one)",
	"init -jira":                            "creates the two claim custom fields in Jira",
	"init -github":                          "creates the hive:* labels in every configured repository",
	"run -once -executor fake":              "polls the tracker once and, for a ready ticket, claims it, branches and reports — with no coding agent",
	"run -once -executor fake -placeholder": "polls the tracker once and, for a ready ticket, claims it, writes a placeholder file, pushes a branch and opens a pull request",
}

// Allowed matches args against the allowlist regardless of flag order.
func Allowed(args []string) (canonical string, mutating bool, ok bool) {
	if len(args) == 0 {
		return "", false, false
	}
	key := args[0] + " " + sortedFlags(args[1:])
	for _, a := range Allowlist {
		parts := strings.Fields(a)
		if parts[0]+" "+sortedFlags(parts[1:]) == key {
			_, mutating = effects[a]
			return a, mutating, true
		}
	}
	return "", false, false
}

func sortedFlags(flags []string) string {
	// Pair each flag with its value so "-executor fake" stays together.
	var items []string
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		if i+1 < len(flags) && !strings.HasPrefix(flags[i+1], "-") {
			f += " " + flags[i+1]
			i++
		}
		items = append(items, f)
	}
	sort.Strings(items)
	return strings.Join(items, " ")
}

type runHivedispatch struct {
	exe, configPath string
	confirm         func(string) bool
}

// NewRunHivedispatch returns the tool that re-executes this binary with an
// allowlisted argv.
func NewRunHivedispatch(exe, workerConfigPath string, confirm func(string) bool) Tool {
	return runHivedispatch{exe: exe, configPath: workerConfigPath, confirm: confirm}
}

// Def returns the tool definition for run_hivedispatch.
func (runHivedispatch) Def() model.ToolDef {
	return model.ToolDef{
		Name: "run_hivedispatch",
		Description: "Run a hivedispatch subcommand and get its exit code and output. Allowed: " + strings.Join(Allowlist, "; ") +
			". -config is added for you. init and run ask the operator first. A non-zero exit is normal — read the output and explain it.",
		Schema: []byte(`{"type":"object","properties":{"args":{"type":"array","items":{"type":"string"},"description":"subcommand and flags, e.g. [\"check\",\"-live\"]"}},"required":["args"]}`),
	}
}

// Call executes the allowed hivedispatch subcommand with captured output.
func (r runHivedispatch) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Args []string `json:"args"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	canonical, mutating, ok := Allowed(in.Args)
	if !ok {
		return "", fmt.Errorf("refused: %q is not allowed; allowed: %s", strings.Join(in.Args, " "), strings.Join(Allowlist, "; "))
	}
	if mutating && !r.confirm(fmt.Sprintf("Run `hivedispatch %s`? It %s.", canonical, effects[canonical])) {
		return "declined by user; nothing was run", nil
	}
	argv := append([]string{in.Args[0], "-config", r.configPath}, in.Args[1:]...)
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.exe, argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	case err != nil:
		return "", fmt.Errorf("start hivedispatch %s: %w", canonical, err)
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("hivedispatch %s: timed out after %s", canonical, runTimeout)
	}
	return fmt.Sprintf("exit %d\n--- stdout ---\n%s\n--- stderr ---\n%s", code, tail(stdout.String()), tail(stderr.String())), nil
}

// tail returns the last runMaxBytes of s, or s itself if shorter.
func tail(s string) string {
	if len(s) <= runMaxBytes {
		return s
	}
	return "…(truncated)…\n" + s[len(s)-runMaxBytes:]
}
