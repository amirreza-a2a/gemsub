package viewmodel

import "time"

// ConfigCategory identifies a configuration section in the Config Center.
type ConfigCategory int

const (
	CategoryGeneral    ConfigCategory = iota // Server, Flag mode
	CategorySources                          // Subscription sources
	CategoryTesting                          // Timeout, Concurrency, Retries
	CategoryGemini                           // URL, Block phrases
	CategoryScheduler                        // Interval, Limits, Pause/Resume
	CategoryPublishing                       // Git publishing options & diagnostics
)

// ConfigCategoryCount is the total number of configuration categories.
const ConfigCategoryCount = 6

// CategoryName returns the display name for a category.
func (c ConfigCategory) Name() string {
	switch c {
	case CategoryGeneral:
		return "General"
	case CategorySources:
		return "Sources"
	case CategoryTesting:
		return "Testing"
	case CategoryGemini:
		return "Gemini"
	case CategoryScheduler:
		return "Scheduler"
	case CategoryPublishing:
		return "Publishing"
	default:
		return "Unknown"
	}
}

// SettingType defines the value type and editing behavior for a configuration item.
type SettingType int

const (
	SettingTypeReadOnly SettingType = iota
	SettingTypeString
	SettingTypeBool
	SettingTypeInt
	SettingTypeDuration
	SettingTypeEnum
	SettingTypeURL
)

// ConfigItemViewModel represents a single key/value setting row
// within a category in the Config Center.
type ConfigItemViewModel struct {
	Key             string      // Canonical setting key (e.g. "serve.listen", "test.concurrency")
	Label           string      // Display label, e.g. "Listen Address"
	Value           string      // Formatted display value, e.g. ":8080"
	EditorValue     string      // Safe initial value for editor modal (credentials masked or omitted)
	RawValue        string      // Exact unformatted value for non-secret fields (empty for secret-bearing)
	HasSecret       bool        // True if setting contains sensitive credentials masked from presentation
	Type            SettingType // Value type for editing behavior
	Editable        bool        // True if setting is user-editable
	RestartRequired bool        // True if changing this setting requires application restart
	PendingRestart  bool        // True if setting changed since startup and requires restart
	EnumOptions     []string    // Allowed values if Type is SettingTypeEnum
	Description     string      // Optional constraint or description hint
}

// SourceItemViewModel represents a single subscription source item in the Config Center.
type SourceItemViewModel struct {
	ID             string // Unique deterministic source ID
	Name           string // Display name or alias
	URL            string // Sanitized URL (credentials masked)
	Enabled        bool   // Active state
	CandidateCount int    // Contributed from latest fetch telemetry (if known)
	HasCount       bool   // True if candidate count telemetry is available
	StatusMsg      string // Last fetch status or error message (if known)
}

// SchedulerViewModel represents presentation telemetry and status for the Scheduler pane.
type SchedulerViewModel struct {
	State             string    // "IDLE", "RUNNING", "PAUSED"
	CycleActive       bool      // True if probe cycle is actively in flight
	NextCycleEstimate time.Time // Zero if paused, stopped, or not scheduled
	NextCycleText     string    // e.g. "in 8m 32s (14:35:00)", "— (paused)", "—"
	LastCycleStart    time.Time // Zero if no cycles have run
	LastCycleEnd      time.Time
	LastDuration      time.Duration
	LastDurationText  string // Formatted duration or "—"
	CompletedCycles   int    // Total completed test cycles
	FetchInterval     string // Formatted interval (e.g. "10m")
	ProbeLimit        string // Formatted probe limit (e.g. "unlimited" or "50 candidates")
}

// ConfigCategoryViewModel contains the display items for a single category tab.
type ConfigCategoryViewModel struct {
	Category  ConfigCategory
	Name      string
	Items     []ConfigItemViewModel
	Sources   []SourceItemViewModel
	Scheduler SchedulerViewModel
}

// ConfigCenterViewModel bundles all category view data for the Configuration Center.
type ConfigCenterViewModel struct {
	Categories []ConfigCategoryViewModel
}

// Sources returns the slice of SourceItemViewModels if the Sources category exists.
func (c ConfigCenterViewModel) Sources() []SourceItemViewModel {
	for _, cat := range c.Categories {
		if cat.Category == CategorySources {
			return cat.Sources
		}
	}
	return nil
}

// Scheduler returns the SchedulerViewModel if the Scheduler category exists.
func (c ConfigCenterViewModel) Scheduler() SchedulerViewModel {
	for _, cat := range c.Categories {
		if cat.Category == CategoryScheduler {
			return cat.Scheduler
		}
	}
	return SchedulerViewModel{}
}
