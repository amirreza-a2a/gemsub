package paths_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gemsub/internal/paths"
)

func mockLookup(goos, home, xdgConfig, xdgState, appData, localAppData string) paths.Lookup {
	return paths.Lookup{
		Getenv: func(key string) string {
			switch key {
			case "XDG_CONFIG_HOME":
				return xdgConfig
			case "XDG_STATE_HOME":
				return xdgState
			case "HOME":
				return home
			default:
				return ""
			}
		},
		UserConfigDir: func() (string, error) {
			if goos == "windows" {
				if appData == "" {
					return "", errors.New("AppData directory unavailable")
				}
				return appData, nil
			}
			if xdgConfig != "" && strings.HasPrefix(xdgConfig, "/") {
				return xdgConfig, nil
			}
			if home != "" && strings.HasPrefix(home, "/") {
				return filepath.Join(home, ".config"), nil
			}
			return "", errors.New("user config directory unavailable")
		},
		UserCacheDir: func() (string, error) {
			if goos == "windows" {
				if localAppData == "" {
					return "", errors.New("LocalAppData directory unavailable")
				}
				return localAppData, nil
			}
			if home != "" && strings.HasPrefix(home, "/") {
				return filepath.Join(home, ".cache"), nil
			}
			return "", errors.New("user cache directory unavailable")
		},
		UserHomeDir: func() (string, error) {
			if home == "" {
				return "", errors.New("user home directory unavailable")
			}
			return home, nil
		},
		GOOS: goos,
	}
}

func TestConfigPath_ExplicitOverride(t *testing.T) {
	lk := mockLookup("linux", "/home/alice", "", "", "", "")

	overrides := []string{
		"./config.json",
		"config.json",
		"/tmp/custom-config.json",
		"../relative/config.json",
		"C:\\custom\\config.json",
	}

	for _, ov := range overrides {
		t.Run("Override_"+ov, func(t *testing.T) {
			got, err := paths.ConfigPathWithLookup(ov, lk)
			if err != nil {
				t.Fatalf("unexpected error for explicit override %q: %v", ov, err)
			}
			if got != ov {
				t.Errorf("expected explicit override %q preserved, got %q", ov, got)
			}
		})
	}
}

func TestStatePath_ExplicitOverride(t *testing.T) {
	lk := mockLookup("linux", "/home/alice", "", "", "", "")

	overrides := []string{
		"./gemsub_state.json",
		"gemsub_state.json",
		"/var/lib/gemsub/state.json",
		"state.json.gz",
	}

	for _, ov := range overrides {
		t.Run("Override_"+ov, func(t *testing.T) {
			got, err := paths.StatePathWithLookup(ov, lk)
			if err != nil {
				t.Fatalf("unexpected error for explicit override %q: %v", ov, err)
			}
			if got != ov {
				t.Errorf("expected explicit override %q preserved, got %q", ov, got)
			}
		})
	}
}

func TestLinux_ConfigDirAndPath_Default(t *testing.T) {
	lk := mockLookup("linux", "/home/alice", "", "", "", "")

	dir, err := paths.ConfigDirWithLookup(lk)
	if err != nil {
		t.Fatalf("ConfigDirWithLookup failed: %v", err)
	}
	expectedDir := "/home/alice/.config/gemsub"
	if dir != expectedDir {
		t.Errorf("expected config dir %q, got %q", expectedDir, dir)
	}

	path, err := paths.ConfigPathWithLookup("", lk)
	if err != nil {
		t.Fatalf("ConfigPathWithLookup failed: %v", err)
	}
	expectedPath := "/home/alice/.config/gemsub/config.json"
	if path != expectedPath {
		t.Errorf("expected config path %q, got %q", expectedPath, path)
	}
}

