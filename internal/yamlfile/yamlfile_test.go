package yamlfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageRejectsBadYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if _, err := Stage(p, []byte("a: [b\n"), nil); err == nil {
		t.Fatal("want error for unparseable YAML")
	}
	if _, err := Stage(p, []byte("  \n"), nil); err == nil {
		t.Fatal("want error for empty content")
	}
}

func TestStageDiffAndProblems(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen string
	s, err := Stage(p, []byte("a: 2\n"), func(tmp string) error {
		raw, err := os.ReadFile(tmp)
		if err != nil {
			return err
		}
		seen = string(raw)
		return errors.New(tmp + ": a is wrong")
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != "a: 2\n" {
		t.Errorf("validator saw %q", seen)
	}
	if !strings.Contains(s.Diff, "-a: 1") || !strings.Contains(s.Diff, "+a: 2") {
		t.Errorf("diff = %q", s.Diff)
	}
	if s.Problems != p+": a is wrong" {
		t.Errorf("problems = %q, want the temp path replaced by the real one", s.Problems)
	}
	if raw, _ := os.ReadFile(p); string(raw) != "a: 1\n" {
		t.Errorf("Stage changed the file: %q", raw)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("Stage left files behind: %v", entries)
	}
}

func TestCommitWritesBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "c.yaml")
	s, err := Stage(p, []byte("a: 1\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Error("a new file must not get a .bak")
	}
	s, err = Stage(p, []byte("a: 2\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(p); string(raw) != "a: 2\n" {
		t.Errorf("file = %q", raw)
	}
	if raw, _ := os.ReadFile(p + ".bak"); string(raw) != "a: 1\n" {
		t.Errorf(".bak = %q", raw)
	}
}

func TestPatchKeepsComments(t *testing.T) {
	in := "# worker config\nagent_id: old # who I am\n\n# where code lives\ncode_dirs:\n  - ~/code\nclaude:\n  model: x\n"
	out, err := Patch([]byte(in), map[string]any{
		"agent_id":       "new",
		"code_dirs":      []any{"~/a", "~/b"},
		"claude.model":   "sonnet",
		"max_concurrent": float64(3),
		"triage.mode":    "passthrough",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# worker config", "agent_id: new # who I am", "# where code lives", "- ~/a", "- ~/b", "model: sonnet", "max_concurrent: 3", "triage:\n  mode: passthrough"} {
		if !strings.Contains(s, want) {
			t.Errorf("patched YAML lacks %q:\n%s", want, s)
		}
	}
	if strings.Index(s, "agent_id") > strings.Index(s, "code_dirs") {
		t.Errorf("key order changed:\n%s", s)
	}
}

func TestPatchDeletesAndStartsEmpty(t *testing.T) {
	out, err := Patch(nil, map[string]any{"a.b": "c"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a:\n  b: c\n" {
		t.Errorf("from empty = %q", out)
	}
	out, err = Patch([]byte("a: 1\nb: 2\n"), map[string]any{"a": nil, "zz.y": nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "b: 2\n" {
		t.Errorf("after delete = %q", out)
	}
}

func TestPatchRefusesNonMapping(t *testing.T) {
	if _, err := Patch([]byte("- a\n"), map[string]any{"a": 1}); err == nil {
		t.Error("want error when the document is not a mapping")
	}
	if _, err := Patch([]byte("a: 1\n"), map[string]any{"a.b": 1}); err == nil {
		t.Error("want error when a path runs through a scalar")
	}
}
