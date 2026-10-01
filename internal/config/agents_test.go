package config

import (
	"strings"
	"testing"
)

func TestParseAgentsLangGraph(t *testing.T) {
	got, err := ParseAgents([]byte("agents:\n  - name: g\n    executor: langgraph\n  - name: h\n    executor: langgraph\n    code_with: codex\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].CodeWith != "claude" || got[1].CodeWith != "codex" {
		t.Fatalf("code_with = %q, %q", got[0].CodeWith, got[1].CodeWith)
	}
	for _, bad := range []string{
		"agents:\n  - name: a\n    executor: claude\n    code_with: codex\n",
		"agents:\n  - name: a\n    executor: langgraph\n    code_with: gemini\n",
	} {
		if _, err := ParseAgents([]byte(bad)); err == nil || !strings.Contains(err.Error(), "code_with") {
			t.Errorf("%q: err = %v, want a code_with problem", bad, err)
		}
	}
}
