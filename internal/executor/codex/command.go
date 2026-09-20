package codex

import (
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

// Config is the worker-level executor configuration.
type Config struct {
	Binary string // default "codex"
	Model  string // default model when the repo config sets none
}

// planSchema is the output schema requested from Plan(); Codex takes it as
// a file path, so it is written to a temporary file per call.
const planSchema = `{"type":"object","properties":{"files":{"type":"array","items":{"type":"string"}}},"required":["files"],"additionalProperties":false}`

// buildArgs assembles the argv for a run or a plan. Options precede the
// optional `resume` subcommand, and the trailing "-" reads the prompt from
// stdin in both forms. Approvals are always off: nobody is there to answer.
func buildArgs(cfg Config, rc repoconfig.ExecutorConfig, resume string, plan bool, schemaPath string) []string {
	args := []string{"exec", "--json", "--skip-git-repo-check", "-c", "approval_policy=never"}
	if plan {
		args = append(args, "--sandbox", "read-only", "--ephemeral", "--output-schema", schemaPath)
	} else {
		args = append(args, "--sandbox", rc.Codex.Sandbox)
		if rc.Codex.Network {
			args = append(args, "-c", "sandbox_workspace_write.network_access=true")
		}
	}
	if model := firstNonEmpty(rc.Codex.Model, cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	if resume != "" && !plan {
		args = append(args, "resume", resume)
	}
	return append(args, "-")
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