func TestLinux_ConfigDirAndPath_CustomXDG(t *testing.T) {
	lk := mockLookup("linux", "/home/alice", "/custom/xdg_config", "", "", "")

	dir, err := paths.ConfigDirWithLookup(lk)
	if err != nil {
		t.Fatalf("ConfigDirWithLookup failed: %v", err)
	}
	expectedDir := "/custom/xdg_config/gemsub"
	if dir != expectedDir {
		t.Errorf("expected config dir %q, got %q", expectedDir, dir)
	}

	path, err := paths.ConfigPathWithLookup("", lk)
	if err != nil {
		t.Fatalf("ConfigPathWithLookup failed: %v", err)
	}
	expectedPath := "/custom/xdg_config/gemsub/config.json"
	if path != expectedPath {
		t.Errorf("expected config path %q, got %q", expectedPath, path)
	}
}

func TestLinux_ConfigDirAndPath_RelativeXDG_Ignored(t *testing.T) {
	lk := mockLookup("linux", "/home/alice", "relative/.config", "", "", "")

	dir, err := paths.ConfigDirWithLookup(lk)
	if err != nil {
		t.Fatalf("ConfigDirWithLookup failed: %v", err)
	}
	expectedDir := "/home/alice/.config/gemsub"
	if dir != expectedDir {
		t.Errorf("expected relative XDG_CONFIG_HOME ignored and fallback %q used, got %q", expectedDir, dir)
	}

	path, err := paths.ConfigPathWithLookup("", lk)
	if err != nil {
		t.Fatalf("ConfigPathWithLookup failed: %v", err)
	}
	expectedPath := "/home/alice/.config/gemsub/config.json"
	if path != expectedPath {
		t.Errorf("expected fallback config path %q, got %q", expectedPath, path)
	}
}

func TestLinux_StateDirAndPath_Default(t *testing.T) {
	lk := mockLookup("linux", "/home/alice", "", "", "", "")

	dir, err := paths.StateDirWithLookup(lk)
	if err != nil {
		t.Fatalf("StateDirWithLookup failed: %v", err)
	}
	expectedDir := "/home/alice/.local/state/gemsub"
	if dir != expectedDir {
		t.Errorf("expected state dir %q, got %q", expectedDir, dir)
	}

	path, err := paths.StatePathWithLookup("", lk)
	if err != nil {
		t.Fatalf("StatePathWithLookup failed: %v", err)
	}
	expectedPath := "/home/alice/.local/state/gemsub/state.json"
	if path != expectedPath {
		t.Errorf("expected state path %q, got %q", expectedPath, path)
	}
}

func TestLinux_StateDirAndPath_CustomXDG(t *testing.T) {
	lk := mockLookup("linux", "/home/alice", "", "/custom/xdg_state", "", "")

	dir, err := paths.StateDirWithLookup(lk)
	if err != nil {
		t.Fatalf("StateDirWithLookup failed: %v", err)
	}
	expectedDir := "/custom/xdg_state/gemsub"
	if dir != expectedDir {
		t.Errorf("expected state dir %q, got %q", expectedDir, dir)
	}

	path, err := paths.StatePathWithLookup("", lk)
	if err != nil {
		t.Fatalf("StatePathWithLookup failed: %v", err)
	}
	expectedPath := "/custom/xdg_state/gemsub/state.json"
	if path != expectedPath {
		t.Errorf("expected state path %q, got %q", expectedPath, path)
	}
}

func TestLinux_StateDirAndPath_RelativeXDG_Ignored(t *testing.T) {
	lk := mockLookup("linux", "/home/alice", "", "relative/.local/state", "", "")

	dir, err := paths.StateDirWithLookup(lk)
	if err != nil {
		t.Fatalf("StateDirWithLookup failed: %v", err)
	}
	expectedDir := "/home/alice/.local/state/gemsub"
	if dir != expectedDir {
		t.Errorf("expected relative XDG_STATE_HOME ignored and fallback %q used, got %q", expectedDir, dir)
	}

	path, err := paths.StatePathWithLookup("", lk)
	if err != nil {
		t.Fatalf("StatePathWithLookup failed: %v", err)
	}
	expectedPath := "/home/alice/.local/state/gemsub/state.json"
	if path != expectedPath {
		t.Errorf("expected fallback state path %q, got %q", expectedPath, path)
	}
}

