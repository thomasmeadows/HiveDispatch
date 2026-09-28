// Package supervisor is HiveDispatch's own agent: a loop over a chat model
// with tools that read the docs, edit the worker config and run the
// allowlisted hivedispatch subcommands, plus a notes file it remembers
// between sessions. It helps an operator get HiveDispatch running.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/anthropic"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/fake"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/openai"
)

// Config is the supervisor's own settings: the supervisor: block of the
// worker config.
type Config struct {
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"`
	BaseURL    string `yaml:"base_url"`
	APIKeyEnv  string `yaml:"api_key_env"`
	MaxTokens  int    `yaml:"max_tokens"`
	StepBudget int    `yaml:"step_budget"`
	FromEnv    bool   `yaml:"-"` // provider was chosen from the environment, not the file or a flag
}

// Defaults for the per-turn limits when the config leaves them unset.
const (
	defaultMaxTokens  = 4096
	defaultStepBudget = 20
)

type preset struct {
	model, baseURL, keyEnv string
}

var presets = map[string]preset{
	"anthropic":   {"claude-sonnet-5", "https://api.anthropic.com", "ANTHROPIC_API_KEY"},
	"openai":      {"gpt-5-mini", "https://api.openai.com/v1", "OPENAI_API_KEY"},
	"deepseek":    {"deepseek-flash", "https://api.deepseek.com/v1", "DEEPSEEK_API_KEY"},
	"huggingface": {"Qwen/Qwen3-32B", "https://router.huggingface.co/v1", "HF_TOKEN"},
	"ollama":      {"qwen3", "http://localhost:11434/v1", ""},
	"fake":        {"fake", "", ""},
}

// Providers lists the configurable providers, for messages.
const Providers = "anthropic, openai, deepseek, huggingface or ollama"

// Dir is the supervisor's directory beside the worker config: its notes
// (memory.md) and session transcripts.
func Dir(workerConfigPath string) string {
	return filepath.Join(filepath.Dir(workerConfigPath), "supervisor")
}

// LoadConfig reads the supervisor: block of the worker config at
// workerConfigPath, or returns defaults when the file or the block is
// missing. The separate supervisor/config.yaml of earlier versions is an
// error rather than silently ignored.
func LoadConfig(workerConfigPath string, getenv func(string) string) (Config, error) {
	var c Config
	old := filepath.Join(Dir(workerConfigPath), "config.yaml")
	if _, err := os.Stat(old); err == nil {
		return c, fmt.Errorf("%s is no longer read: move its keys under supervisor: in %s, then delete it", old, workerConfigPath)
	}
	raw, err := os.ReadFile(workerConfigPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, fmt.Errorf("read config: %w", err)
	default:
		var f struct {
			Supervisor Config `yaml:"supervisor"`
		}
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return c, fmt.Errorf("parse config %s: %w", workerConfigPath, err)
		}
		c = f.Supervisor
	}
	if c.Provider == "fake" {
		return c, fmt.Errorf("%s: supervisor.provider fake is for tests; pass -provider fake on the command line instead", workerConfigPath)
	}
	if c.Provider == "" {
		c.Provider = providerFromEnv(getenv)
		c.FromEnv = true
	}
	if err := c.validate(); err != nil {
		return c, fmt.Errorf("%s: supervisor.%w", workerConfigPath, err)
	}
	c.applyPreset()
	return c, nil
}

func providerFromEnv(getenv func(string) string) string {
	switch {
	case getenv("ANTHROPIC_API_KEY") != "":
		return "anthropic"
	case getenv("OPENAI_API_KEY") != "":
		return "openai"
	case getenv("DEEPSEEK_API_KEY") != "":
		return "deepseek"
	case getenv("HF_TOKEN") != "":
		return "huggingface"
	default:
		return "ollama"
	}
}

func (c *Config) validate() error {
	if _, ok := presets[c.Provider]; !ok {
		return fmt.Errorf("provider: want %s, got %q", Providers, c.Provider)
	}
	if c.MaxTokens < 0 {
		return errors.New("max_tokens must be positive")
	}
	if c.StepBudget < 0 {
		return errors.New("step_budget must be positive")
	}
	return nil
}

// applyPreset fills empty fields from the provider's preset.
func (c *Config) applyPreset() {
	p := presets[c.Provider]
	if c.Model == "" {
		c.Model = p.model
	}
	if c.BaseURL == "" {
		c.BaseURL = p.baseURL
	}
	if c.APIKeyEnv == "" {
		c.APIKeyEnv = p.keyEnv
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = defaultMaxTokens
	}
	if c.StepBudget == 0 {
		c.StepBudget = defaultStepBudget
	}
}

// Override applies -provider and -model. A new provider resets the model,
// base URL and key env so its presets apply.
func (c *Config) Override(provider, modelName string) {
	if provider != "" && provider != c.Provider {
		c.Provider = provider
		c.Model, c.BaseURL, c.APIKeyEnv = "", "", ""
		c.FromEnv = false
	}
	if modelName != "" {
		c.Model = modelName
	}
	c.applyPreset()
}

// NewModel builds the provider's client. A missing key is reported here,
// naming the variable, never on the first call.
func NewModel(c Config, getenv func(string) string) (model.Model, error) {
	key := ""
	if c.APIKeyEnv != "" {
		key = getenv(c.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("%s is not set (supervisor provider %s; change supervisor.api_key_env in the worker config to use another variable)", c.APIKeyEnv, c.Provider)
		}
	}
	switch c.Provider {
	case "anthropic":
		return anthropic.New(anthropic.Config{Model: c.Model, APIKey: key, BaseURL: c.BaseURL})
	case "openai", "deepseek", "huggingface", "ollama":
		return openai.New(openai.Config{BaseURL: c.BaseURL, Model: c.Model, APIKey: key, Vendor: c.Provider})
	case "fake":
		return fake.New(fake.Text("(fake supervisor: no model configured)")), nil
	default:
		return nil, fmt.Errorf("provider: want %s, got %q", Providers, c.Provider)
	}
}

// ProbeOllama checks that an Ollama server answers at baseURL, so a
// defaulted provider fails with a useful message instead of a timeout on
// the first turn.
func ProbeOllama(ctx context.Context, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL)
	}
	return nil
}
