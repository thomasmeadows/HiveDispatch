package claudecode

import (
	"fmt"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// decisionSchema is the structured output the triager must return.
const decisionSchema = `{"type":"object","properties":{"decision":{"type":"string","enum":["dispatch","needs_info","reject"]},"reason":{"type":"string"},"question":{"type":"string"},"complexity":{"type":"integer","minimum":1,"maximum":5},"notes":{"type":"string"}},"required":["decision","reason"]}`

type decisionJSON struct {
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
	Question   string `json:"question"`
	Complexity int    `json:"complexity"`
	Notes      string `json:"notes"`
}

func buildPrompt(in triage.Input) string {
	var sb strings.Builder
	sb.WriteString(`You are the triage step of an automated pipeline that turns tickets into pull requests.
A coding agent will attempt this ticket in this repository if you decide "dispatch".
Your job is to decide whether it is worth attempting, using the repository to check.

Use Read, Grep and Glob to answer:
- Does the area the ticket describes exist in this repository?
- Is the request specific enough that an agent could finish it without guessing?
- Is it small enough for one pull request (not architectural or cross-cutting)?

Decide:
- "dispatch": actionable. Put concrete pointers for the agent in "notes" (files, functions, conventions you saw).
- "needs_info": underspecified. Put ONE clear question for the ticket author in "question".
- "reject": out of scope for automation (too large, architectural, or not in this repo). Explain in "reason".

Set "complexity" from 1 (trivial) to 5 (large). Do not modify anything.

`)
	fmt.Fprintf(&sb, "# Ticket %s: %s\n\n", in.Ticket.Key, in.Ticket.Summary)
	if in.Ticket.URL != "" {
		fmt.Fprintf(&sb, "%s\n\n", in.Ticket.URL)
	}
	sb.WriteString("## Description\n\n")
	if strings.TrimSpace(in.Ticket.Description) == "" {
		sb.WriteString("(no description)\n")
	} else {
		sb.WriteString(in.Ticket.Description + "\n")
	}
	if len(in.Ticket.Comments) > 0 {
		sb.WriteString("\n## Discussion\n\n")
		for _, c := range in.Ticket.Comments {
			fmt.Fprintf(&sb, "- [%s] %s: %s\n", c.Created.UTC().Format("2006-01-02 15:04"), c.Author, strings.ReplaceAll(c.Body, "\n", "\n  "))
		}
	}
	fmt.Fprintf(&sb, "\n## Repository\n\n%s, default branch %s. Work would happen on branch %s.\n", in.Repo.Name, in.Repo.DefaultBranch, in.Branch)
	if in.Attempts > 0 {
		fmt.Fprintf(&sb, "\n## History\n\nThis ticket has been attempted %d time(s). Last stop cause: %s. If earlier attempts failed for a reason a better prompt cannot fix, prefer needs_info or reject.\n", in.Attempts, orNone(in.LastStopCause))
	}
	return sb.String()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// toDecision validates the model's output and renders the executor prompt.
func toDecision(d decisionJSON, in triage.Input) (triage.Decision, error) {
	out := triage.Decision{Reason: strings.TrimSpace(d.Reason), Complexity: d.Complexity}
	switch triage.Kind(d.Decision) {
	case triage.KindDispatch:
		out.Kind = triage.KindDispatch
		out.Prompt = prompt.Render(in.Ticket, in.Repo, in.Branch)
		if n := strings.TrimSpace(d.Notes); n != "" {
			out.Prompt += "\n## Triage notes\n\n" + n + "\n"
		}
	case triage.KindNeedsInfo:
		q := strings.TrimSpace(d.Question)
		if q == "" {
			return out, fmt.Errorf("triage: needs_info without a question")
		}
		out.Kind = triage.KindNeedsInfo
		out.Question = q
	case triage.KindReject:
		out.Kind = triage.KindReject
		if out.Reason == "" {
			out.Reason = "rejected by triage"
		}
	default:
		return out, fmt.Errorf("triage: unknown decision %q", d.Decision)
	}
	return out, nil
}
