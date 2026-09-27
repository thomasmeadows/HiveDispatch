package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/fake"
)

func TestSessionUsesInjectedModelAndConfirm(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	var asked []string
	var events []string
	m := fake.New(fake.Call("c1", "write_config", `{"content":"agent_id: w\n"}`), fake.Text("declined, as you wish"))
	s, err := NewSession(context.Background(), SessionOptions{
		WorkerConfigPath: cfgPath, Exe: fakeExe(t), ChatModel: m,
		Now:     func() time.Time { return at },
		Confirm: func(p string) bool { asked = append(asked, p); return false },
		Events:  func(e Event) { events = append(events, e.Kind) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ModelName() != "fake" {
		t.Errorf("model = %q", s.ModelName())
	}
	s.RefreshCheck(context.Background())
	reply, err := s.Turn(context.Background(), "write it")
	if err != nil || reply != "declined, as you wish" {
		t.Fatalf("Turn = %q, %v", reply, err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "+agent_id: w") {
		t.Errorf("confirm prompts = %q", asked)
	}
	if _, err := os.Stat(cfgPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("a declined write_config wrote the file")
	}
	if !strings.Contains(strings.Join(events, ","), "tool_start") {
		t.Errorf("events = %v", events)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	first := s.Name()
	s.Reset()
	if len(s.History()) != 0 || s.Name() == "" {
		t.Errorf("after Reset: history %d, name %q (was %q)", len(s.History()), s.Name(), first)
	}
}
