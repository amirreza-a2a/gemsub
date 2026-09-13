package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gemsub/internal/config"
	"gemsub/internal/paths"
)

func TestResolveConfigPath_ImplicitDefault(t *testing.T) {
	t.Run("linux XDG_CONFIG_HOME set", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "linux",
			Getenv: func(key string) string {
				if key == "XDG_CONFIG_HOME" {
					return "/custom/xdg_config"
				}
				return ""
			},
			UserHomeDir: func() (string, error) {
				return "/home/testuser", nil
			},
		}

		got, err := resolveConfigPath("", false, lk)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Join("/custom/xdg_config", "gemsub", "config.json")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("linux XDG fallback to HOME", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "linux",
			Getenv: func(key string) string {
				return ""
			},
			UserHomeDir: func() (string, error) {
				return "/home/testuser", nil
			},
		}

		got, err := resolveConfigPath("", false, lk)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Join("/home/testuser", ".config", "gemsub", "config.json")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("windows AppData", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "windows",
			UserConfigDir: func() (string, error) {
				return `C:\Users\TestUser\AppData\Roaming`, nil
			},
		}

		got, err := resolveConfigPath("", false, lk)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := `C:\Users\TestUser\AppData\Roaming\gemsub\config.json`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("lookup error propagated", func(t *testing.T) {
		lk := paths.Lookup{
			GOOS: "linux",
			UserHomeDir: func() (string, error) {
				return "", errors.New("cannot determine home")
			},
		}

		_, err := resolveConfigPath("", false, lk)
		if err == nil {
			t.Fatal("expected error when lookup fails, got nil")
		}
	})
}

func TestResolveConfigPath_ExplicitPreserved(t *testing.T) {
	testCases := []struct {
		name     string
		override string
		want     string
	}{
		{
			name:     "relative CWD dot-slash",
			override: "./config.json",
			want:     "./config.json",
		},
		{
			name:     "relative custom path",
			override: "custom/dir/config.json",
			want:     "custom/dir/config.json",
		},
		{
			name:     "relative plain filename",
			override: "my-config.json",
			want:     "my-config.json",
		},
		{
			name:     "absolute path",
			override: "/tmp/gemsub-custom.json",
			want:     "/tmp/gemsub-custom.json",
		},
		{
			name:     "explicit path identical to canonical default",
			override: "/home/testuser/.config/gemsub/config.json",
			want:     "/home/testuser/.config/gemsub/config.json",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveConfigPath(tc.override, true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("explicit empty string rejected", func(t *testing.T) {
		_, err := resolveConfigPath("", true)
		if err == nil {
			t.Fatal("expected error for explicit empty config path, got nil")
		}
	})

	t.Run("explicit whitespace string rejected", func(t *testing.T) {
		_, err := resolveConfigPath("   ", true)
		if err == nil {
			t.Fatal("expected error for explicit whitespace config path, got nil")
		}
	})
}

func TestDetermineConfigPath_FlagSet(t *testing.T) {
	lk := paths.Lookup{
		GOOS: "linux",
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return "/xdg/conf"
			}
			return ""
		},
		UserHomeDir: func() (string, error) {
			return "/home/testuser", nil
		},
	}

	t.Run("omitted flag uses default canonical path", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		cfgFlag := fs.String("config", "", "path to config file")
		_ = fs.Bool("headless", false, "run headless")
		if err := fs.Parse([]string{"--headless"}); err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		resolved, isExplicit, err := determineConfigPath(fs, *cfgFlag, lk)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if isExplicit {
			t.Error("expected isExplicit == false when -config flag omitted")
		}
		want := filepath.Join("/xdg/conf", "gemsub", "config.json")
		if resolved != want {
			t.Errorf("got %q, want %q", resolved, want)
		}
	})

	t.Run("explicit flag overrides default", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		cfgFlag := fs.String("config", "", "path to config file")
		if err := fs.Parse([]string{"-config", "./my-config.json"}); err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		resolved, isExplicit, err := determineConfigPath(fs, *cfgFlag, lk)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !isExplicit {
			t.Error("expected isExplicit == true when -config flag provided")
		}
		if resolved != "./my-config.json" {
			t.Errorf("got %q, want %q", resolved, "./my-config.json")
		}
	})

	t.Run("explicit empty flag fails", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		cfgFlag := fs.String("config", "", "path to config file")
		if err := fs.Parse([]string{"-config", ""}); err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		_, _, err := determineConfigPath(fs, *cfgFlag, lk)
		if err == nil {
			t.Fatal("expected error for explicit empty flag, got nil")
		}
	})
}

