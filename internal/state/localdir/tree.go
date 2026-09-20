package localdir

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// ListRuns reads every runs/<KEY>.json under dir.
func ListRuns(dir string) ([]state.Run, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []state.Run
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "runs", e.Name()))
		if err != nil {
			return nil, err
		}
		var r state.Run
		if json.Unmarshal(raw, &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// PruneTree removes raw logs older than before (by modification time) and
// finished runs not updated since before. It returns the number of files
// removed and their paths relative to dir. events.jsonl is never touched.
func PruneTree(dir string, before time.Time) (int, []string, error) {
	var removed []string
	logs := filepath.Join(dir, "logs")
	_ = filepath.WalkDir(logs, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".log") {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().Before(before) {
			return nil
		}
		if os.Remove(p) == nil {
			rel, _ := filepath.Rel(dir, p)
			removed = append(removed, rel)
		}
		return nil
	})
	runs, err := ListRuns(dir)
	if err != nil {
		return len(removed), removed, err
	}
	for _, r := range runs {
		if r.Phase == state.PhaseDone && r.UpdatedAt.Before(before) {
			p := filepath.Join(dir, "runs", r.Ticket+".json")
			if os.Remove(p) == nil {
				removed = append(removed, filepath.Join("runs", r.Ticket+".json"))
			}
		}
	}
	return len(removed), removed, nil
}