func TestLinux_MissingHome_ReturnsError(t *testing.T) {
	lk := mockLookup("linux", "", "", "", "", "")

	if _, err := paths.ConfigDirWithLookup(lk); err == nil {
		t.Error("expected error for ConfigDir when HOME is missing, got nil")
	}
	if _, err := paths.ConfigPathWithLookup("", lk); err == nil {
		t.Error("expected error for ConfigPath when HOME is missing, got nil")
	}

	if _, err := paths.StateDirWithLookup(lk); err == nil {
		t.Error("expected error for StateDir when HOME is missing, got nil")
	}
	if _, err := paths.StatePathWithLookup("", lk); err == nil {
		t.Error("expected error for StatePath when HOME is missing, got nil")
	}
}

func TestLinux_RelativeHome_ReturnsError(t *testing.T) {
	lk := mockLookup("linux", "relative/home", "", "", "", "")

	if _, err := paths.ConfigDirWithLookup(lk); err == nil {
		t.Error("expected error for ConfigDir when HOME is relative, got nil")
	}
	if _, err := paths.ConfigPathWithLookup("", lk); err == nil {
		t.Error("expected error for ConfigPath when HOME is relative, got nil")
	}

	if _, err := paths.StateDirWithLookup(lk); err == nil {
		t.Error("expected error for StateDir when HOME is relative, got nil")
	}
	if _, err := paths.StatePathWithLookup("", lk); err == nil {
		t.Error("expected error for StatePath when HOME is relative, got nil")
	}
}

func TestWindows_ConfigAndStatePaths(t *testing.T) {
	appData := `C:\Users\bob\AppData\Roaming`
	localAppData := `C:\Users\bob\AppData\Local`
	lk := mockLookup("windows", `C:\Users\bob`, "", "", appData, localAppData)

	cfgDir, err := paths.ConfigDirWithLookup(lk)
	if err != nil {
		t.Fatalf("ConfigDirWithLookup on windows failed: %v", err)
	}
	expectedCfgDir := `C:\Users\bob\AppData\Roaming\gemsub`
	if cfgDir != expectedCfgDir {
		t.Errorf("expected windows config dir %q, got %q", expectedCfgDir, cfgDir)
	}

	cfgPath, err := paths.ConfigPathWithLookup("", lk)
	if err != nil {
		t.Fatalf("ConfigPathWithLookup on windows failed: %v", err)
	}
	expectedCfgPath := `C:\Users\bob\AppData\Roaming\gemsub\config.json`
	if cfgPath != expectedCfgPath {
		t.Errorf("expected windows config path %q, got %q", expectedCfgPath, cfgPath)
	}

	stateDir, err := paths.StateDirWithLookup(lk)
	if err != nil {
		t.Fatalf("StateDirWithLookup on windows failed: %v", err)
	}
	expectedStateDir := `C:\Users\bob\AppData\Local\gemsub`
	if stateDir != expectedStateDir {
		t.Errorf("expected windows state dir %q, got %q", expectedStateDir, stateDir)
	}

	statePath, err := paths.StatePathWithLookup("", lk)
	if err != nil {
		t.Fatalf("StatePathWithLookup on windows failed: %v", err)
	}
	expectedStatePath := `C:\Users\bob\AppData\Local\gemsub\state.json`
	if statePath != expectedStatePath {
		t.Errorf("expected windows state path %q, got %q", expectedStatePath, statePath)
	}
}

