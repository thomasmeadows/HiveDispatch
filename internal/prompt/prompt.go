// Package prompt renders tickets into executor prompts.
package prompt

import (
	"fmt"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// NeedsInputMarker is the line prefix an executor uses to ask a question.
const NeedsInputMarker = "HIVE_NEEDS_INPUT:"

// Render builds the initial prompt for a ticket.
func Render(t tracker.Ticket, repo config.RepoConfig, branch string) string {
	var sb strings.Builder
	ticketBody(&sb, t)
	sb.WriteString(instructions(repo, branch))
	return sb.String()
}

// RenderResume builds the prompt for continuing after a human replied.
// Only comments created after since that are not the orchestrator's own
// are included.
func RenderResume(t tracker.Ticket, since time.Time, isOurs func(tracker.Comment) bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Resuming ticket %s: %s\n\n", t.Key, t.Summary)
	sb.WriteString("You previously asked for input. New replies:\n\n")
	var fresh []tracker.Comment
	for _, c := range t.Comments {
		if c.Created.After(since) && !isOurs(c) {
			fresh = append(fresh, c)
		}
	}
	writeComments(&sb, fresh)
	sb.WriteString("\nContinue the work with this information. The same rules as before apply.\n")
	return sb.String()
}

func writeComments(sb *strings.Builder, cs []tracker.Comment) {
	for _, c := range cs {
		fmt.Fprintf(sb, "- [%s] %s: %s\n", c.Created.UTC().Format("2006-01-02 15:04"), c.Author, strings.ReplaceAll(c.Body, "\n", "\n  "))
	}
}

func instructions(repo config.RepoConfig, branch string) string {
	return fmt.Sprintf(`
## Instructions

You are working in the repository %s on branch %s (based on %s).
Implement what the ticket asks and nothing more.
Commit as you go with clear messages; do not leave work uncommitted.
Run the project's tests before you finish.
Do not merge, do not push, and do not touch other branches.
If you cannot proceed without information only a human has, stop and end your
reply with a single line starting with %s followed by the question.
When you are done, end your reply with a short summary of what changed.
`, repo.Name, branch, repo.DefaultBranch, NeedsInputMarker)
}

// ticketBody writes a ticket's heading, description and discussion.
func ticketBody(sb *strings.Builder, t tracker.Ticket) {
	fmt.Fprintf(sb, "# Ticket %s: %s\n", t.Key, t.Summary)
	if t.URL != "" {
		fmt.Fprintf(sb, "%s\n", t.URL)
	}
	sb.WriteString("\n## Description\n\n")
	if strings.TrimSpace(t.Description) == "" {
		sb.WriteString("(no description)\n")
	} else {
		sb.WriteString(t.Description + "\n")
	}
	if len(t.Comments) > 0 {
		sb.WriteString("\n## Discussion\n\n")
		writeComments(sb, t.Comments)
	}
}

// RenderPlanning is the planning agent's prompt: read the repository, then
// either plan the ticket or ask one question.
func RenderPlanning(t tracker.Ticket, repo config.RepoConfig) string {
	var sb strings.Builder
	sb.WriteString(`You are the planning agent of an automated pipeline that turns tickets into pull requests.
A coding agent will implement this ticket from your plan. You may read the repository but
change nothing.

Answer in the requested JSON shape:
- "decision": "plan" when the ticket is clear enough to implement, "needs_info" when only its author can settle something.
- "plan": for "plan", a concise implementation plan in Markdown: the files and functions to change,
  the approach, the tests to add or update, and anything the coding agent must not break.
- "question": for "needs_info", ONE clear question for the ticket's author.

`)
	ticketBody(&sb, t)
	fmt.Fprintf(&sb, "\n## Repository\n\n%s, default branch %s.\n", repo.Name, repo.DefaultBranch)
	return sb.String()
}

// RenderReview is the review agent's prompt: the ticket and the pull
// request's diff, to review against it.
func RenderReview(t tracker.Ticket, repo config.RepoConfig, branch, prURL, diff string) string {
	var sb strings.Builder
	sb.WriteString(`You are the review agent of an automated pipeline that turns tickets into pull requests.
A coding agent implemented the ticket below; review its pull request. The repository is checked
out on the pull request's branch, so you may read any file for context, but change nothing.

Check that the change does what the ticket asks and nothing unrelated, that it is correct, that
it is tested, and that it follows the repository's conventions. Do not ask for matters of taste.

Answer in the requested JSON shape:
- "verdict": "approve" when a human could merge it as is, "request_changes" when it needs work.
- "summary": your review in Markdown. For request_changes, list each change needed, specifically
  enough for the coding agent to act on it.

`)
	ticketBody(&sb, t)
	fmt.Fprintf(&sb, "\n## Pull request\n\n%s\nBranch %s into %s of %s.\n\n```diff\n%s\n```\n", prURL, branch, repo.DefaultBranch, repo.Name, diff)
	return sb.String()
}

// RenderReviewFix is the coding agent's prompt after a review asked for
// changes.
func RenderReviewFix(t tracker.Ticket, repo config.RepoConfig, branch, review string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Review of ticket %s: %s\n\n", t.Key, t.Summary)
	sb.WriteString("A reviewer asked for changes to your pull request:\n\n")
	sb.WriteString(strings.TrimSpace(review) + "\n\n")
	sb.WriteString("Make those changes on the same branch.\n")
	sb.WriteString(instructions(repo, branch))
	return sb.String()
}
