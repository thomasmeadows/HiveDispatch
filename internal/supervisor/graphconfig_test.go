package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkerConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadGraphConfig(t *testing.T) {
	env := func(k string) string { return map[string]string{"DEEPSEEK_API_KEY": "k"}[k] }

	g, err := LoadGraphConfig(writeWorkerConfig(t, "graph:\n  provider: openai\n  model: gpt-x\n"), env)
	if err != nil || g.Binary != "hivegraph" || g.Model.Provider != "openai" || g.Model.Model != "gpt-x" || g.Model.APIKeyEnv != "OPENAI_API_KEY" || g.Model.BaseURL == "" {
		t.Fatalf("explicit: %+v %v", g, err)
	}

	g, err = LoadGraphConfig(writeWorkerConfig(t, "supervisor:\n  provider: deepseek\ngraph:\n  binary: /opt/hivegraph\n"), env)
	if err != nil || g.Binary != "/opt/hivegraph" || g.Model.Provider != "deepseek" || g.Model.Model != "deepseek-flash" {
		t.Fatalf("inherit supervisor: %+v %v", g, err)
	}

	if _, err := LoadGraphConfig(writeWorkerConfig(t, "graph:\n  provider: anthropic\n"), env); err == nil || !strings.Contains(err.Error(), "OpenAI-compatible") {
		t.Fatalf("anthropic: %v", err)
	}
	if _, err := LoadGraphConfig(writeWorkerConfig(t, "supervisor:\n  provider: anthropic\n"), env); err == nil || !strings.Contains(err.Error(), "graph.provider") {
		t.Fatalf("anthropic inherited: %v", err)
	}
}
