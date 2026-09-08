package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestFlagMode_Validation(t *testing.T) {
	dir := t.TempDir()

	validModes := []string{"", "auto", "unicode", "ascii"}
	for _, m := range validModes {
		cfgMap := baseConfig()
		if m != "" {
			cfgMap["flag_mode"] = m
		}
		path := writeTestConfig(t, dir, cfgMap)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("expected mode %q to be valid, got err: %v", m, err)
		}
		if cfg.FlagMode != m {
			t.Errorf("expected FlagMode=%q, got %q", m, cfg.FlagMode)
		}
	}

	invalidMap := baseConfig()
	invalidMap["flag_mode"] = "invalid_mode"
	path := writeTestConfig(t, dir, invalidMap)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for invalid flag_mode, got nil")
	}
	if !strings.Contains(err.Error(), "flag_mode") {
		t.Errorf("expected error message to mention flag_mode, got: %v", err)
	}
}

func TestHealthConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.HealthURL != "https://www.gstatic.com/generate_204" {
		t.Errorf("expected default HealthURL='https://www.gstatic.com/generate_204', got %q", cfg.Test.HealthURL)
	}
	if cfg.Test.HealthTimeout != 4*time.Second {
		t.Errorf("expected default HealthTimeout=4s, got %s", cfg.Test.HealthTimeout)
	}
}

func TestHealthConfig_InheritDialTimeout(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	testCfg["dial_timeout"] = "6s"
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.HealthTimeout != 6*time.Second {
		t.Errorf("expected HealthTimeout to inherit dial_timeout (6s), got %s", cfg.Test.HealthTimeout)
	}
}

func TestHealthConfig_ExplicitValues(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	testCfg["health_url"] = "https://custom.connectivity.test/check"
	testCfg["health_timeout"] = "2s"
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.HealthURL != "https://custom.connectivity.test/check" {
		t.Errorf("expected custom HealthURL, got %q", cfg.Test.HealthURL)
	}
	if cfg.Test.HealthTimeout != 2*time.Second {
		t.Errorf("expected HealthTimeout=2s, got %s", cfg.Test.HealthTimeout)
	}
}

func TestHealthConfig_InvalidTimeout(t *testing.T) {
	dir := t.TempDir()

	invalidCases := []struct {
		val string
		msg string
	}{
		{"invalid_duration", "test.health_timeout"},
		{"0s", "test.health_timeout must be positive"},
		{"-5s", "test.health_timeout must be positive"},
	}

	for _, tc := range invalidCases {
		cfgMap := baseConfig()
		testCfg := cfgMap["test"].(map[string]interface{})
		testCfg["health_timeout"] = tc.val
		path := writeTestConfig(t, dir, cfgMap)

		_, err := config.Load(path)
		if err == nil {
			t.Errorf("expected error for health_timeout=%q, got nil", tc.val)
			continue
		}
		if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("expected error containing %q, got %q", tc.msg, err.Error())
		}
	}
}

func TestGeminiConfig_LegacyFallback(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	testCfg["target_url"] = "https://legacy.gemini.endpoint/test"
	testCfg["block_phrases"] = []string{"legacy block phrase"}
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.Gemini.URL != "https://legacy.gemini.endpoint/test" {
		t.Errorf("expected Gemini.URL to be populated from target_url, got %q", cfg.Test.Gemini.URL)
	}
	if len(cfg.Test.Gemini.BlockPhrases) != 1 || cfg.Test.Gemini.BlockPhrases[0] != "legacy block phrase" {
		t.Errorf("expected Gemini.BlockPhrases populated from legacy block_phrases, got %v", cfg.Test.Gemini.BlockPhrases)
	}
	if cfg.Test.TargetURL != cfg.Test.Gemini.URL {
		t.Errorf("expected TargetURL to match Gemini.URL for backward compatibility")
	}
	if len(cfg.Test.BlockPhrases) != 1 || cfg.Test.BlockPhrases[0] != "legacy block phrase" {
		t.Errorf("expected BlockPhrases to match Gemini.BlockPhrases for backward compatibility")
	}
}

