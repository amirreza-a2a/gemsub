package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestPublishing_ValidExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	cfgMap["publishing"] = map[string]interface{}{
		"enabled":    true,
		"repository": "~/gemsub-subscriptions",
		"remote_url": "git@github.com:example/gemsub-subscriptions.git",
	}
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Publishing.Enabled {
		t.Errorf("expected Publishing.Enabled=true")
	}
	if cfg.Publishing.Branch != "main" {
		t.Errorf("expected default branch 'main', got %q", cfg.Publishing.Branch)
	}
	if cfg.Publishing.RemoteURL != "git@github.com:example/gemsub-subscriptions.git" {
		t.Errorf("expected RemoteURL 'git@github.com:example/gemsub-subscriptions.git', got %q", cfg.Publishing.RemoteURL)
	}
	if cfg.Publishing.Repository != "~/gemsub-subscriptions" {
		t.Errorf("expected repository '~/gemsub-subscriptions', got %q", cfg.Publishing.Repository)
	}
}

func TestPublishing_EnabledRequiresRemoteURL(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	cfgMap["publishing"] = map[string]interface{}{
		"enabled":    true,
		"repository": "~/gemsub-subscriptions",
		// remote_url omitted
	}
	path := writeTestConfig(t, dir, cfgMap)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error when publishing is enabled without remote_url")
	}
	expected := "publishing.remote_url must not be empty when publishing is enabled"
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error containing %q, got %q", expected, err.Error())
	}
}

func TestPublishing_EnabledRequiresRepository(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	cfgMap["publishing"] = map[string]interface{}{
		"enabled":    true,
		"remote_url": "git@github.com:example/gemsub-subscriptions.git",
		// repository omitted
	}
	path := writeTestConfig(t, dir, cfgMap)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error when publishing is enabled without repository")
	}
	expected := "publishing.repository must not be empty when publishing is enabled"
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error containing %q, got %q", expected, err.Error())
	}
}

func TestPublishing_DisabledPermitsEmptyFields(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	cfgMap["publishing"] = map[string]interface{}{
		"enabled": false,
		// repository and remote_url omitted
	}
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Publishing.Enabled {
		t.Errorf("expected Publishing.Enabled=false")
	}
	if cfg.Publishing.RemoteURL != "" {
		t.Errorf("expected empty RemoteURL when disabled, got %q", cfg.Publishing.RemoteURL)
	}
	if cfg.Publishing.Repository != "" {
		t.Errorf("expected empty Repository when disabled, got %q", cfg.Publishing.Repository)
	}
	if cfg.Publishing.Branch != "main" {
		t.Errorf("expected default branch 'main', got %q", cfg.Publishing.Branch)
	}
}