func TestFormatMissingConfigHeadlessHelp(t *testing.T) {
	t.Run("relative path in cwd remains unchanged", func(t *testing.T) {
		msg := formatMissingConfigHeadlessHelp("./config.json")
		if !strings.Contains(msg, "Configuration file not found: ./config.json") {
			t.Errorf("unexpected message header: %s", msg)
		}
		if !strings.Contains(msg, "cp config.example.json ./config.json") {
			t.Errorf("expected plain cp advice, got: %s", msg)
		}
		if strings.Contains(msg, "mkdir -p") {
			t.Errorf("unexpected mkdir -p in cwd advice: %s", msg)
		}
	})

	t.Run("normal canonical path remains readable without quotes", func(t *testing.T) {
		canonicalPath := "/home/user/.config/gemsub/config.json"
		msg := formatMissingConfigHeadlessHelp(canonicalPath)
		if !strings.Contains(msg, "Configuration file not found: "+canonicalPath) {
			t.Errorf("unexpected message header: %s", msg)
		}
		expectedAdvice := "mkdir -p /home/user/.config/gemsub && cp config.example.json /home/user/.config/gemsub/config.json"
		if !strings.Contains(msg, expectedAdvice) {
			t.Errorf("expected advice %q, got:\n%s", expectedAdvice, msg)
		}
	})

	t.Run("linux canonical path containing spaces is quoted", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("skipping Linux path assertion on Windows host")
		}
		path := "/home/test user/.config/gemsub/config.json"
		msg := formatMissingConfigHeadlessHelp(path)
		expectedAdvice := "mkdir -p '/home/test user/.config/gemsub' && cp config.example.json '/home/test user/.config/gemsub/config.json'"
		if !strings.Contains(msg, expectedAdvice) {
			t.Errorf("expected advice %q, got:\n%s", expectedAdvice, msg)
		}
	})

	t.Run("nested path containing spaces is quoted", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("skipping POSIX path assertion on Windows host")
		}
		path := "./my configs/dev/config.json"
		msg := formatMissingConfigHeadlessHelp(path)
		expectedAdvice := "mkdir -p 'my configs/dev' && cp config.example.json './my configs/dev/config.json'"
		if !strings.Contains(msg, expectedAdvice) {
			t.Errorf("expected advice %q, got:\n%s", expectedAdvice, msg)
		}
	})

	t.Run("windows style path where appropriate", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("skipping native Windows path format assertion on non-Windows host")
		}
		path := `C:\Users\Test User\AppData\Roaming\gemsub\config.json`
		msg := formatMissingConfigHeadlessHelp(path)
		expectedAdvice := `mkdir -p "C:\Users\Test User\AppData\Roaming\gemsub" && cp config.example.json "C:\Users\Test User\AppData\Roaming\gemsub\config.json"`
		if !strings.Contains(msg, expectedAdvice) {
			t.Errorf("expected advice %q, got:\n%s", expectedAdvice, msg)
		}
	})
}

func TestQuoteShellArg(t *testing.T) {
	t.Run("clean paths returned unquoted", func(t *testing.T) {
		cases := []string{
			"./config.json",
			"config.json",
			"/etc/gemsub/config.json",
			"/home/user/.config/gemsub/config.json",
			"sub_dir-1/file.json",
		}
		for _, c := range cases {
			got := quoteShellArg(c)
			if got != c {
				t.Errorf("quoteShellArg(%q) = %q; want %q", c, got, c)
			}
		}
	})

	t.Run("paths with spaces quoted", func(t *testing.T) {
		in := "/path with spaces/config.json"
		got := quoteShellArg(in)
		if runtime.GOOS == "windows" {
			want := `"` + in + `"`
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		} else {
			want := `'` + in + `'`
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		}
	})
}

func TestBootstrapConfig_CanonicalDefaultOnboardingCreatesParentDir(t *testing.T) {
	tmpDir := t.TempDir()
	nestedConfigPath := filepath.Join(tmpDir, ".config", "gemsub", "config.json")

	svc, isFirstRun, err := bootstrapConfig(nestedConfigPath, false)
	if err != nil {
		t.Fatalf("bootstrapConfig failed: %v", err)
	}
	if !isFirstRun {
		t.Fatal("expected isFirstRun == true for non-existent canonical path")
	}
	if svc == nil {
		t.Fatal("expected non-nil config.Service")
	}

	// Verify parent directory does not exist yet
	parentDir := filepath.Dir(nestedConfigPath)
	if _, err := os.Stat(parentDir); !os.IsNotExist(err) {
		t.Fatalf("expected parent dir not to exist initially, got err: %v", err)
	}

	// Simulate onboarding save through service
	err = svc.Update(func(c *config.Config) error {
		c.Sources = config.NewSources("https://example.com/sub.txt")
		return nil
	})
	if err != nil {
		t.Fatalf("svc.Update failed: %v", err)
	}

	// Verify parent directory was created and config file exists
	fi, err := os.Stat(nestedConfigPath)
	if err != nil {
		t.Fatalf("expected config file on disk: %v", err)
	}
	if fi.Size() == 0 {
		t.Error("expected non-empty config file")
	}
	if svc.IsFirstRun() {
		t.Error("expected isFirstRun == false after save")
	}
}

func TestBootstrapConfig_MissingCanonicalHeadless(t *testing.T) {
	tmpDir := t.TempDir()
	canonicalPath := filepath.Join(tmpDir, ".config", "gemsub", "config.json")

	svc, isFirstRun, err := bootstrapConfig(canonicalPath, true)
	if err == nil {
		t.Fatal("expected error in headless mode for missing config, got nil")
	}
	if !errors.Is(err, ErrMissingConfigHeadless) {
		t.Errorf("expected ErrMissingConfigHeadless, got: %v", err)
	}
	if !strings.Contains(err.Error(), canonicalPath) {
		t.Errorf("expected error to mention canonical path %q, got %q", canonicalPath, err.Error())
	}
	if isFirstRun {
		t.Error("expected isFirstRun == false")
	}
	if svc != nil {
		t.Errorf("expected nil service, got %v", svc)
	}
}