func TestGeminiConfig_NewSyntax(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	delete(testCfg, "target_url")
	delete(testCfg, "block_phrases")
	testCfg["gemini"] = map[string]interface{}{
		"url":           "https://new.gemini.endpoint/app",
		"block_phrases": []string{"new block 1", "new block 2"},
	}
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.Gemini.URL != "https://new.gemini.endpoint/app" {
		t.Errorf("expected Gemini.URL=%q, got %q", "https://new.gemini.endpoint/app", cfg.Test.Gemini.URL)
	}
	if len(cfg.Test.Gemini.BlockPhrases) != 2 || cfg.Test.Gemini.BlockPhrases[0] != "new block 1" {
		t.Errorf("expected Gemini.BlockPhrases to be populated from new syntax, got %v", cfg.Test.Gemini.BlockPhrases)
	}
	if cfg.Test.TargetURL != cfg.Test.Gemini.URL {
		t.Errorf("expected TargetURL synced to Gemini.URL, got %q", cfg.Test.TargetURL)
	}
	if len(cfg.Test.BlockPhrases) != 2 || cfg.Test.BlockPhrases[0] != "new block 1" {
		t.Errorf("expected BlockPhrases synced to Gemini.BlockPhrases, got %v", cfg.Test.BlockPhrases)
	}
}

func TestGeminiConfig_Precedence(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	testCfg["target_url"] = "https://legacy.gemini.endpoint/old"
	testCfg["block_phrases"] = []string{"old phrase"}
	testCfg["gemini"] = map[string]interface{}{
		"url":           "https://new.gemini.endpoint/new",
		"block_phrases": []string{"new phrase"},
	}
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.Gemini.URL != "https://new.gemini.endpoint/new" {
		t.Errorf("expected new gemini.url to take precedence, got %q", cfg.Test.Gemini.URL)
	}
	if len(cfg.Test.Gemini.BlockPhrases) != 1 || cfg.Test.Gemini.BlockPhrases[0] != "new phrase" {
		t.Errorf("expected new gemini.block_phrases to take precedence, got %v", cfg.Test.Gemini.BlockPhrases)
	}
	if cfg.Test.TargetURL != "https://new.gemini.endpoint/new" {
		t.Errorf("expected TargetURL to sync with winning Gemini.URL, got %q", cfg.Test.TargetURL)
	}
	if len(cfg.Test.BlockPhrases) != 1 || cfg.Test.BlockPhrases[0] != "new phrase" {
		t.Errorf("expected BlockPhrases to sync with winning Gemini.BlockPhrases, got %v", cfg.Test.BlockPhrases)
	}
}

func TestGeminiConfig_DefaultURL(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	delete(testCfg, "target_url")
	testCfg["gemini"] = map[string]interface{}{
		"block_phrases": []string{"block phrase"},
	}
	path := writeTestConfig(t, dir, cfgMap)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Test.Gemini.URL != "https://gemini.google.com/" {
		t.Errorf("expected default Gemini.URL='https://gemini.google.com/', got %q", cfg.Test.Gemini.URL)
	}
	if cfg.Test.TargetURL != "https://gemini.google.com/" {
		t.Errorf("expected default TargetURL='https://gemini.google.com/', got %q", cfg.Test.TargetURL)
	}
}

func TestGeminiConfig_MissingBlockPhrases(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	testCfg := cfgMap["test"].(map[string]interface{})
	delete(testCfg, "block_phrases")
	delete(testCfg, "gemini")
	path := writeTestConfig(t, dir, cfgMap)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error when block phrases are omitted, got nil")
	}
	if !strings.Contains(err.Error(), "block_phrases must not be empty") {
		t.Errorf("expected error message to mention block_phrases, got: %v", err)
	}
}

func TestConfigExample_LoadsSuccessfully(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.json")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("failed to load config.example.json: %v", err)
	}
	if cfg.Test.HealthURL != "https://www.gstatic.com/generate_204" {
		t.Errorf("expected health_url in config.example.json, got %q", cfg.Test.HealthURL)
	}
	if cfg.Test.Gemini.URL != "https://gemini.google.com/" {
		t.Errorf("expected gemini.url in config.example.json, got %q", cfg.Test.Gemini.URL)
	}
	if len(cfg.Test.Gemini.BlockPhrases) == 0 {
		t.Errorf("expected non-empty block phrases in config.example.json")
	}
}

func TestConfigJson_LoadsSuccessfully(t *testing.T) {
	path := filepath.Join("..", "..", "config.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("config.json does not exist, skipping")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("failed to load existing config.json: %v", err)
	}
	if cfg.Test.HealthURL != "https://www.gstatic.com/generate_204" {
		t.Errorf("expected default health_url for legacy config.json, got %q", cfg.Test.HealthURL)
	}
	if cfg.Test.Gemini.URL != "https://gemini.google.com/app" {
		t.Errorf("expected legacy target_url to populate Gemini.URL, got %q", cfg.Test.Gemini.URL)
	}
	if len(cfg.Test.Gemini.BlockPhrases) == 0 {
		t.Errorf("expected legacy block_phrases to populate Gemini.BlockPhrases")
	}
}
