// Package graphcli runs hivegraph, the LangGraph workflow, as a subprocess
// and parses its JSONL events. hivegraph runs Claude Code or Codex through
// `hivedispatch agent-run`, which kills its CLI on SIGTERM; so stopping a
// run sends SIGTERM to the group and waits before SIGKILL.
package graphcli

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

// Result is hivegraph's final "result" event.
type Result struct {
	Status       string   `json:"status"`
	StopCause    string   `json:"stop_cause"`
	Summary      string   `json:"summary"`
	Question     string   `json:"question"`
	ResumeToken  string   `json:"resume_token"`
	ChangedFiles []string `json:"changed_files"`
	StepsTraced  bool     `json:"steps_traced"`
}

// Transcript is what the parser learned from a run.
type Transcript struct {
	Steps  []executor.Step
	Usage  executor.Usage // summed; Model from the last usage event
	Nodes  []string       // node names, in order
	Result *Result
	Lines  int
}

type event struct {
	Type         string  `json:"type"`
	Kind         string  `json:"kind"`
	Name         string  `json:"name"`
	Input        string  `json:"input"`
	Output       string  `json:"output"`
	IsError      bool    `json:"is_error"`
	Start        string  `json:"start"`
	End          string  `json:"end"`
	Model        string  `json:"model"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Parser consumes hivegraph's stdout lines.
type Parser struct {
	onStep func(count int)
	tools  int
	t      Transcript
}

// NewParser returns a parser that calls onStep with the running count of
// tool steps, for the step budget.
func NewParser(onStep func(int)) *Parser { return &Parser{onStep: onStep} }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s) // zero for a missing or bad time
	return t
}

// Line consumes one line. Non-JSON lines are counted and ignored.
func (p *Parser) Line(raw []byte) {
	p.t.Lines++
	s := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(s, "{") {
		return
	}
	var e event
	if json.Unmarshal([]byte(s), &e) != nil {
		return
	}
	switch e.Type {
	case "node":
		p.t.Nodes = append(p.t.Nodes, e.Name)
	case "step":
		if len(p.t.Steps) < executor.MaxSteps {
			p.t.Steps = append(p.t.Steps, executor.Step{
				Kind: executor.StepKind(e.Kind), Name: e.Name, Input: executor.CapOutput(e.Input), Output: executor.CapOutput(e.Output),
				IsError: e.IsError, Start: parseTime(e.Start), End: parseTime(e.End),
			})
		}
		if e.Kind == string(executor.StepTool) {
			p.tools++
			if p.onStep != nil {
				p.onStep(p.tools)
			}
		}
	case "usage":
		p.t.Usage.InputTokens += e.InputTokens
		p.t.Usage.OutputTokens += e.OutputTokens
		p.t.Usage.CostUSD += e.CostUSD
		if e.Model != "" {
			p.t.Usage.Model = e.Model
		}
	case "result":
		var r Result
		if json.Unmarshal([]byte(s), &r) == nil {
			p.t.Result = &r
		}
	}
}

// Transcript returns what has been parsed so far.
func (p *Parser) Transcript() Transcript { return p.t }
