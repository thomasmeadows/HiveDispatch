package prompt

import (
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

var base = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func ticket() tracker.Ticket {
	return tracker.Ticket{
		Key: "HIVE-7", Summary: "Add --version", Description: "Print the version.",
		URL: "https://x/browse/HIVE-7",
		Comments: []tracker.Comment{
			{Author: "Thomas", Body: "Also bump the changelog.", Created: base.Add(-2 * time.Hour)},
			{Author: "Thomas", Body: "[HiveDispatch] Which format?", Created: base.Add(-time.Hour)},
			{Author: "Thomas", Body: "semver please", Created: base.Add(-30 * time.Minute)},
		},
	}
}

func TestRenderIncludesEverything(t *testing.T) {
	got := Render(ticket(), config.RepoConfig{Name: "o/r", DefaultBranch: "main"}, "hive/HIVE-7")
	for _, want := range []string{"HIVE-7", "Add --version", "Print the version.", "Also bump the changelog.", "hive/HIVE-7", "o/r", "HIVE_NEEDS_INPUT:"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestRenderResumeOnlyNewHumanComments(t *testing.T) {
	isOurs := func(c tracker.Comment) bool { return strings.HasPrefix(c.Body, "[HiveDispatch]") }
	got := RenderResume(ticket(), base.Add(-time.Hour), isOurs)
	if !strings.Contains(got, "semver please") {
		t.Errorf("missing reply:\n%s", got)
	}
	if strings.Contains(got, "Also bump") || strings.Contains(got, "Which format?") {
		t.Errorf("included old or own comments:\n%s", got)
	}
}
