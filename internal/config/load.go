package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where Load looks when no path is given.
func DefaultPath() string {
	return filepath.Join(homeDir(), ".config", "hivedispatch", "config.yaml")
}

// Load reads a YAML worker config, fills secrets from the environment,
// applies defaults, and validates.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.Jira.Token = os.Getenv("HIVE_JIRA_TOKEN")
	c.Workroot = expandHome(c.Workroot)
	if c.Workroot == "" {
		c.Workroot = filepath.Join(homeDir(), ".local", "share", "hivedispatch")
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), strings.TrimPrefix(p, "~"))
	}
	return p
}
