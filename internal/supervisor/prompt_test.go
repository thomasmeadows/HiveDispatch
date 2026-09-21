package supervisor

import (
	"context"
	"strings"
	"testing"
)

func TestBuildSystem(t *testing.T) {
	s := BuildSystem(PromptInput{ConfigPath: "/c/config.yaml", ConfigExists: false, ModelName: "fake", Notes: "- 2026-09-20: uses github\n", NoteLines: 1, CheckOutput: "read config: open /c/config.yaml: no such file"})
	for _, want := range []string{
		"HiveDispatch supervisor",
		"ask before changing anything",
		"read_doc",
		"run -once -executor fake -placeholder",
		"# Notes from earlier sessions",
		"- 2026-09-20: uses github",
		"/c/config.yaml (missing)",
		"# Current `hivedispatch check` output",
		"no such file",
		"never in the config",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if len(s) > 12<<10 {
		t.Errorf("base prompt is %d bytes; keep it small, docs are fetched on demand", len(s))
	}
	if s2 := BuildSystem(PromptInput{ConfigPath: "/c/config.yaml", ConfigExists: true, ModelName: "fake"}); !strings.Contains(s2, "/c/config.yaml (exists)") || strings.Contains(s2, "# Notes from earlier sessions") {
		t.Errorf("exists/no-notes variant: %q", s2)
	}
}

func TestCheckOutput(t *testing.T) {
	out := CheckOutput(context.Background(), fakeExe(t), "/c/config.yaml")
	if !strings.Contains(out, "args: check -config /c/config.yaml") {
		t.Errorf("out = %q", out)
	}
	if out := CheckOutput(context.Background(), "/nonexistent/hivedispatch", "/c"); !strings.Contains(out, "check could not run") {
		t.Errorf("out = %q", out)
	}
}
