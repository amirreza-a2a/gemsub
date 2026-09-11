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

// ConfigCategoryViewModel contains the display items for a single category tab.
type ConfigCategoryViewModel struct {
	Category ConfigCategory
	Name     string
	Items    []ConfigItemViewModel
}

// ConfigCenterViewModel bundles all category view data for the Configuration Center.
type ConfigCenterViewModel struct {
	Categories []ConfigCategoryViewModel
}
