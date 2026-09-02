// Package config loads and validates gemsub's JSON configuration file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type TestConfig struct {
	TargetURL    string   `json:"target_url"`
	BlockPhrases []string `json:"block_phrases"`
	TimeoutRaw   string   `json:"timeout"`
	Concurrency  int      `json:"concurrency"`

	// Parsed fields, populated by Validate.
	Timeout time.Duration `json:"-"`
}

type ServeConfig struct {
	Listen string `json:"listen"`
	Path   string `json:"path"`
	Format string `json:"format"` // "raw" or "base64"
}

type Config struct {
	Sources          []string    `json:"sources"`
	FetchIntervalRaw string      `json:"fetch_interval"`
	Test             TestConfig  `json:"test"`
	Serve            ServeConfig `json:"serve"`
	StateFile        string      `json:"state_file"`
	Headless         bool        `json:"headless"`

	// Parsed fields, populated by Validate.
	FetchInterval time.Duration `json:"-"`
}

// Load reads and parses the config file at path, then validates it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// Validate checks required fields and parses duration/default values.
// Safe to call multiple times (idempotent).
func (c *Config) Validate() error {
	if len(c.Sources) == 0 {
		return fmt.Errorf("sources must not be empty")
	}

	fetchInterval, err := time.ParseDuration(c.FetchIntervalRaw)
	if err != nil {
		return fmt.Errorf("fetch_interval: %w", err)
	}
	if fetchInterval < time.Minute {
		return fmt.Errorf("fetch_interval must be at least 1m, got %s", fetchInterval)
	}
	c.FetchInterval = fetchInterval

	if c.Test.TargetURL == "" {
		return fmt.Errorf("test.target_url must not be empty")
	}
	if len(c.Test.BlockPhrases) == 0 {
		return fmt.Errorf("test.block_phrases must not be empty")
	}

	timeout, err := time.ParseDuration(c.Test.TimeoutRaw)
	if err != nil {
		return fmt.Errorf("test.timeout: %w", err)
	}
	if timeout <= 0 {
		return fmt.Errorf("test.timeout must be positive")
	}
	c.Test.Timeout = timeout

	if c.Test.Concurrency <= 0 {
		c.Test.Concurrency = 20
	}

	if c.Serve.Listen == "" {
		c.Serve.Listen = "127.0.0.1:8765"
	}
	if c.Serve.Path == "" {
		c.Serve.Path = "/sub"
	}
	switch c.Serve.Format {
	case "":
		c.Serve.Format = "base64"
	case "raw", "base64":
		// ok
	default:
		return fmt.Errorf("serve.format must be \"raw\" or \"base64\", got %q", c.Serve.Format)
	}

	if c.StateFile == "" {
		c.StateFile = "./gemsub_state.json"
	}

	return nil
}
