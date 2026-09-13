package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gemsub/internal/config"
	"gemsub/internal/paths"
)

func TestConfig_DefaultStatePath_OmittedAndEmpty(t *testing.T) {
	expectedDefault, err := paths.StatePath("")
	if err != nil {
		t.Fatalf("paths.StatePath: %v", err)
	}

	// 1. Omitted state_file in JSON
	dir := t.TempDir()
	cfgMap := baseConfig()
	delete(cfgMap, "state_file")
	path1 := writeTestConfig(t, dir, cfgMap)

	loaded1, err := config.Load(path1)
	if err != nil {
		t.Fatalf("config.Load omitted state_file: %v", err)
	}
	if loaded1.StateFile != expectedDefault {
		t.Errorf("omitted state_file: expected %q, got %q", expectedDefault, loaded1.StateFile)
	}

	// 2. Explicitly empty state_file in JSON: "state_file": ""
	cfgMap["state_file"] = ""
	path2 := filepath.Join(dir, "test_config_empty.json")
	data2, err := json.Marshal(cfgMap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path2, data2, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded2, err := config.Load(path2)
	if err != nil {
		t.Fatalf("config.Load empty state_file: %v", err)
	}
	if loaded2.StateFile != expectedDefault {
		t.Errorf("empty state_file: expected %q, got %q", expectedDefault, loaded2.StateFile)
	}
}

func TestConfig_ExplicitStateFile_PreservedVerbatim(t *testing.T) {
	cases := []string{
		"./gemsub_state.json",
		"./custom/state.json",
		"relative/state.json",
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, `C:\custom\state.json`, `D:\data\gemsub\state.json`)
	} else {
		cases = append(cases, "/var/lib/gemsub/state.json", "/tmp/custom_state.json")
	}

	dir := t.TempDir()
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			cfgMap := baseConfig()
			cfgMap["state_file"] = tc
			path := writeTestConfig(t, dir, cfgMap)

			loaded, err := config.Load(path)
			if err != nil {
				t.Fatalf("config.Load failed for %q: %v", tc, err)
			}
			if loaded.StateFile != tc {
				t.Errorf("explicit state_file mutated: expected %q, got %q", tc, loaded.StateFile)
			}
		})
	}
}

func TestConfig_StateLocationIndependentFromConfigLocation(t *testing.T) {
	// An arbitrary config file location should not influence default state location.
	arbitraryDir := filepath.Join(t.TempDir(), "deep", "custom", "dir")
	if err := os.MkdirAll(arbitraryDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfgMap := baseConfig()
	delete(cfgMap, "state_file")
	cfgPath := writeTestConfig(t, arbitraryDir, cfgMap)

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	expectedCanonical, err := paths.StatePath("")
	if err != nil {
		t.Fatalf("paths.StatePath: %v", err)
	}

	if loaded.StateFile != expectedCanonical {
		t.Fatalf("expected state_file %q, got %q", expectedCanonical, loaded.StateFile)
	}
	if strings.HasPrefix(loaded.StateFile, arbitraryDir) {
		t.Fatalf("state_file %q was incorrectly derived from config dir %q", loaded.StateFile, arbitraryDir)
	}
}

func TestConfig_DefaultStatePath_XDGEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG environment variables do not apply to Windows")
	}

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// 1. Explicit XDG_STATE_HOME set
	customXDG := filepath.Join(t.TempDir(), "custom_xdg_state")
	t.Setenv("XDG_STATE_HOME", customXDG)

	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceItem{{URL: "https://example.com/sub", Enabled: true}}
	cfg.StateFile = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}
	expectedCustom := filepath.Join(customXDG, "gemsub", "state.json")
	if cfg.StateFile != expectedCustom {
		t.Errorf("expected custom XDG state path %q, got %q", expectedCustom, cfg.StateFile)
	}

	// 2. XDG_STATE_HOME unset -> fallback to $HOME/.local/state/gemsub/state.json
	t.Setenv("XDG_STATE_HOME", "")
	cfg2 := config.DefaultConfig()
	cfg2.Sources = []config.SourceItem{{URL: "https://example.com/sub", Enabled: true}}
	cfg2.StateFile = ""
	if err := cfg2.Validate(); err != nil {
		t.Fatalf("cfg2.Validate: %v", err)
	}
	expectedFallback := filepath.Join(tempHome, ".local", "state", "gemsub", "state.json")
	if cfg2.StateFile != expectedFallback {
		t.Errorf("expected fallback XDG state path %q, got %q", expectedFallback, cfg2.StateFile)
	}
}

func TestDefaultConfig_HasCanonicalStatePath(t *testing.T) {
	expectedDefault, err := paths.StatePath("")
	if err != nil {
		t.Fatalf("paths.StatePath: %v", err)
	}

	cfg := config.DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
	}
	if cfg.StateFile != expectedDefault {
		t.Errorf("DefaultConfig.StateFile: expected %q, got %q", expectedDefault, cfg.StateFile)
	}
}

func TestNewDefaultService_HasCanonicalStatePath(t *testing.T) {
	expectedDefault, err := paths.StatePath("")
	if err != nil {
		t.Fatalf("paths.StatePath: %v", err)
	}

	svc := config.NewDefaultService("/tmp/dummy-config.json", nil)
	if svc == nil {
		t.Fatal("NewDefaultService() returned nil")
	}
	currentCfg := svc.Get()
	if currentCfg.StateFile != expectedDefault {
		t.Errorf("NewDefaultService state file: expected %q, got %q", expectedDefault, currentCfg.StateFile)
	}
}

func TestDefaultConfig_MissingHome_ValidateFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME/XDG resolution tests apply to Unix/Linux")
	}

	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceItem{{URL: "https://example.com/sub", Enabled: true}}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected Validate() to fail when HOME and XDG_STATE_HOME are empty")
	}
	if !strings.Contains(err.Error(), "resolve state file") {
		t.Errorf("expected error to mention resolving state file, got: %v", err)
	}
}
