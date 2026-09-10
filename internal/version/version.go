// Package version provides application version, git commit, and build timestamp metadata.
// The variables are linker-injected during release builds.
package version

import (
	"fmt"
	"strings"
	"time"
)

// AppName is the binary application name.
const AppName = "gemsub"

// Linker-injected build metadata. Defaults represent development builds.
var (
	// Version is the semver release tag (e.g. "v0.1.0" or "0.1.0") or "dev".
	Version = "dev"
	// Commit is the git commit SHA or "none".
	Commit = "none"
	// Date is the UTC build timestamp formatted as RFC3339 or "unknown".
	Date = "unknown"
)

// Info returns the canonical formatted multi-line version string.
//
// Example shape:
// gemsub v0.1.0
// commit: 1a2b3c4d
// built: 2026-09-10T15:00:00Z
func Info() string {
	return formatInfo(AppName, Version, Commit, Date)
}

// formatInfo formats the version metadata according to repository standards.
func formatInfo(name, ver, commit, date string) string {
	v := ver
	if v == "" {
		v = "dev"
	}
	if v != "dev" && !strings.HasPrefix(v, "v") {
		v = "v" + v
	}

	c := commit
	if c == "" {
		c = "none"
	}

	d := date
	if d == "" {
		d = "unknown"
	} else if parsed, err := time.Parse(time.RFC3339, d); err == nil {
		d = parsed.UTC().Format(time.RFC3339)
	}

	return fmt.Sprintf("%s %s\ncommit: %s\nbuilt: %s", name, v, c, d)
}
