package viewmodel

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

// ConfigItemViewModel represents a single read-only key/value setting row
// within a category in the Config Center.
type ConfigItemViewModel struct {
	Label string // Display label, e.g. "Listen Address"
	Value string // Formatted display value, e.g. ":8080"
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

// ConfigCategoryViewModel contains the display items for a single category tab.
type ConfigCategoryViewModel struct {
	Category ConfigCategory
	Name     string
	Items    []ConfigItemViewModel
	Sources  []SourceItemViewModel
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
