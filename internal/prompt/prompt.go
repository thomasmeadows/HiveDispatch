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
	fmt.Fprintf(&sb, "# Ticket %s: %s\n", t.Key, t.Summary)
	if t.URL != "" {
		fmt.Fprintf(&sb, "%s\n", t.URL)
	}
	sb.WriteString("\n## Description\n\n")
	if strings.TrimSpace(t.Description) == "" {
		sb.WriteString("(no description)\n")
	} else {
		sb.WriteString(t.Description + "\n")
	}
	if len(t.Comments) > 0 {
		sb.WriteString("\n## Discussion\n\n")
		writeComments(&sb, t.Comments)
	}
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
