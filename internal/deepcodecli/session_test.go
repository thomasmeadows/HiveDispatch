package deepcodecli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

const fixtureID = "d2aca809-b451-42a3-8db5-2adbfa384eef"

// fixtureHome lays the recorded session out the way deepcode stores it:
// <home>/projects/<code>/<id>.jsonl beside sessions-index.json.
func fixtureHome(t *testing.T) (home, sessionFile string) {
	t.Helper()
	home = t.TempDir()
	dir := filepath.Join(home, "projects", "work-HIVE-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for src, dst := range map[string]string{"session_success.jsonl": fixtureID + ".jsonl", "index_success.json": "sessions-index.json"} {
		raw, err := os.ReadFile(filepath.Join("deepcodetest", src))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, dst), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, filepath.Join(dir, fixtureID+".jsonl")
}

func TestParseSession(t *testing.T) {
	_, file := fixtureHome(t)
	tr, err := ParseSession(file, "/work/HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.SessionID != fixtureID || tr.Status != "completed" || tr.FailReason != "" {
		t.Fatalf("session %+v", tr)
	}
	if tr.Reply == "" || tr.ToolCalls != 4 {
		t.Fatalf("reply %q tool calls %d", tr.Reply, tr.ToolCalls)
	}
	names := []string{}
	for _, s := range tr.Steps {
		if s.Kind == executor.StepTool {
			names = append(names, s.Name)
		}
	}
	if len(names) != 4 || names[0] != "bash" || names[1] != "read" || names[2] != "edit" || names[3] != "bash" {
		t.Fatalf("tool steps %v", names)
	}
	edit := tr.Steps[2]
	if edit.IsError || edit.Start.IsZero() || edit.End.Before(edit.Start) || edit.Output == "" {
		t.Fatalf("edit step %+v", edit)
	}
	if want := time.Date(2026, 10, 1, 7, 31, 12, 912e6, time.UTC); !edit.Start.Equal(want) {
		t.Fatalf("edit start %v, want %v", edit.Start, want)
	}
	if len(tr.EditedFiles) != 1 || tr.EditedFiles[0] != "main.go" {
		t.Fatalf("edited %v", tr.EditedFiles)
	}
	if tr.Usage.Model != "deepseek-flash" || tr.Usage.InputTokens != 32023 || tr.Usage.OutputTokens != 539 {
		t.Fatalf("usage %+v", tr.Usage)
	}
	if got := RootPath(file); got != "/work/HIVE-1" {
		t.Fatalf("root path %q", got)
	}
	if n := CountToolCalls(file); n != 4 {
		t.Fatalf("CountToolCalls %d", n)
	}
}

func TestParseSessionFailedTool(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "s.jsonl")
	lines := `{"sessionId":"s","role":"assistant","content":"","messageParams":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"command\": \"false\"}"}}]},"createTime":"2026-10-01T07:00:00.000Z"}
{"sessionId":"s","role":"tool","content":"{\"ok\": true, \"name\": \"bash\", \"output\": \"\", \"metadata\": {\"exitCode\": 1}}","messageParams":{"tool_call_id":"c1"},"createTime":"2026-10-01T07:00:01.000Z"}
{"sessionId":"s","role":"assistant","content":"","messageParams":{"tool_calls":[{"id":"c2","type":"function","function":{"name":"write","arguments":"{\"file_path\": \"/elsewhere/x.go\"}"}}]},"createTime":"2026-10-01T07:00:02.000Z"}
{"sessionId":"s","role":"tool","content":"{\"ok\": false, \"name\": \"write\", \"error\": \"denied\"}","messageParams":{"tool_call_id":"c2"},"createTime":"2026-10-01T07:00:03.000Z"}
`
	if err := os.WriteFile(file, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions-index.json"), []byte(`{"entries":[{"id":"s","status":"failed","failReason":"429 Too Many Requests"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tr, err := ParseSession(file, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Steps) != 2 || !tr.Steps[0].IsError || !tr.Steps[1].IsError || tr.Steps[1].Output != "denied" {
		t.Fatalf("steps %+v", tr.Steps)
	}
	if len(tr.EditedFiles) != 0 {
		t.Fatalf("a failed write is not an edit: %v", tr.EditedFiles)
	}
	if tr.Status != "failed" || tr.FailReason != "429 Too Many Requests" || !LooksLikeBudget(tr.FailReason) {
		t.Fatalf("status %q reason %q", tr.Status, tr.FailReason)
	}
}

func TestFindSession(t *testing.T) {
	home, file := fixtureHome(t)
	if got := FindSession(home, "/work/HIVE-1", fixtureID, nil); got != file {
		t.Fatalf("resume: %q", got)
	}
	if got := FindSession(home, "/work/HIVE-1", "", map[string]bool{}); got != file {
		t.Fatalf("new: %q", got)
	}
	if got := FindSession(home, "/work/HIVE-2", "", map[string]bool{}); got != "" {
		t.Fatalf("another worktree's session was picked: %q", got)
	}
	if got := FindSession(home, "/work/HIVE-1", "", map[string]bool{file: true}); got != "" {
		t.Fatalf("a session that existed before the run was picked: %q", got)
	}
}
