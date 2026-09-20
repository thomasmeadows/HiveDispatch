package dispatch

import (
	"fmt"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Marker prefixes every comment the dispatcher posts so it can recognise
// its own comments when looking for human replies.
const Marker = "[HiveDispatch]"

func isOurs(c tracker.Comment) bool {
	return strings.HasPrefix(c.Body, Marker)
}

const resumeHint = "Reply in this thread, then move the ticket back to Ready to resume."

func reportTriageNeedsInfo(question string) string {
	return fmt.Sprintf("%s Before starting, I need more information:\n\n%s\n\n%s", Marker, question, resumeHint)
}

func reportRejected(reason string) string {
	return fmt.Sprintf("%s Not attempting this ticket automatically: %s\n\nA human should pick this up.", Marker, reason)
}

func reportNeedsInput(question string) string {
	return fmt.Sprintf("%s I need input before continuing:\n\n%s\n\n%s", Marker, question, resumeHint)
}

func reportCompleted(res executor.Result, pr *githost.PR, branch string, pushed bool) string {
	var sb strings.Builder
	switch {
	case pr != nil:
		fmt.Fprintf(&sb, "%s Opened %s from branch `%s`.", Marker, pr.URL, branch)
	case pushed:
		fmt.Fprintf(&sb, "%s Pushed branch `%s` but could not open a PR (no GitHub credentials configured) — please open it by hand.", Marker, branch)
	default:
		fmt.Fprintf(&sb, "%s Finished with no code changes.", Marker)
	}
	if s := strings.TrimSpace(res.Summary); s != "" {
		sb.WriteString("\n\n" + s)
	}
	return sb.String()
}

func reportFailed(res executor.Result, attempts, maxAttempts int, branch string, pushed, exhausted bool, now time.Time) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s Stopped at %s — %s. Attempt %d of %d.", Marker, now.UTC().Format("15:04 UTC"), res.StopCause.Describe(), attempts, maxAttempts)
	if pushed {
		fmt.Fprintf(&sb, "\n\nWork so far is committed to `%s`. No PR was opened.", branch)
	} else {
		sb.WriteString("\n\nNo changes were made.")
	}
	if s := strings.TrimSpace(res.Summary); s != "" {
		sb.WriteString("\n\n" + s)
	}
	if exhausted {
		sb.WriteString("\n\nAttempts exhausted; a human should look at this.")
	} else {
		sb.WriteString("\n\nWill retry on the next poll.")
	}
	return sb.String()
}

func reportPRFailed(err error, branch string) string {
	return fmt.Sprintf("%s Pushed branch `%s` but opening the PR failed: %v\n\nWill retry on the next poll.", Marker, branch, err)
}
