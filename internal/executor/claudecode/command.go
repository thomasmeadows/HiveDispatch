package claudecode

import (
	"strconv"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

// Config is the worker-level executor configuration.
type Config struct {
	Binary string // default "claude"
	Model  string // default model when the repo config sets none
}

// planSchema is the structured output requested from Plan().
const planSchema = `{"type":"object","properties":{"files":{"type":"array","items":{"type":"string"}}},"required":["files"]}`

// buildArgs assembles the argv for a run or a plan. --bare is deliberately
// absent: it skips credential loading.
func buildArgs(cfg Config, rc repoconfig.ExecutorConfig, resume string, plan bool) []string {
	args := []string{"-p"}
	if plan {
		args = append(args, "--output-format", "json", "--permission-mode", "plan", "--json-schema", planSchema, "--no-session-persistence")
	} else {
		args = append(args, "--output-format", "stream-json", "--verbose", "--permission-mode", rc.PermissionMode)
		if resume != "" {
			args = append(args, "--resume", resume)
		}
	}
	if len(rc.Tools) > 0 {
		args = append(args, "--tools", strings.Join(rc.Tools, ","))
	}
	if len(rc.AllowedTools) > 0 && !plan {
		args = append(args, "--allowedTools")
		args = append(args, rc.AllowedTools...)
	}
	if rc.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(rc.MaxBudgetUSD, 'f', -1, 64))
	}
	if model := firstNonEmpty(rc.Model, cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	return args
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
