package supervisor

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// GraphConfig is the graph: block of the worker config: the hivegraph
// binary and the chat model its plan and review nodes use.
type GraphConfig struct {
	Binary string
	Model  Config
}

// LoadGraphConfig reads graph: from the worker config. Without a
// graph.provider the supervisor's model is used. The graph talks to its
// model through LangChain's OpenAI client, so Anthropic is refused.
func LoadGraphConfig(workerConfigPath string, getenv func(string) string) (GraphConfig, error) {
	var f struct {
		Graph struct {
			Binary string `yaml:"binary"`
			Config `yaml:",inline"`
		} `yaml:"graph"`
	}
	raw, err := os.ReadFile(workerConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return GraphConfig{}, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return GraphConfig{}, fmt.Errorf("parse config %s: %w", workerConfigPath, err)
	}
	g := GraphConfig{Binary: f.Graph.Binary, Model: f.Graph.Config}
	if g.Binary == "" {
		g.Binary = "hivegraph"
	}
	if g.Model.Provider == "" {
		sup, err := LoadConfig(workerConfigPath, getenv)
		if err != nil {
			return g, err
		}
		if sup.Provider == "anthropic" {
			return g, fmt.Errorf("%s: the supervisor uses anthropic, which the graph cannot: set graph.provider to an OpenAI-compatible provider (openai, deepseek, huggingface or ollama)", workerConfigPath)
		}
		g.Model = sup
		return g, nil
	}
	if g.Model.Provider == "anthropic" {
		return g, fmt.Errorf("%s: graph.provider anthropic: the graph needs an OpenAI-compatible provider (openai, deepseek, huggingface or ollama)", workerConfigPath)
	}
	if err := g.Model.validate(); err != nil {
		return g, fmt.Errorf("%s: graph.%w", workerConfigPath, err)
	}
	g.Model.applyPreset()
	return g, nil
}
