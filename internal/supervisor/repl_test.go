package supervisor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

func opts(t *testing.T, stdin string, interactive bool) (Options, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	var out, errb bytes.Buffer
	return Options{
		WorkerConfigPath: filepath.Join(dir, "config.yaml"),
		Provider:         "fake",
		Exe:              fakeExe(t),
		Stdin:            strings.NewReader(stdin),
		Stdout:           &out, Stderr: &errb,
		Interactive: interactive,
		Getenv:      func(string) string { return "" },
		Now:         func() time.Time { return at },
	}, &out, &errb
}

func historyOf(user string) []model.Message {
	return []model.Message{{Role: model.RoleUser, Content: user}}
}

func TestWatchInterrupt(t *testing.T) {
	t.Run("signal seen", func(t *testing.T) {
		sig := make(chan os.Signal, 1)
		canceled := make(chan struct{})
		stop := watchInterrupt(sig, func() { close(canceled) })
		sig <- os.Interrupt
		select {
		case <-canceled:
		case <-time.After(2 * time.Second):
			t.Fatal("cancel was not called")
		}
		if !stop() {
			t.Error("stop() = false, want true after a signal")
		}
	})

	t.Run("no signal", func(t *testing.T) {
		sig := make(chan os.Signal, 1)
		stop := watchInterrupt(sig, func() { t.Error("cancel must not be called") })
		if stop() {
			t.Error("stop() = true, want false with no signal")
		}
	})
}

func TestOneShot(t *testing.T) {
	o, out, _ := opts(t, "why does check fail?\n", false)
	r, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(fake supervisor") {
		t.Errorf("stdout = %q", out.String())
	}
	if r.Confirm("x?") {
		t.Error("non-interactive confirm must be false")
	}
	name, _ := NewMemory(Dir(o.WorkerConfigPath)).NewestSession()
	if name == "" {
		t.Error("session not saved")
	}
}

func TestInteractiveCommandsAndQuit(t *testing.T) {
	o, out, _ := opts(t, "/model\n/notes\nhello\n/quit\nnever sent\n", true)
	r, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "fake") || !strings.Contains(s, "(no notes yet)") || !strings.Contains(s, "(fake supervisor") {
		t.Errorf("stdout = %q", s)
	}
	if strings.Count(s, "(fake supervisor") != 1 {
		t.Errorf("/quit did not stop the loop: %q", s)
	}
	if !strings.Contains(r.Banner(), "config "+o.WorkerConfigPath+" (missing)") {
		t.Errorf("banner = %q", r.Banner())
	}
}

func TestResumeLoadsNewest(t *testing.T) {
	o, _, _ := opts(t, "", false)
	mem := NewMemory(Dir(o.WorkerConfigPath))
	if err := mem.SaveSession(mem.NewSessionName(at), historyOf("earlier")); err != nil {
		t.Fatal(err)
	}
	o.Resume = true
	r, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if h := r.agent.History(); len(h) != 1 || h[0].Content != "earlier" {
		t.Errorf("history = %+v", h)
	}
	o.Resume = false
	o.Session = "missing.json"
	if _, err := New(context.Background(), o); err == nil {
		t.Error("missing session accepted")
	}
}

func TestSessionPathRoundTrips(t *testing.T) {
	o, _, _ := opts(t, "hello\n", false)
	p := filepath.Join(t.TempDir(), "abs.json")
	mem := NewMemory(Dir(o.WorkerConfigPath))
	if err := mem.SaveSession(p, historyOf("earlier")); err != nil {
		t.Fatal(err)
	}
	o.Session = p
	r, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(p); statErr != nil {
		t.Errorf("session file missing at %s: %v", p, statErr)
	}
	if _, statErr := os.Stat(filepath.Join(mem.sessionsDir(), filepath.Base(p))); statErr == nil {
		t.Error("session was also written under sessions/ instead of at the given path")
	}
}

func TestConfirmReadsLine(t *testing.T) {
	o, _, errb := opts(t, "y\nn\n", true)
	r, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Confirm("Apply?") || r.Confirm("Apply?") {
		t.Error("confirm should read y then n")
	}
	if !strings.Contains(errb.String(), "Apply? [y/N] ") {
		t.Errorf("stderr = %q", errb.String())
	}
	if _, statErr := os.Stat(o.WorkerConfigPath); statErr == nil {
		t.Error("New must not create the worker config")
	}
}
