package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"gemsub/internal/paths"
)

// resolveConfigPath resolves the configuration file path.
// If isExplicit is true, override is validated and returned verbatim without
// modifying relative paths or resolving via XDG.
// If isExplicit is false, the canonical user configuration path is resolved.
func resolveConfigPath(override string, isExplicit bool, lk ...paths.Lookup) (string, error) {
	if isExplicit {
		if strings.TrimSpace(override) == "" {
			return "", errors.New("explicit config path cannot be empty")
		}
		return override, nil
	}

	lookup := paths.DefaultLookup
	if len(lk) > 0 {
		lookup = lk[0]
	}

	path, err := paths.ConfigPathWithLookup("", lookup)
	if err != nil {
		return "", fmt.Errorf("resolve default config path: %w", err)
	}
	return path, nil
}

// determineConfigPath inspects the parsed flag set to check if the -config flag
// was explicitly provided by the user. If omitted, the default canonical path is used.
func determineConfigPath(fs *flag.FlagSet, configFlagValue string, lk ...paths.Lookup) (string, bool, error) {
	var isExplicit bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			isExplicit = true
		}
	})

	resolved, err := resolveConfigPath(configFlagValue, isExplicit, lk...)
	if err != nil {
		return "", isExplicit, err
	}
	return resolved, isExplicit, nil
}

// formatMissingConfigHeadlessHelp formats user-facing guidance when configuration
// is missing in headless mode.
func formatMissingConfigHeadlessHelp(configPath string) string {
	dir := filepath.Dir(configPath)
	var copyAdvice string
	if dir != "" && dir != "." {
		copyAdvice = fmt.Sprintf("mkdir -p %s && cp config.example.json %s", quoteShellArg(dir), quoteShellArg(configPath))
	} else {
		copyAdvice = fmt.Sprintf("cp config.example.json %s", quoteShellArg(configPath))
	}

	return fmt.Sprintf("Configuration file not found: %s\n\n"+
		"To configure gemsub:\n"+
		"  1. Run gemsub interactively without --headless to launch the onboarding wizard: gemsub\n"+
		"  2. Or create %s manually by copying config.example.json:\n"+
		"     %s\n",
		configPath, configPath, copyAdvice)
}

// quoteShellArg quotes a file or directory path for safe copy-pasting in a shell
// if it contains spaces or other shell metacharacters. If the path contains only
// safe path characters, it is returned unquoted to preserve readability.
func quoteShellArg(s string) string {
	if s == "" {
		if runtime.GOOS == "windows" {
			return `""`
		}
		return "''"
	}

	isSafe := true
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '/' || r == '\\' || r == '.' || r == '_' || r == '-' || r == ':' || r == '@' {
			continue
		}
		isSafe = false
		break
	}
	if isSafe {
		return s
	}

	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
