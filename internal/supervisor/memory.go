package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

const notesPromptCap = 16 << 10

// Memory is what the supervisor keeps between sessions: a notes file it
// appends to, and one transcript per session.
type Memory struct {
	Dir string
}

// NewMemory returns a Memory rooted at dir (created on first write).
func NewMemory(dir string) *Memory { return &Memory{Dir: dir} }

// NotesPath is the notes file.
func (m *Memory) NotesPath() string { return filepath.Join(m.Dir, "memory.md") }

func (m *Memory) sessionsDir() string { return filepath.Join(m.Dir, "sessions") }

// Notes returns the whole notes file, or "" when there is none.
func (m *Memory) Notes() (string, error) {
	raw, err := os.ReadFile(m.NotesPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(raw), err
}

// NotesForPrompt returns the notes capped for the system prompt, keeping
// the newest lines, and the total number of lines in the file.
func (m *Memory) NotesForPrompt() (string, int) {
	n, err := m.Notes()
	if err != nil || n == "" {
		return "", 0
	}
	lines := strings.Split(strings.TrimSuffix(n, "\n"), "\n")
	total := len(lines)
	for len(n) > notesPromptCap && len(lines) > 1 {
		lines = lines[1:]
		n = strings.Join(lines, "\n") + "\n"
	}
	return n, total
}

// Append adds one dated note.
func (m *Memory) Append(now time.Time, note string) error {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(m.NotesPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("- %s: %s\n", now.UTC().Format("2006-01-02"), strings.TrimSpace(note))
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// NewSessionName names a session file by its start time.
func (m *Memory) NewSessionName(now time.Time) string {
	return now.UTC().Format("20060102T150405Z") + ".json"
}

// SaveSession writes the transcript atomically.
func (m *Memory) SaveSession(name string, h []model.Message) error {
	if err := os.MkdirAll(m.sessionsDir(), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	p := filepath.Join(m.sessionsDir(), name)
	if err := os.WriteFile(p+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// LoadSession reads a transcript by name, or by path when name contains a
// separator.
func (m *Memory) LoadSession(name string) ([]model.Message, error) {
	p := name
	if !strings.ContainsRune(name, filepath.Separator) {
		p = filepath.Join(m.sessionsDir(), name)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	var h []model.Message
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", p, err)
	}
	return h, nil
}

// NewestSession returns the most recent session file name, or "".
func (m *Memory) NewestSession() (string, error) {
	entries, err := os.ReadDir(m.sessionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)
	return names[len(names)-1], nil
}
