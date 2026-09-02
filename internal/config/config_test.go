package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gemsub/internal/config"
)

func writeTestConfig(t *testing.T, dir string, cfg map[string]interface{}) string {
	t.Helper()
	path := filepath.Join(dir, "test_config.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func baseConfig() map[string]interface{} {
	return map[string]interface{}{
		"sources":        []string{"https://example.com/sub"},
		"fetch_interval": "3h",
		"test": map[string]interface{}{
			"target_url":    "https://gemini.google.com/",
			"block_phrases": []string{"not available"},
			"timeout":       "10s",
			"concurrency":   10,
		},
		"serve": map[string]interface{}{
			"listen": "127.0.0.1:8765",
			"path":   "/sub",
			"format": "base64",
		},
		"state_file": "./test_state.json",
	}
}

func TestMaxRetries_OmittedDefaultsTo2(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	// max_retries intentionally omitted
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.MaxRetries != 2 {
		t.Errorf("expected default MaxRetries=2 when omitted, got %d", cfg.Test.MaxRetries)
	}
}

func TestMaxRetries_ExplicitZeroMeansNoRetries(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	testCfg["max_retries"] = 0
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.MaxRetries != 0 {
		t.Errorf("expected MaxRetries=0 for explicit zero, got %d", cfg.Test.MaxRetries)
	}
}

func TestMaxRetries_ExplicitTwoMeansThreeAttempts(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	testCfg["max_retries"] = 2
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.MaxRetries != 2 {
		t.Errorf("expected MaxRetries=2, got %d", cfg.Test.MaxRetries)
	}
}

func TestMaxRetries_NegativeReturnsError(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	testCfg["max_retries"] = -1
	path := writeTestConfig(t, dir, cfgMap)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected validation error for negative max_retries")
	}
}
