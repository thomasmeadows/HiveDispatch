package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

var at = time.Date(2026, 9, 20, 14, 30, 0, 0, time.UTC)

func TestNotesAppendAndRead(t *testing.T) {
	m := NewMemory(filepath.Join(t.TempDir(), "supervisor"))
	if n, err := m.Notes(); err != nil || n != "" {
		t.Fatalf("empty: %q %v", n, err)
	}
	if err := m.Append(at, "tracker is github"); err != nil {
		t.Fatal(err)
	}
	if err := m.Append(at, "token comes from gh"); err != nil {
		t.Fatal(err)
	}
	n, _ := m.Notes()
	if n != "- 2026-09-20: tracker is github\n- 2026-09-20: token comes from gh\n" {
		t.Errorf("notes = %q", n)
	}
	text, lines := m.NotesForPrompt()
	if lines != 2 || text != n {
		t.Errorf("prompt notes = %q (%d)", text, lines)
	}
}

func TestNotesForPromptCapKeepsNewest(t *testing.T) {
	m := NewMemory(filepath.Join(t.TempDir(), "supervisor"))
	for i := 0; i < 2000; i++ {
		if err := m.Append(at, strings.Repeat("x", 20)+" "+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	text, lines := m.NotesForPrompt()
	if lines != 2000 || len(text) > 16<<10 || !strings.HasSuffix(text, string(rune('a'+1999%26))+"\n") {
		t.Errorf("len=%d lines=%d tail=%q", len(text), lines, text[len(text)-30:])
	}
	if !strings.HasPrefix(text, "- ") {
		t.Errorf("cap must cut on a line boundary: %q", text[:20])
	}
	raw, _ := os.ReadFile(m.NotesPath())
	if len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 2000 {
		t.Error("file was truncated")
	}
}

func TestSessions(t *testing.T) {
	m := NewMemory(filepath.Join(t.TempDir(), "supervisor"))
	if name, err := m.NewestSession(); err != nil || name != "" {
		t.Fatalf("no sessions: %q %v", name, err)
	}
	h := []model.Message{{Role: model.RoleUser, Content: "hi"}, {Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: "read_doc", Args: []byte(`{"name":"setup"}`)}}}, {Role: model.RoleTool, ToolCallID: "c1", Content: "# Setup", IsError: true}}
	first := m.NewSessionName(at)
	if first != "20260920T143000Z.json" {
		t.Errorf("name = %s", first)
	}
	if err := m.SaveSession(first, h); err != nil {
		t.Fatal(err)
	}
	second := m.NewSessionName(at.Add(time.Minute))
	if err := m.SaveSession(second, h[:1]); err != nil {
		t.Fatal(err)
	}
	if name, _ := m.NewestSession(); name != second {
		t.Errorf("newest = %s", name)
	}
	got, err := m.LoadSession(first)
	if err != nil || len(got) != 3 || got[1].ToolCalls[0].Name != "read_doc" || string(got[1].ToolCalls[0].Args) != `{"name":"setup"}` || !got[2].IsError {
		t.Errorf("loaded = %+v, err = %v", got, err)
	}
	if _, err := m.LoadSession("nope.json"); err == nil {
		t.Error("missing session loaded")
	}
}

func TestRememberTool(t *testing.T) {
	m := NewMemory(filepath.Join(t.TempDir(), "supervisor"))
	out, err := call(t, NewRemember(m, func() time.Time { return at }), `{"note":"jira site is x.atlassian.net"}`)
	if err != nil || !strings.Contains(out, "remembered") {
		t.Fatalf("out = %q err = %v", out, err)
	}
	if n, _ := m.Notes(); n != "- 2026-09-20: jira site is x.atlassian.net\n" {
		t.Errorf("notes = %q", n)
	}
	if _, err := call(t, NewRemember(m, func() time.Time { return at }), `{"note":"  "}`); err == nil {
		t.Error("empty note accepted")
	}
}
