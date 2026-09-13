// Package paths provides centralized, platform-aware resolution for user-specific
// configuration and runtime state directories and file paths.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// AppName is the standard application directory name used across platforms.
	AppName = "gemsub"

	// ConfigFileName is the canonical user configuration filename.
	ConfigFileName = "config.json"

	// StateFileName is the canonical candidate state persistence filename.
	StateFileName = "state.json"
)

// Lookup abstracts environment lookups and standard directory queries.
// It allows hermetic testing of platform-specific and XDG behaviors without
// mutating process-wide environment variables.
type Lookup struct {
	Getenv        func(string) string
	UserConfigDir func() (string, error)
	UserCacheDir  func() (string, error)
	UserHomeDir   func() (string, error)
	GOOS          string
}

// DefaultLookup uses the host operating system and real environment.
var DefaultLookup = Lookup{
	Getenv:        os.Getenv,
	UserConfigDir: os.UserConfigDir,
	UserCacheDir:  os.UserCacheDir,
	UserHomeDir:   os.UserHomeDir,
	GOOS:          runtime.GOOS,
}

// ConfigPath returns the canonical configuration file path.
// If override is non-empty, it is returned verbatim without modification,
// preserving explicit caller control and relative paths.
func ConfigPath(override string) (string, error) {
	return ConfigPathWithLookup(override, DefaultLookup)
}

// ConfigPathWithLookup resolves the configuration file path using the provided Lookup.
func ConfigPathWithLookup(override string, lk Lookup) (string, error) {
	if override != "" {
		return override, nil
	}
	dir, err := ConfigDirWithLookup(lk)
	if err != nil {
		return "", err
	}
	return joinPath(lk.GOOS, dir, ConfigFileName), nil
}

// ConfigDir returns the canonical user configuration directory.
// On Linux/Unix: $XDG_CONFIG_HOME/gemsub, falling back to $HOME/.config/gemsub.
// On Windows: %AppData%\gemsub.
func ConfigDir() (string, error) {
	return ConfigDirWithLookup(DefaultLookup)
}

// ConfigDirWithLookup resolves the configuration directory using the provided Lookup.
func ConfigDirWithLookup(lk Lookup) (string, error) {
	if lk.GOOS == "windows" {
		if lk.UserConfigDir == nil {
			return "", errors.New("UserConfigDir function not configured")
		}
		appData, err := lk.UserConfigDir()
		if err != nil || appData == "" {
			if err != nil {
				return "", fmt.Errorf("resolve user config directory: %w", err)
			}
			return "", errors.New("user config directory is empty")
		}
		return joinPath(lk.GOOS, appData, AppName), nil
	}

	// Unix / Linux / Termux resolution
	if lk.Getenv != nil {
		if xdg := lk.Getenv("XDG_CONFIG_HOME"); xdg != "" && isAbsPath(xdg, lk.GOOS) {
			return joinPath(lk.GOOS, xdg, AppName), nil
		}
	}

	if lk.UserHomeDir == nil {
		return "", errors.New("UserHomeDir function not configured")
	}
	home, err := lk.UserHomeDir()
	if err != nil || home == "" || !isAbsPath(home, lk.GOOS) {
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		return "", errors.New("user home directory is empty or invalid")
	}

	return joinPath(lk.GOOS, home, ".config", AppName), nil
}

// StatePath returns the canonical runtime state file path.
// If override is non-empty, it is returned verbatim without modification,
// preserving explicit configuration overrides and relative paths.
func StatePath(override string) (string, error) {
	return StatePathWithLookup(override, DefaultLookup)
}

// StatePathWithLookup resolves the state file path using the provided Lookup.
func StatePathWithLookup(override string, lk Lookup) (string, error) {
	if override != "" {
		return override, nil
	}
	dir, err := StateDirWithLookup(lk)
	if err != nil {
		return "", err
	}
	return joinPath(lk.GOOS, dir, StateFileName), nil
}

// StateDir returns the canonical user runtime state directory.
// On Linux/Unix: $XDG_STATE_HOME/gemsub, falling back to $HOME/.local/state/gemsub.
// On Windows: %LocalAppData%\gemsub.
func StateDir() (string, error) {
	return StateDirWithLookup(DefaultLookup)
}

// StateDirWithLookup resolves the state directory using the provided Lookup.
func StateDirWithLookup(lk Lookup) (string, error) {
	if lk.GOOS == "windows" {
		if lk.UserCacheDir == nil {
			return "", errors.New("UserCacheDir function not configured")
		}
		localAppData, err := lk.UserCacheDir()
		if err != nil || localAppData == "" {
			if err != nil {
				return "", fmt.Errorf("resolve user state directory: %w", err)
			}
			return "", errors.New("user state directory is empty")
		}
		return joinPath(lk.GOOS, localAppData, AppName), nil
	}

	// Unix / Linux / Termux resolution
	if lk.Getenv != nil {
		if xdg := lk.Getenv("XDG_STATE_HOME"); xdg != "" && isAbsPath(xdg, lk.GOOS) {
			return joinPath(lk.GOOS, xdg, AppName), nil
		}
	}

	if lk.UserHomeDir == nil {
		return "", errors.New("UserHomeDir function not configured")
	}
	home, err := lk.UserHomeDir()
	if err != nil || home == "" || !isAbsPath(home, lk.GOOS) {
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		return "", errors.New("user home directory is empty or invalid")
	}

	return joinPath(lk.GOOS, home, ".local", "state", AppName), nil
}

// EnsureDir ensures that the parent directory of targetFilePath exists on disk,
// creating it with the specified permission mode if it does not exist.
func EnsureDir(targetFilePath string, perm os.FileMode) error {
	if targetFilePath == "" {
		return errors.New("target file path cannot be empty")
	}
	dir := filepath.Dir(targetFilePath)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, perm); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	return nil
}

func isAbsPath(p string, goos string) bool {
	if p == "" {
		return false
	}
	if goos == "windows" {
		if len(p) >= 3 && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
			return true
		}
		if len(p) >= 2 && (p[0] == '\\' || p[0] == '/') && (p[1] == '\\' || p[1] == '/') {
			return true
		}
		return false
	}
	return strings.HasPrefix(p, "/")
}

func joinPath(goos string, elem ...string) string {
	if goos == "windows" {
		cleaned := make([]string, 0, len(elem))
		for _, e := range elem {
			e = strings.TrimRight(e, "\\/")
			if e != "" {
				cleaned = append(cleaned, e)
			}
		}
		return strings.Join(cleaned, "\\")
	}
	return path.Join(elem...)
}