func TestWindows_MissingAppData_ReturnsError(t *testing.T) {
	lk := mockLookup("windows", `C:\Users\bob`, "", "", "", "")

	if _, err := paths.ConfigDirWithLookup(lk); err == nil {
		t.Error("expected error for ConfigDir on windows with missing AppData, got nil")
	}
	if _, err := paths.ConfigPathWithLookup("", lk); err == nil {
		t.Error("expected error for ConfigPath on windows with missing AppData, got nil")
	}
	if _, err := paths.StateDirWithLookup(lk); err == nil {
		t.Error("expected error for StateDir on windows with missing LocalAppData, got nil")
	}
	if _, err := paths.StatePathWithLookup("", lk); err == nil {
		t.Error("expected error for StatePath on windows with missing LocalAppData, got nil")
	}
}

func TestEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Nested file path whose parent directories do not exist
	nestedFile := filepath.Join(tmpDir, "level1", "level2", "file.json")
	if err := paths.EnsureDir(nestedFile, 0o700); err != nil {
		t.Fatalf("EnsureDir failed for nested file: %v", err)
	}

	parentDir := filepath.Dir(nestedFile)
	fi, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("expected parent directory %s to exist: %v", parentDir, err)
	}
	if !fi.IsDir() {
		t.Fatalf("expected %s to be a directory", parentDir)
	}

	// 2. Calling EnsureDir again should be idempotent
	if err := paths.EnsureDir(nestedFile, 0o700); err != nil {
		t.Fatalf("EnsureDir failed on repeated call: %v", err)
	}

	// 3. Empty path returns error
	if err := paths.EnsureDir("", 0o700); err == nil {
		t.Error("expected error for empty path in EnsureDir, got nil")
	}
}

func TestLiveDefaults(t *testing.T) {
	// Verifies that calling default APIs against the live OS environment does not panic
	// and returns non-empty paths on the current platform.
	cfgDir, err := paths.ConfigDir()
	if err != nil {
		t.Fatalf("live ConfigDir failed: %v", err)
	}
	if cfgDir == "" {
		t.Error("expected non-empty live ConfigDir")
	}

	cfgPath, err := paths.ConfigPath("")
	if err != nil {
		t.Fatalf("live ConfigPath failed: %v", err)
	}
	if cfgPath == "" {
		t.Error("expected non-empty live ConfigPath")
	}
	if !strings.HasSuffix(cfgPath, "config.json") {
		t.Errorf("expected live ConfigPath to end with 'config.json', got %q", cfgPath)
	}

	stateDir, err := paths.StateDir()
	if err != nil {
		t.Fatalf("live StateDir failed: %v", err)
	}
	if stateDir == "" {
		t.Error("expected non-empty live StateDir")
	}

	statePath, err := paths.StatePath("")
	if err != nil {
		t.Fatalf("live StatePath failed: %v", err)
	}
	if statePath == "" {
		t.Error("expected non-empty live StatePath")
	}
	if !strings.HasSuffix(statePath, "state.json") {
		t.Errorf("expected live StatePath to end with 'state.json', got %q", statePath)
	}
}

