package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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

// LegacyConfigFileName is the name of the legacy configuration file.
const LegacyConfigFileName = "config.json"

type statFunc func(string) (os.FileInfo, error)

// detectLegacyConfig reports whether a legacy config.json file exists in dir (default ".").
// It checks file existence and ensures it is not a directory. It NEVER reads or parses the content.
func detectLegacyConfig(dir ...string) bool {
	return detectLegacyConfigWithStat(os.Stat, dir...)
}

func detectLegacyConfigWithStat(stat statFunc, dir ...string) bool {
	targetDir := "."
	if len(dir) > 0 && dir[0] != "" {
		targetDir = dir[0]
	}
	targetPath := filepath.Join(targetDir, LegacyConfigFileName)
	fi, err := stat(targetPath)
	if err != nil {
		return false
	}
	return !fi.IsDir()
}

// formatMissingConfigHeadlessHelp formats user-facing guidance when configuration
// is missing in headless mode. If legacyDetected is true, it extends the output
// with explicit guidance on how to run with or migrate the detected legacy configuration.
func formatMissingConfigHeadlessHelp(configPath string, legacyDetected ...bool) string {
	dir := filepath.Dir(configPath)
	var copyAdvice string
	if dir != "" && dir != "." {
		copyAdvice = fmt.Sprintf("mkdir -p %s && cp config.example.json %s", quoteShellArg(dir), quoteShellArg(configPath))
	} else {
		copyAdvice = fmt.Sprintf("cp config.example.json %s", quoteShellArg(configPath))
	}

	hasLegacy := len(legacyDetected) > 0 && legacyDetected[0]
	if !hasLegacy {
		return fmt.Sprintf("Configuration file not found: %s\n\n"+
			"To configure gemsub:\n"+
			"  1. Run gemsub interactively without --headless to launch the onboarding wizard: gemsub\n"+
			"  2. Or create %s manually by copying config.example.json:\n"+
			"     %s\n",
			configPath, configPath, copyAdvice)
	}

	var migrationAdvice string
	if dir != "" && dir != "." {
		migrationAdvice = fmt.Sprintf("mkdir -p %s && cp ./config.json %s", quoteShellArg(dir), quoteShellArg(configPath))
	} else {
		migrationAdvice = fmt.Sprintf("cp ./config.json %s", quoteShellArg(configPath))
	}

	return fmt.Sprintf("Configuration file not found: %s\n\n"+
		"A legacy configuration was found at:\n"+
		"  ./config.json\n\n"+
		"This file is not loaded automatically.\n\n"+
		"To use it explicitly:\n"+
		"  gemsub --headless -config ./config.json\n\n"+
		"To migrate it manually:\n"+
		"  %s\n\n"+
		"Or to configure a new setup:\n"+
		"  1. Run gemsub interactively without --headless to launch the onboarding wizard: gemsub\n"+
		"  2. Or create %s manually by copying config.example.json:\n"+
		"     %s\n",
		configPath, migrationAdvice, configPath, copyAdvice)
}

// quoteShellArg quotes a file or directory path for safe copy-pasting in a shell
// if it contains spaces or other shell metacharacters. If the path contains only
// safe path characters, it is returned unquoted to preserve readability.
func quoteShellArg(s string) string {
	return paths.QuoteShellArg(s)
}
