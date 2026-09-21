package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigMissingFileDefaultsFromEnv(t *testing.T) {
	c, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"), env(map[string]string{"ANTHROPIC_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider != "anthropic" || c.Model != "claude-sonnet-5" || c.APIKeyEnv != "ANTHROPIC_API_KEY" || !c.FromEnv {
		t.Errorf("config = %+v", c)
	}
	if c.MaxTokens != 4096 || c.StepBudget != 20 {
		t.Errorf("budgets = %+v", c)
	}
}

func TestProviderFromEnvOrder(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "o", "HF_TOKEN": "h"}, "anthropic"},
		{map[string]string{"OPENAI_API_KEY": "o", "HF_TOKEN": "h"}, "openai"},
		{map[string]string{"HF_TOKEN": "h"}, "huggingface"},
		{map[string]string{}, "ollama"},
	}
	for _, tc := range cases {
		c, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"), env(tc.env))
		if err != nil {
			t.Fatal(err)
		}
		if c.Provider != tc.want {
			t.Errorf("env %v: provider = %s, want %s", tc.env, c.Provider, tc.want)
		}
	}
}

func TestLoadConfigFileWinsAndPresetsFill(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("provider: huggingface\nmodel: meta-llama/Llama-3.3-70B-Instruct\nstep_budget: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p, env(map[string]string{"ANTHROPIC_API_KEY": "a"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider != "huggingface" || c.FromEnv || c.BaseURL != "https://router.huggingface.co/v1" || c.APIKeyEnv != "HF_TOKEN" || c.StepBudget != 5 || c.Model != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("provider: bard\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p, env(nil)); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Errorf("err = %v", err)
	}
	if err := os.WriteFile(p, []byte("step_budget: -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p, env(nil)); err == nil || !strings.Contains(err.Error(), "step_budget") {
		t.Errorf("err = %v", err)
	}
}

func TestOverride(t *testing.T) {
	c, _ := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"), env(nil))
	c.Override("openai", "")
	if c.Provider != "openai" || c.Model != "gpt-5-mini" || c.BaseURL != "https://api.openai.com/v1" || c.FromEnv {
		t.Errorf("config = %+v", c)
	}
	c.Override("", "gpt-5")
	if c.Provider != "openai" || c.Model != "gpt-5" {
		t.Errorf("config = %+v", c)
	}
}

func TestNewModel(t *testing.T) {
	c, _ := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"), env(map[string]string{"ANTHROPIC_API_KEY": "k"}))
	m, err := NewModel(c, env(map[string]string{"ANTHROPIC_API_KEY": "k"}))
	if err != nil || m.Name() != "anthropic/claude-sonnet-5" {
		t.Fatalf("model = %v, err = %v", m, err)
	}
	if _, err := NewModel(c, env(nil)); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY is not set") {
		t.Errorf("err = %v", err)
	}
	c.Override("ollama", "")
	if m, err := NewModel(c, env(nil)); err != nil || m.Name() != "ollama/qwen3" {
		t.Errorf("ollama: %v %v", m, err)
	}
	c.Override("fake", "")
	if m, err := NewModel(c, env(nil)); err != nil || m.Name() != "fake" {
		t.Errorf("fake: %v %v", m, err)
	}
}

func TestPaths(t *testing.T) {
	if got := ConfigPath("/home/u/.config/hivedispatch/config.yaml"); got != "/home/u/.config/hivedispatch/supervisor/config.yaml" {
		t.Errorf("ConfigPath = %s", got)
	}
}
