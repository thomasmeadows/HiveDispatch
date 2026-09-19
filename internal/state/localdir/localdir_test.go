package localdir

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

func TestLoadMissingReturnsEmptyRun(t *testing.T) {
	s := New(t.TempDir())
	run, err := s.Load(context.Background(), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Ticket != "HIVE-1" || run.Attempts != 0 || run.Phase != "" {
		t.Errorf("run = %+v", run)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()
	in := &state.Run{Ticket: "HIVE-1", Agent: "w", Branch: "hive/HIVE-1", Attempts: 2, Phase: state.PhasePushed, ResumeToken: "tok", QuestionAt: time.Date(2026, 9, 19, 1, 2, 3, 0, time.UTC)}
	if err := s.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	if in.UpdatedAt.IsZero() {
		t.Error("Save must set UpdatedAt")
	}
	out, err := s.Load(ctx, "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Attempts != 2 || out.Phase != state.PhasePushed || out.ResumeToken != "tok" || !out.QuestionAt.Equal(in.QuestionAt) {
		t.Errorf("out = %+v", out)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "runs", "HIVE-1.json")); err != nil {
		t.Error("run file not at runs/HIVE-1.json")
	}
}

func TestAppendLogAndWriteLog(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.AppendLog(ctx, "HIVE-1", state.LogEntry{Agent: "w", Event: "claimed"}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "logs", "HIVE-1", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; lines != 2 {
		t.Errorf("lines = %d", lines)
	}
	p, err := s.WriteLog(ctx, "HIVE-1", "run-1", "executor output")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "executor output" {
		t.Errorf("log = %q", got)
	}
	if !strings.HasSuffix(p, filepath.Join("logs", "HIVE-1", "run-1.log")) {
		t.Errorf("path = %s", p)
	}
}
