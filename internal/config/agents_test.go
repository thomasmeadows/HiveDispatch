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

func TestParseAgentsCodeWithLangGraph(t *testing.T) {
	got, err := ParseAgents([]byte("agents:\n  - name: d\n    executor: langgraph\n    code_with: langgraph\n"))
	if err != nil || got[0].CodeWith != "langgraph" {
		t.Fatalf("got %+v err %v", got, err)
	}
	_, err = ParseAgents([]byte("agents:\n  - name: p\n    role: planning\n    executor: langgraph\n    code_with: langgraph\n"))
	if err == nil || !strings.Contains(err.Error(), "coding") {
		t.Fatalf("planning with code_with langgraph: err %v", err)
	}
}

func TestParseAgentsDeepCode(t *testing.T) {
	got, err := ParseAgents([]byte("agents:\n  - name: dc\n    executor: deepcode\n  - name: g\n    executor: langgraph\n    code_with: deepcode\n"))
	if err != nil || got[0].Executor != "deepcode" || got[1].CodeWith != "deepcode" {
		t.Fatalf("got %+v err %v", got, err)
	}
	for bad, want := range map[string]string{
		"agents:\n  - name: r\n    role: review\n    executor: deepcode\n":                             "coding",
		"agents:\n  - name: m\n    executor: deepcode\n    model: deepseek-pro\n":                      "settings.json",
		"agents:\n  - name: p\n    role: planning\n    executor: langgraph\n    code_with: deepcode\n": "coding",
		"agents:\n  - name: m\n    executor: langgraph\n    code_with: deepcode\n    model: x\n":       "settings.json",
	} {
		if _, err := ParseAgents([]byte(bad)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", bad, err, want)
		}
	}
}

func TestGrokAgents(t *testing.T) {
	for _, fields := range []string{"executor: grok", "executor: langgraph\n    code_with: grok"} {
		if _, err := ParseAgents([]byte("agents:\n  - name: g\n    " + fields + "\n    model: grok-test\n")); err != nil {
			t.Fatal(err)
		}
		for _, role := range []string{"planning", "review"} {
			if _, err := ParseAgents([]byte("agents:\n  - name: g\n    " + fields + "\n    role: " + role + "\n")); err == nil {
				t.Fatalf("accepted %s: %s", role, fields)
			}
		}
	}
}

func TestAntigravityAgents(t *testing.T) {
	for _, fields := range []string{"executor: antigravity", "executor: langgraph\n    code_with: antigravity"} {
		if _, err := ParseAgents([]byte("agents:\n  - name: g\n    " + fields + "\n    model: antigravity-test\n")); err != nil {
			t.Fatal(err)
		}
		for _, role := range []string{"planning", "review"} {
			if _, err := ParseAgents([]byte("agents:\n  - name: g\n    " + fields + "\n    role: " + role + "\n")); err == nil {
				t.Fatalf("accepted %s: %s", role, fields)
			}
		}
	}
}
