package config

// ReloadPolicy indicates how a configuration field update takes effect.
type ReloadPolicy string

const (
	// PolicyHotReloadable means the setting takes effect dynamically in running
	// subsystems without requiring an application restart.
	PolicyHotReloadable ReloadPolicy = "HOT_RELOADABLE"

	// PolicyRestartRequired means the setting cannot be safely rebound at runtime
	// and requires an application daemon restart to take effect.
	PolicyRestartRequired ReloadPolicy = "RESTART_REQUIRED"
)

// FieldPolicy maps configuration field names to their reload policies.
var FieldPolicy = map[string]ReloadPolicy{
	"sources":        PolicyHotReloadable,
	"fetch_interval": PolicyHotReloadable,
	"test":           PolicyHotReloadable,
	"publishing":     PolicyHotReloadable,
	"probe_limit":    PolicyHotReloadable,
	"flag_mode":      PolicyHotReloadable,
	"serve.format":   PolicyHotReloadable,

	"serve.listen": PolicyRestartRequired,
	"serve.path":   PolicyRestartRequired,
	"state_file":   PolicyRestartRequired,
	"headless":     PolicyRestartRequired,
}

// RequiresRestart compares old and new configurations and returns the list of
// canonical field paths that require a daemon or subsystem restart to take effect.
func RequiresRestart(old, new Config) []string {
	var fields []string
	if old.Serve.Listen != new.Serve.Listen {
		fields = append(fields, "serve.listen")
	}
	if old.Serve.Path != new.Serve.Path {
		fields = append(fields, "serve.path")
	}
	if old.StateFile != new.StateFile {
		fields = append(fields, "state_file")
	}
	if old.Headless != new.Headless {
		fields = append(fields, "headless")
	}
	return fields
}

// IsRestartRequired returns true if any modified field between old and new
// requires a daemon restart.
func IsRestartRequired(old, new Config) bool {
	return len(RequiresRestart(old, new)) > 0
}