func TestQuoteShellArg(t *testing.T) {
	tests := []struct {
		name string
		in   string
		goos string
		want string
	}{
		{
			name: "empty posix",
			in:   "",
			goos: "linux",
			want: "''",
		},
		{
			name: "empty windows (PowerShell literal)",
			in:   "",
			goos: "windows",
			want: "''",
		},
		{
			name: "safe posix path",
			in:   "/home/user/.config/gemsub/config.json",
			goos: "linux",
			want: "/home/user/.config/gemsub/config.json",
		},
		{
			name: "safe windows path",
			in:   `C:\Users\User\AppData\Roaming\gemsub\config.json`,
			goos: "windows",
			want: `C:\Users\User\AppData\Roaming\gemsub\config.json`,
		},
		{
			name: "posix path with spaces",
			in:   "/home/test user/.config/gemsub",
			goos: "linux",
			want: "'/home/test user/.config/gemsub'",
		},
		{
			name: "posix path with single quote (escaped as '\\'')'",
			in:   "/home/o'neil/config.json",
			goos: "linux",
			want: `'/home/o'\''neil/config.json'`,
		},
		{
			name: "windows path with spaces (PowerShell literal)",
			in:   `C:\Users\Test User\AppData\Roaming\gemsub`,
			goos: "windows",
			want: `'C:\Users\Test User\AppData\Roaming\gemsub'`,
		},
		{
			name: "windows path with dollar prevents PowerShell variable interpolation",
			in:   `C:\Users\$User\AppData\Roaming\gemsub`,
			goos: "windows",
			want: `'C:\Users\$User\AppData\Roaming\gemsub'`,
		},
		{
			name: "windows path with ampersand and parentheses prevents PowerShell operator evaluation",
			in:   `C:\Users\Alice & Bob (Work)\AppData\Roaming\gemsub`,
			goos: "windows",
			want: `'C:\Users\Alice & Bob (Work)\AppData\Roaming\gemsub'`,
		},
		{
			name: "windows path with percent sign",
			in:   `C:\Users\Dev%1\gemsub`,
			goos: "windows",
			want: `'C:\Users\Dev%1\gemsub'`,
		},
		{
			name: "windows path with exclamation and caret",
			in:   `C:\Users\User!^\gemsub`,
			goos: "windows",
			want: `'C:\Users\User!^\gemsub'`,
		},
		{
			name: "windows path with embedded single quote (PowerShell doubled quote escaping)",
			in:   `C:\Users\O'Neil\AppData\Roaming\gemsub`,
			goos: "windows",
			want: `'C:\Users\O''Neil\AppData\Roaming\gemsub'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paths.QuoteShellArgForGOOS(tt.in, tt.goos)
			if got != tt.want {
				t.Errorf("QuoteShellArgForGOOS(%q, %q) = %q, want %q", tt.in, tt.goos, got, tt.want)
			}
		})
	}
}

func TestHermetic_ConfigAndStateIndependence(t *testing.T) {
	home := "/home/alice"
	customCfg := "/custom/cfg_home"
	customState := "/custom/state_home"

	tests := []struct {
		name          string
		xdgConfig     string
		xdgState      string
		wantCfgDir    string
		wantCfgPath   string
		wantStateDir  string
		wantStatePath string
	}{
		{
			name:          "both custom absolute",
			xdgConfig:     customCfg,
			xdgState:      customState,
			wantCfgDir:    "/custom/cfg_home/gemsub",
			wantCfgPath:   "/custom/cfg_home/gemsub/config.json",
			wantStateDir:  "/custom/state_home/gemsub",
			wantStatePath: "/custom/state_home/gemsub/state.json",
		},
		{
			name:          "config custom, state unset (falls back to home)",
			xdgConfig:     customCfg,
			xdgState:      "",
			wantCfgDir:    "/custom/cfg_home/gemsub",
			wantCfgPath:   "/custom/cfg_home/gemsub/config.json",
			wantStateDir:  "/home/alice/.local/state/gemsub",
			wantStatePath: "/home/alice/.local/state/gemsub/state.json",
		},
		{
			name:          "state custom, config unset (falls back to home)",
			xdgConfig:     "",
			xdgState:      customState,
			wantCfgDir:    "/home/alice/.config/gemsub",
			wantCfgPath:   "/home/alice/.config/gemsub/config.json",
			wantStateDir:  "/custom/state_home/gemsub",
			wantStatePath: "/custom/state_home/gemsub/state.json",
		},
		{
			name:          "both unset (both fall back to home)",
			xdgConfig:     "",
			xdgState:      "",
			wantCfgDir:    "/home/alice/.config/gemsub",
			wantCfgPath:   "/home/alice/.config/gemsub/config.json",
			wantStateDir:  "/home/alice/.local/state/gemsub",
			wantStatePath: "/home/alice/.local/state/gemsub/state.json",
		},
		{
			name:          "config relative rejected, state custom accepted",
			xdgConfig:     "relative/config",
			xdgState:      customState,
			wantCfgDir:    "/home/alice/.config/gemsub",
			wantCfgPath:   "/home/alice/.config/gemsub/config.json",
			wantStateDir:  "/custom/state_home/gemsub",
			wantStatePath: "/custom/state_home/gemsub/state.json",
		},
		{
			name:          "state relative rejected, config custom accepted",
			xdgConfig:     customCfg,
			xdgState:      "relative/state",
			wantCfgDir:    "/custom/cfg_home/gemsub",
			wantCfgPath:   "/custom/cfg_home/gemsub/config.json",
			wantStateDir:  "/home/alice/.local/state/gemsub",
			wantStatePath: "/home/alice/.local/state/gemsub/state.json",
		},
		{
			name:          "both relative rejected (both fall back to home)",
			xdgConfig:     "relative/config",
			xdgState:      "relative/state",
			wantCfgDir:    "/home/alice/.config/gemsub",
			wantCfgPath:   "/home/alice/.config/gemsub/config.json",
			wantStateDir:  "/home/alice/.local/state/gemsub",
			wantStatePath: "/home/alice/.local/state/gemsub/state.json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lk := mockLookup("linux", home, tc.xdgConfig, tc.xdgState, "", "")

			cfgDir, err := paths.ConfigDirWithLookup(lk)
			if err != nil {
				t.Fatalf("ConfigDirWithLookup failed: %v", err)
			}
			if cfgDir != tc.wantCfgDir {
				t.Errorf("cfgDir = %q, want %q", cfgDir, tc.wantCfgDir)
			}

			cfgPath, err := paths.ConfigPathWithLookup("", lk)
			if err != nil {
				t.Fatalf("ConfigPathWithLookup failed: %v", err)
			}
			if cfgPath != tc.wantCfgPath {
				t.Errorf("cfgPath = %q, want %q", cfgPath, tc.wantCfgPath)
			}

			stateDir, err := paths.StateDirWithLookup(lk)
			if err != nil {
				t.Fatalf("StateDirWithLookup failed: %v", err)
			}
			if stateDir != tc.wantStateDir {
				t.Errorf("stateDir = %q, want %q", stateDir, tc.wantStateDir)
			}

			statePath, err := paths.StatePathWithLookup("", lk)
			if err != nil {
				t.Fatalf("StatePathWithLookup failed: %v", err)
			}
			if statePath != tc.wantStatePath {
				t.Errorf("statePath = %q, want %q", statePath, tc.wantStatePath)
			}
		})
	}
}

func TestHermetic_UserHomeDir_FailurePropagation(t *testing.T) {
	t.Run("UserHomeDir returns error", func(t *testing.T) {
		expectedErr := errors.New("simulated home lookup failure")
		lk := paths.Lookup{
			GOOS: "linux",
			UserHomeDir: func() (string, error) {
				return "", expectedErr
			},
		}

		if _, err := paths.ConfigDirWithLookup(lk); err == nil || !errors.Is(err, expectedErr) {
			t.Errorf("expected wrapped error %v, got %v", expectedErr, err)
		}
		if _, err := paths.ConfigPathWithLookup("", lk); err == nil || !errors.Is(err, expectedErr) {
			t.Errorf("expected wrapped error %v, got %v", expectedErr, err)
		}
		if _, err := paths.StateDirWithLookup(lk); err == nil || !errors.Is(err, expectedErr) {
			t.Errorf("expected wrapped error %v, got %v", expectedErr, err)
		}
		if _, err := paths.StatePathWithLookup("", lk); err == nil || !errors.Is(err, expectedErr) {
			t.Errorf("expected wrapped error %v, got %v", expectedErr, err)
		}
	})

	t.Run("UserHomeDir is nil", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS:        "linux",
			UserHomeDir: nil,
		}

		if _, err := paths.ConfigDirWithLookup(lk); err == nil {
			t.Error("expected error for nil UserHomeDir, got nil")
		}
		if _, err := paths.ConfigPathWithLookup("", lk); err == nil {
			t.Error("expected error for nil UserHomeDir, got nil")
		}
		if _, err := paths.StateDirWithLookup(lk); err == nil {
			t.Error("expected error for nil UserHomeDir, got nil")
		}
		if _, err := paths.StatePathWithLookup("", lk); err == nil {
			t.Error("expected error for nil UserHomeDir, got nil")
		}
	})

	t.Run("UserHomeDir returns empty string", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "linux",
			UserHomeDir: func() (string, error) {
				return "", nil
			},
		}

		if _, err := paths.ConfigDirWithLookup(lk); err == nil {
			t.Error("expected error for empty UserHomeDir, got nil")
		}
		if _, err := paths.ConfigPathWithLookup("", lk); err == nil {
			t.Error("expected error for empty UserHomeDir, got nil")
		}
		if _, err := paths.StateDirWithLookup(lk); err == nil {
			t.Error("expected error for empty UserHomeDir, got nil")
		}
		if _, err := paths.StatePathWithLookup("", lk); err == nil {
			t.Error("expected error for empty UserHomeDir, got nil")
		}
	})

	t.Run("UserHomeDir returns relative path", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "linux",
			UserHomeDir: func() (string, error) {
				return "relative/home", nil
			},
		}

		if _, err := paths.ConfigDirWithLookup(lk); err == nil {
			t.Error("expected error for relative UserHomeDir, got nil")
		}
		if _, err := paths.ConfigPathWithLookup("", lk); err == nil {
			t.Error("expected error for relative UserHomeDir, got nil")
		}
		if _, err := paths.StateDirWithLookup(lk); err == nil {
			t.Error("expected error for relative UserHomeDir, got nil")
		}
		if _, err := paths.StatePathWithLookup("", lk); err == nil {
			t.Error("expected error for relative UserHomeDir, got nil")
		}
	})
}

func TestHermetic_Windows_SimulatedLookups(t *testing.T) {
	appDataErr := errors.New("appdata failed")
	localAppDataErr := errors.New("localappdata failed")

	t.Run("UserConfigDir error propagation", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "windows",
			UserConfigDir: func() (string, error) {
				return "", appDataErr
			},
			UserCacheDir: func() (string, error) {
				return `C:\Users\Alice\AppData\Local`, nil
			},
		}

		if _, err := paths.ConfigDirWithLookup(lk); err == nil || !errors.Is(err, appDataErr) {
			t.Errorf("expected error %v, got %v", appDataErr, err)
		}
		if _, err := paths.ConfigPathWithLookup("", lk); err == nil || !errors.Is(err, appDataErr) {
			t.Errorf("expected error %v, got %v", appDataErr, err)
		}

		// State resolution must remain independent and succeed
		statePath, err := paths.StatePathWithLookup("", lk)
		if err != nil {
			t.Fatalf("expected state resolution to succeed independently: %v", err)
		}
		wantState := `C:\Users\Alice\AppData\Local\gemsub\state.json`
		if statePath != wantState {
			t.Errorf("got %q, want %q", statePath, wantState)
		}
	})

	t.Run("UserCacheDir error propagation", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "windows",
			UserConfigDir: func() (string, error) {
				return `C:\Users\Alice\AppData\Roaming`, nil
			},
			UserCacheDir: func() (string, error) {
				return "", localAppDataErr
			},
		}

		if _, err := paths.StateDirWithLookup(lk); err == nil || !errors.Is(err, localAppDataErr) {
			t.Errorf("expected error %v, got %v", localAppDataErr, err)
		}
		if _, err := paths.StatePathWithLookup("", lk); err == nil || !errors.Is(err, localAppDataErr) {
			t.Errorf("expected error %v, got %v", localAppDataErr, err)
		}

		// Config resolution must remain independent and succeed
		cfgPath, err := paths.ConfigPathWithLookup("", lk)
		if err != nil {
			t.Fatalf("expected config resolution to succeed independently: %v", err)
		}
		wantCfg := `C:\Users\Alice\AppData\Roaming\gemsub\config.json`
		if cfgPath != wantCfg {
			t.Errorf("got %q, want %q", cfgPath, wantCfg)
		}
	})

	t.Run("nil directory functions on windows return error", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "windows",
		}

		if _, err := paths.ConfigDirWithLookup(lk); err == nil {
			t.Error("expected error for nil UserConfigDir, got nil")
		}
		if _, err := paths.StateDirWithLookup(lk); err == nil {
			t.Error("expected error for nil UserCacheDir, got nil")
		}
	})

	t.Run("empty directory return on windows returns error", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "windows",
			UserConfigDir: func() (string, error) {
				return "", nil
			},
			UserCacheDir: func() (string, error) {
				return "", nil
			},
		}

		if _, err := paths.ConfigDirWithLookup(lk); err == nil {
			t.Error("expected error for empty UserConfigDir, got nil")
		}
		if _, err := paths.StateDirWithLookup(lk); err == nil {
			t.Error("expected error for empty UserCacheDir, got nil")
		}
	})

	t.Run("trailing slashes stripped cleanly", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "windows",
			UserConfigDir: func() (string, error) {
				return `C:\Users\Alice\AppData\Roaming\`, nil
			},
			UserCacheDir: func() (string, error) {
				return `C:\Users\Alice\AppData\Local/`, nil
			},
		}

		cfgPath, err := paths.ConfigPathWithLookup("", lk)
		if err != nil {
			t.Fatalf("ConfigPathWithLookup failed: %v", err)
		}
		wantCfg := `C:\Users\Alice\AppData\Roaming\gemsub\config.json`
		if cfgPath != wantCfg {
			t.Errorf("got %q, want %q", cfgPath, wantCfg)
		}

		statePath, err := paths.StatePathWithLookup("", lk)
		if err != nil {
			t.Fatalf("StatePathWithLookup failed: %v", err)
		}
		wantState := `C:\Users\Alice\AppData\Local\gemsub\state.json`
		if statePath != wantState {
			t.Errorf("got %q, want %q", statePath, wantState)
		}
	})
}

func TestHermetic_ExplicitOverrides_ImmuneToLookupFailures(t *testing.T) {
	// A completely broken Lookup where all functions are nil or return errors
	brokenLookup := paths.Lookup{
		GOOS: "linux",
		Getenv: func(string) string {
			return ""
		},
		UserHomeDir: func() (string, error) {
			return "", errors.New("home lookup broken")
		},
		UserConfigDir: func() (string, error) {
			return "", errors.New("config dir broken")
		},
		UserCacheDir: func() (string, error) {
			return "", errors.New("cache dir broken")
		},
	}

	overrides := []string{
		"./config.json",
		"./gemsub_state.json",
		"config.json",
		"state.json",
		"/tmp/custom_config.json",
		"/tmp/custom_state.json.gz",
		"../relative/config.json",
		`C:\Users\Alice\custom.json`,
	}

	for _, ov := range overrides {
		t.Run("Config_"+ov, func(t *testing.T) {
			got, err := paths.ConfigPathWithLookup(ov, brokenLookup)
			if err != nil {
				t.Fatalf("unexpected error for explicit override %q: %v", ov, err)
			}
			if got != ov {
				t.Errorf("expected %q, got %q", ov, got)
			}
		})

		t.Run("State_"+ov, func(t *testing.T) {
			got, err := paths.StatePathWithLookup(ov, brokenLookup)
			if err != nil {
				t.Fatalf("unexpected error for explicit override %q: %v", ov, err)
			}
			if got != ov {
				t.Errorf("expected %q, got %q", ov, got)
			}
		})
	}
}
