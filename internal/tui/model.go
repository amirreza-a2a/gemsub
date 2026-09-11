package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"gemsub/internal/tui/viewmodel"
)

// Controller represents the presentation query and command boundary for Bubble Tea.
// Bubble Tea operates exclusively on presentation ViewModels and opaque identifiers,
// without performing application-state projection or retaining references to internal
// adapters or stores.
type Controller interface {
	Snapshot(filter viewmodel.FilterMode) viewmodel.SnapshotViewModel
	PollSnapshot(filter viewmodel.FilterMode) (viewmodel.SnapshotViewModel, bool)
	CandidateDetail(opaqueID string) (viewmodel.CandidateDetailViewModel, bool)
	CycleLogLevel() (viewmodel.LogViewModel, string)
	CopyCandidateLink(opaqueID string) error
	CandidateRowsWindow(filter viewmodel.FilterMode, offset, limit int) []viewmodel.CandidateRowViewModel
	ConfigCenter() viewmodel.ConfigCenterViewModel
	ToggleSource(id string) error
	AddSource(rawURL string, name string) error
	UpdateSource(id string, rawURL string, name string) error
	DeleteSource(id string) error
}

// ActiveView represents the primary content pane currently displayed.
type ActiveView int

const (
	ViewCandidates ActiveView = iota
	ViewLogs
	ViewConfig
)

// TickMsg represents a periodic background refresh tick (bounded at 10 Hz / 100 ms).
type TickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// Model implements tea.Model for the Gemsub Terminal UI MVP.
// It is strictly decoupled from internal Store/Scheduler structs and operates
// exclusively on presentation ViewModels produced by the presentation Controller.
type Model struct {
	ctrl Controller

	// ViewModels
	header viewmodel.HeaderViewModel
	rows   []viewmodel.CandidateRowViewModel
	detail viewmodel.CandidateDetailViewModel
	logs   viewmodel.LogViewModel

	// Virtualized table state
	totalRows         int
	windowOffset      int
	detailCandidateID string

	// View state
	activeView       ActiveView
	filterMode       viewmodel.FilterMode
	cursorIndex      int
	tableOffset      int
	showDetail       bool
	statusMessage    string
	statusExpiry     time.Time
	width            int
	height           int
	terminalTooSmall bool

	// Log view scrolling
	logScrollOffset int
	logFollow       bool

	// Config Center state
	prevActiveView  ActiveView
	configCenter    viewmodel.ConfigCenterViewModel
	configCategory  viewmodel.ConfigCategory
	configItemIndex int

	// Source Manager state
	sourceMode        sourceInputMode
	confirmDeleteID   string
	confirmDeleteName string
	editSourceID      string
	inputURL          string
	inputName         string
	inputFocus        int // 0 = URL, 1 = Name
}

type sourceInputMode int

const (
	sourceModeNormal sourceInputMode = iota
	sourceModeDeleteConfirm
	sourceModeAdd
	sourceModeEdit
)

// New creates and pre-hydrates a new Bubble Tea Model using the presentation Controller.
func New(ctrl Controller) *Model {
	m := &Model{
		ctrl:       ctrl,
		activeView: ViewCandidates,
		width:      80,
		height:     24,
		logFollow:  true,
	}
	if ctrl != nil {
		m.applySnapshot(ctrl.Snapshot(viewmodel.FilterAll), true)
	}
	return m
}

// Init starts the periodic background refresh tick (bounded at 10 Hz / 100 ms).
// Interactive navigation, keystrokes, and resize events continue to render immediately.
func (m *Model) Init() tea.Cmd {
	return tickCmd()
}

// Update handles incoming Bubble Tea messages and keyboard events.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.terminalTooSmall = (m.width < 80 || m.height < 24)
		m.adjustScroll()
		m.refreshVisibleRows()
		return m, nil

	case TickMsg:
		// Background Store/ViewModel refreshes are throttled to a maximum 10 Hz rate (100 ms interval).
		// When dirty == false, no Store snapshot or ViewModel rebuild is performed.
		// Interactive navigation (keys, resizes) responds and renders immediately without frame drops.
		if m.ctrl != nil {
			if snap, ok := m.ctrl.PollSnapshot(m.filterMode); ok {
				m.applySnapshot(snap, false)
				if m.activeView == ViewConfig {
					m.refreshConfigCenter()
				}
			}
		}
		// Clear expired status messages
		if m.statusMessage != "" && time.Now().After(m.statusExpiry) {
			m.statusMessage = ""
		}
		return m, tickCmd()
	}

	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// In Config Center Sources category, active input modes capture keys (q, esc, etc.)
	if m.activeView == ViewConfig && m.configCategory == viewmodel.CategorySources && m.sourceMode != sourceModeNormal {
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.handleConfigKeys(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "c":
		// Enter Config Center from Candidates or Logs (not from detail)
		if !m.showDetail && m.activeView != ViewConfig {
			m.prevActiveView = m.activeView
			m.activeView = ViewConfig
			m.configItemIndex = 0
			m.sourceMode = sourceModeNormal
			m.refreshConfigCenter()
			return m, nil
		}

	case "esc":
		// From Config Center, return to previous view
		if m.activeView == ViewConfig {
			m.activeView = m.prevActiveView
			m.sourceMode = sourceModeNormal
			return m, nil
		}
	}

	// Config Center has its own key handling; do not process global keys
	if m.activeView == ViewConfig {
		return m.handleConfigKeys(msg)
	}

	switch msg.String() {
	case "tab":
		if m.showDetail {
			m.showDetail = false
		}
		if m.activeView == ViewCandidates {
			m.activeView = ViewLogs
		} else {
			m.activeView = ViewCandidates
		}
		return m, nil

	case "s":
		switch m.filterMode {
		case viewmodel.FilterAll:
			m.filterMode = viewmodel.FilterGemini
			m.setStatus("Filter: Gemini servable")
		case viewmodel.FilterGemini:
			m.filterMode = viewmodel.FilterGeneric
			m.setStatus("Filter: Generic servable")
		case viewmodel.FilterGeneric:
			m.filterMode = viewmodel.FilterAll
			m.setStatus("Filter: All candidates")
		default:
			m.filterMode = viewmodel.FilterAll
			m.setStatus("Filter: All candidates")
		}
		if m.ctrl != nil {
			m.applySnapshot(m.ctrl.Snapshot(m.filterMode), true)
		}
		return m, nil

	case "l":
		if m.ctrl != nil {
			logs, lvl := m.ctrl.CycleLogLevel()
			m.logs = logs
			m.setStatus(fmt.Sprintf("Log level: %s", lvl))
		}
		return m, nil
	}

	if m.showDetail {
		return m.handleDetailKeys(msg)
	}

	switch m.activeView {
	case ViewCandidates:
		return m.handleCandidateKeys(msg)
	case ViewLogs:
		return m.handleLogKeys(msg)
	}

	return m, nil
}

func (m *Model) handleCandidateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	prevOffset := m.tableOffset
	switch msg.String() {
	case "up", "k":
		if m.cursorIndex > 0 {
			m.cursorIndex--
			m.adjustScroll()
		}
	case "down", "j":
		if m.cursorIndex < m.totalRows-1 {
			m.cursorIndex++
			m.adjustScroll()
		}
	case "pgup", "ctrl+b":
		page := m.visibleTableRows()
		m.cursorIndex -= page
		if m.cursorIndex < 0 {
			m.cursorIndex = 0
		}
		m.adjustScroll()
	case "pgdown", "ctrl+f":
		page := m.visibleTableRows()
		m.cursorIndex += page
		if m.cursorIndex >= m.totalRows {
			m.cursorIndex = m.totalRows - 1
		}
		if m.cursorIndex < 0 {
			m.cursorIndex = 0
		}
		m.adjustScroll()
	case "g":
		m.cursorIndex = 0
		m.adjustScroll()
	case "G":
		if m.totalRows > 0 {
			m.cursorIndex = m.totalRows - 1
			m.adjustScroll()
		}
	case "enter", " ":
		selectedID := m.selectedCandidateID()
		if selectedID != "" && m.ctrl != nil {
			if detail, ok := m.ctrl.CandidateDetail(selectedID); ok {
				m.detail = detail
				m.showDetail = true
				m.detailCandidateID = selectedID
			}
		}
	case "y":
		selectedID := m.selectedCandidateID()
		if selectedID != "" && m.ctrl != nil {
			if err := m.ctrl.CopyCandidateLink(selectedID); err == nil {
				m.setStatus("Copied active link to clipboard")
			} else {
				m.setStatus("Clipboard unavailable: could not copy")
			}
		}
	}
	if m.tableOffset != prevOffset {
		m.refreshVisibleRows()
	}
	return m, nil
}

func (m *Model) handleDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter", " ":
		m.showDetail = false
		m.detailCandidateID = ""
	case "y":
		if m.ctrl != nil {
			if err := m.ctrl.CopyCandidateLink(m.detail.ID); err == nil {
				m.setStatus("Copied active link to clipboard")
			} else {
				m.setStatus("Clipboard unavailable: could not copy")
			}
		}
	}
	return m, nil
}

func (m *Model) handleLogKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	totalLines := len(m.logs.Lines)
	visLines := m.visibleLogRows()

	switch msg.String() {
	case "up", "k":
		m.logFollow = false
		if m.logScrollOffset > 0 {
			m.logScrollOffset--
		}
	case "down", "j":
		maxOffset := totalLines - visLines
		if maxOffset < 0 {
			maxOffset = 0
		}
		if m.logScrollOffset < maxOffset {
			m.logScrollOffset++
		}
		if m.logScrollOffset >= maxOffset {
			m.logFollow = true
		}
	case "pgup", "ctrl+b":
		m.logFollow = false
		m.logScrollOffset -= visLines
		if m.logScrollOffset < 0 {
			m.logScrollOffset = 0
		}
	case "pgdown", "ctrl+f":
		maxOffset := totalLines - visLines
		if maxOffset < 0 {
			maxOffset = 0
		}
		m.logScrollOffset += visLines
		if m.logScrollOffset >= maxOffset {
			m.logScrollOffset = maxOffset
			m.logFollow = true
		}
	case "g":
		m.logFollow = false
		m.logScrollOffset = 0
	case "G":
		m.logFollow = true
		maxOffset := totalLines - visLines
		if maxOffset < 0 {
			maxOffset = 0
		}
		m.logScrollOffset = maxOffset
	}
	return m, nil
}

func (m *Model) handleConfigKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.configCategory == viewmodel.CategorySources {
		return m.handleSourceKeys(msg)
	}

	catCount := viewmodel.ConfigCategoryCount
	switch msg.String() {
	case "tab", "l", "right":
		m.configCategory = (m.configCategory + 1) % viewmodel.ConfigCategory(catCount)
		m.configItemIndex = 0
	case "shift+tab", "h", "left":
		if m.configCategory == 0 {
			m.configCategory = viewmodel.ConfigCategory(catCount - 1)
		} else {
			m.configCategory--
		}
		m.configItemIndex = 0
	case "j", "down":
		items := m.configCategoryItems()
		if m.configItemIndex < len(items)-1 {
			m.configItemIndex++
		}
	case "k", "up":
		if m.configItemIndex > 0 {
			m.configItemIndex--
		}
	case "g":
		m.configItemIndex = 0
	case "G":
		items := m.configCategoryItems()
		if len(items) > 0 {
			m.configItemIndex = len(items) - 1
		}
	}
	return m, nil
}

func (m *Model) handleSourceKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	catCount := viewmodel.ConfigCategoryCount

	// 1. Delete confirmation mode
	if m.sourceMode == sourceModeDeleteConfirm {
		switch msg.String() {
		case "y", "Y":
			if m.ctrl != nil && m.confirmDeleteID != "" {
				if err := m.ctrl.DeleteSource(m.confirmDeleteID); err != nil {
					m.setStatus(fmt.Sprintf("Error: %s", err))
				} else {
					m.setStatus(fmt.Sprintf("Deleted source: %s", m.confirmDeleteName))
					m.refreshConfigCenter()
					sources := m.configCategorySources()
					if m.configItemIndex >= len(sources) && len(sources) > 0 {
						m.configItemIndex = len(sources) - 1
					}
				}
			}
			m.sourceMode = sourceModeNormal
			m.confirmDeleteID = ""
			m.confirmDeleteName = ""
			return m, nil
		default:
			m.sourceMode = sourceModeNormal
			m.confirmDeleteID = ""
			m.confirmDeleteName = ""
			m.setStatus("Deletion cancelled")
			return m, nil
		}
	}

	// 2. Add or Edit modal modes
	if m.sourceMode == sourceModeAdd || m.sourceMode == sourceModeEdit {
		switch msg.String() {
		case "esc":
			m.sourceMode = sourceModeNormal
			m.inputURL = ""
			m.inputName = ""
			m.setStatus("Cancelled")
			return m, nil

		case "tab", "down":
			m.inputFocus = (m.inputFocus + 1) % 2
			return m, nil

		case "shift+tab", "up":
			m.inputFocus = (m.inputFocus - 1 + 2) % 2
			return m, nil

		case "backspace":
			if m.inputFocus == 0 {
				_, size := utf8.DecodeLastRuneInString(m.inputURL)
				if size > 0 {
					m.inputURL = m.inputURL[:len(m.inputURL)-size]
				}
			} else if m.inputFocus == 1 {
				_, size := utf8.DecodeLastRuneInString(m.inputName)
				if size > 0 {
					m.inputName = m.inputName[:len(m.inputName)-size]
				}
			}
			return m, nil

		case "enter":
			trimmedURL := strings.TrimSpace(m.inputURL)
			trimmedName := strings.TrimSpace(m.inputName)
			if trimmedURL == "" {
				m.setStatus("Error: URL cannot be empty")
				return m, nil
			}

			if m.sourceMode == sourceModeAdd {
				if m.ctrl != nil {
					if err := m.ctrl.AddSource(trimmedURL, trimmedName); err != nil {
						m.setStatus(fmt.Sprintf("Error: %s", err))
						return m, nil
					}
					m.setStatus(fmt.Sprintf("Added source: %s", trimmedURL))
					m.refreshConfigCenter()
					sources := m.configCategorySources()
					m.configItemIndex = len(sources) - 1
				}
			} else { // sourceModeEdit
				if m.ctrl != nil {
					if err := m.ctrl.UpdateSource(m.editSourceID, trimmedURL, trimmedName); err != nil {
						m.setStatus(fmt.Sprintf("Error: %s", err))
						return m, nil
					}
					m.setStatus(fmt.Sprintf("Updated source: %s", trimmedName))
					m.refreshConfigCenter()
				}
			}
			m.sourceMode = sourceModeNormal
			m.inputURL = ""
			m.inputName = ""
			m.editSourceID = ""
			return m, nil

		default:
			var char string
			if msg.Type == tea.KeyRunes {
				char = string(msg.Runes)
			} else if msg.Type == tea.KeySpace || msg.String() == " " || msg.String() == "space" {
				char = " "
			} else if len(msg.String()) == 1 {
				char = msg.String()
			}
			if char != "" {
				if m.inputFocus == 0 {
					m.inputURL += char
				} else {
					m.inputName += char
				}
			}
			return m, nil
		}
	}

	// 3. Normal navigation mode in CategorySources
	sources := m.configCategorySources()
	switch msg.String() {
	case "tab", "l", "right":
		m.configCategory = (m.configCategory + 1) % viewmodel.ConfigCategory(catCount)
		m.configItemIndex = 0
		return m, nil

	case "shift+tab", "h", "left":
		if m.configCategory == 0 {
			m.configCategory = viewmodel.ConfigCategory(catCount - 1)
		} else {
			m.configCategory--
		}
		m.configItemIndex = 0
		return m, nil

	case "j", "down":
		if m.configItemIndex < len(sources)-1 {
			m.configItemIndex++
		}
		return m, nil

	case "k", "up":
		if m.configItemIndex > 0 {
			m.configItemIndex--
		}
		return m, nil

	case "g":
		m.configItemIndex = 0
		return m, nil

	case "G":
		if len(sources) > 0 {
			m.configItemIndex = len(sources) - 1
		}
		return m, nil

	case " ", "e":
		if len(sources) == 0 || m.ctrl == nil {
			return m, nil
		}
		if m.configItemIndex >= len(sources) {
			m.configItemIndex = 0
		}
		sel := sources[m.configItemIndex]
		if err := m.ctrl.ToggleSource(sel.ID); err != nil {
			m.setStatus(fmt.Sprintf("Error: %s", err))
		} else {
			newState := "enabled"
			if sel.Enabled {
				newState = "disabled"
			}
			m.setStatus(fmt.Sprintf("Source %s: %s", newState, sel.Name))
			m.refreshConfigCenter()
		}
		return m, nil

	case "a":
		m.sourceMode = sourceModeAdd
		m.inputURL = ""
		m.inputName = ""
		m.inputFocus = 0
		m.setStatus("Add Source: enter URL and optional Name ([Enter] Submit, [Tab] Field, [Esc] Cancel)")
		return m, nil

	case "enter":
		if len(sources) == 0 {
			return m, nil
		}
		if m.configItemIndex >= len(sources) {
			m.configItemIndex = 0
		}
		sel := sources[m.configItemIndex]
		m.sourceMode = sourceModeEdit
		m.editSourceID = sel.ID
		m.inputURL = sel.URL
		m.inputName = sel.Name
		m.inputFocus = 0
		m.setStatus("Edit Source: update URL or Name ([Enter] Save, [Tab] Field, [Esc] Cancel)")
		return m, nil

	case "d", "x":
		if len(sources) == 0 {
			m.setStatus("No sources to delete")
			return m, nil
		}
		if m.configItemIndex >= len(sources) {
			m.configItemIndex = 0
		}
		sel := sources[m.configItemIndex]
		m.sourceMode = sourceModeDeleteConfirm
		m.confirmDeleteID = sel.ID
		m.confirmDeleteName = sel.Name
		m.setStatus(fmt.Sprintf("Delete %q? Press 'y' to confirm, any other key to cancel", sel.Name))
		return m, nil
	}

	return m, nil
}

// configCategorySources returns the sources for the CategorySources tab.
func (m *Model) configCategorySources() []viewmodel.SourceItemViewModel {
	for _, cat := range m.configCenter.Categories {
		if cat.Category == viewmodel.CategorySources {
			return cat.Sources
		}
	}
	return nil
}

// configCategoryItems returns the items for the currently active config category.
func (m *Model) configCategoryItems() []viewmodel.ConfigItemViewModel {
	for _, cat := range m.configCenter.Categories {
		if cat.Category == m.configCategory {
			return cat.Items
		}
	}
	return nil
}

// refreshConfigCenter re-queries the presentation Controller for updated configuration
// ViewModels and clamps the active item cursor to the bounds of the active category.
func (m *Model) refreshConfigCenter() {
	if m.ctrl != nil {
		m.configCenter = m.ctrl.ConfigCenter()
		if m.configCategory == viewmodel.CategorySources {
			sources := m.configCategorySources()
			if m.configItemIndex >= len(sources) && len(sources) > 0 {
				m.configItemIndex = len(sources) - 1
			} else if len(sources) == 0 {
				m.configItemIndex = 0
			}
		} else {
			items := m.configCategoryItems()
			if m.configItemIndex >= len(items) && len(items) > 0 {
				m.configItemIndex = len(items) - 1
			} else if len(items) == 0 {
				m.configItemIndex = 0
			}
		}
	}
}

func (m *Model) applySnapshot(snap viewmodel.SnapshotViewModel, resetCursor bool) {
	m.header = snap.Header
	m.logs = snap.Logs

	total := snap.TotalRows
	if total == 0 && len(snap.Rows) > 0 {
		total = len(snap.Rows)
	}
	m.totalRows = total

	if resetCursor {
		m.cursorIndex = 0
		m.tableOffset = 0
	} else if m.totalRows > 0 {
		if m.cursorIndex >= m.totalRows {
			m.cursorIndex = m.totalRows - 1
		}
	} else {
		m.cursorIndex = 0
	}
	m.adjustScroll()

	m.syncVisibleRows(snap.Rows)

	if m.showDetail && m.detailCandidateID != "" && m.ctrl != nil {
		if detail, ok := m.ctrl.CandidateDetail(m.detailCandidateID); ok {
			m.detail = detail
		}
	}

	if m.logFollow {
		vis := m.visibleLogRows()
		maxOffset := len(m.logs.Lines) - vis
		if maxOffset < 0 {
			maxOffset = 0
		}
		m.logScrollOffset = maxOffset
	}
}

func (m *Model) syncVisibleRows(snapRows []viewmodel.CandidateRowViewModel) {
	vis := m.visibleTableRows()
	if m.tableOffset == 0 && len(snapRows) >= vis {
		m.rows = snapRows[:vis]
		m.windowOffset = 0
		return
	}
	if m.tableOffset == 0 && len(snapRows) == m.totalRows {
		m.rows = snapRows
		m.windowOffset = 0
		return
	}
	m.refreshVisibleRows()
}

func (m *Model) refreshVisibleRows() {
	vis := m.visibleTableRows()
	if m.ctrl != nil {
		m.rows = m.ctrl.CandidateRowsWindow(m.filterMode, m.tableOffset, vis)
		m.windowOffset = m.tableOffset
	}
}

func (m *Model) selectedCandidateID() string {
	if m.cursorIndex < 0 || m.cursorIndex >= m.totalRows {
		return ""
	}
	idx := m.cursorIndex - m.windowOffset
	if idx < 0 || idx >= len(m.rows) {
		m.adjustScroll()
		m.refreshVisibleRows()
		idx = m.cursorIndex - m.windowOffset
	}
	if idx >= 0 && idx < len(m.rows) {
		return m.rows[idx].ID
	}
	return ""
}

func (m *Model) adjustScroll() {
	vis := m.visibleTableRows()
	if vis <= 0 {
		vis = 10
	}
	if m.cursorIndex < m.tableOffset {
		m.tableOffset = m.cursorIndex
	} else if m.cursorIndex >= m.tableOffset+vis {
		m.tableOffset = m.cursorIndex - vis + 1
	}
	if m.tableOffset < 0 {
		m.tableOffset = 0
	}
	if m.totalRows > 0 && m.tableOffset >= m.totalRows {
		m.tableOffset = m.totalRows - 1
	}
}

func (m *Model) visibleTableRows() int {
	// Total height minus Header (4), Header Divider (1), Table Header (1), Footer (2)
	avail := m.height - 8
	if avail < 3 {
		return 3
	}
	return avail
}

func (m *Model) visibleLogRows() int {
	// Total height minus Header (4), Divider (1), Log Title (1), Footer (2)
	avail := m.height - 8
	if avail < 3 {
		return 3
	}
	return avail
}

func (m *Model) setStatus(msg string) {
	m.statusMessage = msg
	m.statusExpiry = time.Now().Add(3 * time.Second)
}

// View renders the terminal user interface according to the current model state.
func (m *Model) View() string {
	if m.terminalTooSmall {
		return lipgloss.Place(
			m.width,
			m.height,
			lipgloss.Center,
			lipgloss.Center,
			"Terminal window too small (minimum: 80x24). Please enlarge.",
		)
	}

	var sb strings.Builder

	// 1. Header (global stats, progress, cycle lifecycle)
	sb.WriteString(m.renderHeader())
	sb.WriteString("\n")

	// 2. Main content
	if m.showDetail {
		sb.WriteString(m.renderDetail())
	} else {
		switch m.activeView {
		case ViewCandidates:
			sb.WriteString(m.renderCandidateTable())
		case ViewLogs:
			sb.WriteString(m.renderLogs())
		case ViewConfig:
			sb.WriteString(m.renderConfigCenter())
		}
	}

	// Fill vertical space
	renderedLines := strings.Count(sb.String(), "\n")
	targetLines := m.height - 1
	for i := renderedLines; i < targetLines; i++ {
		sb.WriteString("\n")
	}

	// 3. Footer status bar
	sb.WriteString(m.renderFooter())

	return sb.String()
}

func (m *Model) renderHeader() string {
	bold := lipgloss.NewStyle().Bold(true)
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	statusStyle := yellow
	if m.header.CycleStatus == viewmodel.CycleRunning {
		statusStyle = green
	}

	lastCycleStr := "Never"
	if !m.header.LastCycle.IsZero() {
		lastCycleStr = m.header.LastCycle.Format("15:04:05")
	}

	if m.activeView == ViewConfig {
		line1 := fmt.Sprintf("%s  >  %s  |  Cycle: %s  |  Last: %s",
			bold.Render("GEMSUB"),
			bold.Render("CONFIGURATION CENTER"),
			statusStyle.Render(string(m.header.CycleStatus)),
			lastCycleStr,
		)
		divider := dim.Render(strings.Repeat("─", m.width))
		return line1 + "\n" + divider
	}

	line1 := fmt.Sprintf("%s  |  Cycle: %s #%d  |  Last: %s",
		bold.Render("GEMSUB"),
		statusStyle.Render(string(m.header.CycleStatus)),
		m.header.CycleCount,
		lastCycleStr,
	)

	progStr := fmt.Sprintf("Probes: %d / %d", m.header.ProgressCurrent, m.header.ProgressTotal)
	if m.header.ProgressTotal > 0 {
		pct := int(float64(m.header.ProgressCurrent) / float64(m.header.ProgressTotal) * 100)
		progStr = fmt.Sprintf("Probes: %d / %d (%d%%)", m.header.ProgressCurrent, m.header.ProgressTotal, pct)
	}

	line2 := fmt.Sprintf("Servable: Gemini: %s  Generic: %s / %d  |  Cycle: Pass: %d  Fail: %d  Incon: %d  |  %s",
		green.Render(fmt.Sprintf("%d", m.header.ServableCount)),
		green.Render(fmt.Sprintf("%d", m.header.GenericServableCount)),
		m.header.TotalCandidates,
		m.header.PassedCount,
		m.header.FailedCount,
		m.header.InconclusiveCount,
		progStr,
	)

	divider := dim.Render(strings.Repeat("─", m.width))
	return line1 + "\n" + line2 + "\n" + divider
}

func (m *Model) renderCandidateTable() string {
	bold := lipgloss.NewStyle().Bold(true)
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)

	// Column header
	headerStr := fmt.Sprintf("  %-4s %-6s %-7s %-6s %-7s %-12s %s",
		"SERV", "PROTO", "STATUS", "SCORE", "LAT", "HISTORY", "ENDPOINT / REMARK")
	var sb strings.Builder
	sb.WriteString(bold.Render(headerStr))
	sb.WriteString("\n")

	if m.totalRows == 0 {
		sb.WriteString(dim.Render("  No candidates in current view."))
		sb.WriteString("\n")
		return sb.String()
	}

	vis := m.visibleTableRows()
	end := m.tableOffset + vis
	if end > m.totalRows {
		end = m.totalRows
	}

	endpointWidth := m.width - 49
	if endpointWidth < 15 {
		endpointWidth = 15
	}

	for i := m.tableOffset; i < end; i++ {
		idx := i - m.windowOffset
		if idx < 0 || idx >= len(m.rows) {
			continue
		}
		row := m.rows[idx]

		servChar := "[  ]"
		if row.Servable {
			servChar = green.Render("[✓✓]")
		} else if row.NetworkHealthy {
			servChar = yellow.Render("[G ]")
		}

		statusColor := dim
		switch row.Status {
		case "PASS":
			statusColor = green
		case "FAIL":
			statusColor = red
		case "INCON":
			statusColor = yellow
		case "BLOCKED", "DENIED":
			statusColor = yellow
		}

		target := row.Endpoint
		if row.Remark != "" {
			target = fmt.Sprintf("%s (%s)", row.Endpoint, row.Remark)
		}
		if len(target) > endpointWidth {
			target = target[:endpointWidth-1] + "…"
		}

		cursor := "  "
		if i == m.cursorIndex {
			cursor = "> "
		}

		line := fmt.Sprintf("%s%-4s %-6s %-7s %-6s %-7s %-12s %s",
			cursor,
			servChar,
			row.Protocol,
			statusColor.Render(fmt.Sprintf("%-7s", row.Status)),
			row.ScoreFormatted,
			row.LatencyFormatted,
			row.HistoryGlyphs,
			target,
		)

		if i == m.cursorIndex {
			sb.WriteString(selectedStyle.Render(line))
		} else {
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m *Model) renderDetail() string {
	bold := lipgloss.NewStyle().Bold(true)
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	var sb strings.Builder
	sb.WriteString(bold.Render("CANDIDATE INSPECTION"))
	sb.WriteString("  " + dim.Render("[Esc/Enter] Close  [y] Copy Raw Link"))
	sb.WriteString("\n\n")

	gemStr := red.Render("NO")
	if m.detail.Servable {
		gemStr = green.Render("YES")
	}
	genStr := red.Render("NO")
	if m.detail.NetworkHealthy {
		genStr = green.Render("YES")
	}

	var transStatusStr, evidenceStr, transLatStr string
	if m.detail.TransportEvidenceKnown {
		evidenceStr = "explicit"
		if m.detail.TransportOK {
			transStatusStr = green.Render("OK")
		} else {
			transStatusStr = red.Render("FAILED")
		}
		if m.detail.TransportLatency > 0 {
			transLatStr = m.detail.TransportLatency.Round(time.Millisecond).String()
		} else {
			transLatStr = "---"
		}
	} else {
		evidenceStr = "legacy"
		if m.detail.NetworkHealthy {
			transStatusStr = green.Render("OK")
			if m.detail.TransportLatency > 0 {
				transLatStr = m.detail.TransportLatency.Round(time.Millisecond).String()
			} else {
				transLatStr = "---"
			}
		} else {
			transStatusStr = dim.Render("UNKNOWN")
			transLatStr = "---"
		}
	}

	sb.WriteString(fmt.Sprintf("  ID:        %s\n", m.detail.ID))
	sb.WriteString(fmt.Sprintf("  Protocol:  %s\n", m.detail.Protocol))
	sb.WriteString(fmt.Sprintf("  Endpoint:  %s\n", m.detail.Endpoint))
	if m.detail.Host != "" || m.detail.Port != "" {
		sb.WriteString(fmt.Sprintf("  Host/Port: %s\n", formatHostPort(m.detail.Host, m.detail.Port)))
	}
	if m.detail.SNI != "" {
		sb.WriteString(fmt.Sprintf("  SNI:       %s\n", m.detail.SNI))
	}
	if m.detail.Path != "" {
		sb.WriteString(fmt.Sprintf("  Path:      %s\n", m.detail.Path))
	}
	if m.detail.Remark != "" {
		sb.WriteString(fmt.Sprintf("  Remark:    %s\n", m.detail.Remark))
	}
	sb.WriteString(fmt.Sprintf("  Servable:  Gemini: %s (Gate: %s)  |  Generic: %s\n", gemStr, m.detail.ServabilityGate, genStr))
	sb.WriteString(fmt.Sprintf("  Transport: Status=%s  Latency=%s  Evidence=%s\n", transStatusStr, transLatStr, evidenceStr))
	sb.WriteString(fmt.Sprintf("  Score:     %s  |  Proven: %t (Lat: %s)  |  AbsentCycles: %d\n",
		m.detail.ScoreFormatted, m.detail.HasPassed, m.detail.ProvenLatencyFormatted, m.detail.AbsentCycles))
	sb.WriteString(fmt.Sprintf("  Latest:    Status=%s  Category=%s  Code=%d  Lat=%v  Attempts=%d\n",
		m.detail.Status, m.detail.Category, m.detail.StatusCode, m.detail.Latency, m.detail.Attempts))
	if m.detail.Reason != "" {
		sb.WriteString(fmt.Sprintf("  Reason:    %s\n", m.detail.Reason))
	}
	if len(m.detail.Warnings) > 0 {
		sb.WriteString(fmt.Sprintf("  Warnings:  %s\n", strings.Join(m.detail.Warnings, "; ")))
	}

	sb.WriteString("\n  Masked Share URL:\n")
	sb.WriteString("  " + dim.Render(m.detail.MaskedLink) + "\n\n")

	sb.WriteString(bold.Render("  Bounded Sample History (Oldest → Newest):"))
	sb.WriteString("\n")
	if len(m.detail.Samples) == 0 {
		sb.WriteString(dim.Render("  No samples recorded."))
		sb.WriteString("\n")
	} else {
		headerStr := fmt.Sprintf("  %-2s %-10s %-12s %-21s %-5s %-10s %-8s",
			"#", "AGE", "STATUS", "CATEGORY", "CODE", "LATENCY", "ATTEMPTS")
		sb.WriteString(dim.Render(headerStr))
		sb.WriteString("\n")
		for _, s := range m.detail.Samples {
			statusColor := dim
			switch s.Status {
			case "passed":
				statusColor = green
			case "failed":
				statusColor = red
			}
			cat := s.Category
			if cat == "" {
				cat = "none"
			}
			if len(cat) > 21 {
				cat = cat[:20] + "…"
			}
			latStr := "---"
			if s.Latency > 0 {
				latStr = s.Latency.Round(time.Millisecond).String()
			}
			if len(latStr) > 10 {
				latStr = latStr[:10]
			}
			ageStr := s.Age
			if len(ageStr) > 10 {
				ageStr = ageStr[:10]
			}
			st := s.Status
			if len(st) > 12 {
				st = st[:12]
			}
			statusCell := statusColor.Render(fmt.Sprintf("%-12s", st))
			sb.WriteString(fmt.Sprintf("  %-2d %-10s %s %-21s %-5d %-10s %-8d\n",
				s.Index, ageStr, statusCell, cat, s.StatusCode, latStr, s.Attempts))
		}
	}

	return sb.String()
}

func (m *Model) renderLogs() string {
	bold := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))

	followStr := "auto-scroll ON"
	if !m.logFollow {
		followStr = "auto-scroll PAUSED (press G to resume)"
	}

	title := fmt.Sprintf("%s  [%s]  (Total: %d, %s)",
		bold.Render("LOG VIEWER"),
		yellow.Render(m.logs.MinLevel.String()),
		m.logs.TotalCount,
		dim.Render(followStr),
	)
	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString("\n")

	if len(m.logs.Lines) == 0 {
		sb.WriteString(dim.Render("  No log records at current level."))
		sb.WriteString("\n")
		return sb.String()
	}

	vis := m.visibleLogRows()
	start := m.logScrollOffset
	if start > len(m.logs.Lines)-vis {
		start = len(m.logs.Lines) - vis
	}
	if start < 0 {
		start = 0
	}
	end := start + vis
	if end > len(m.logs.Lines) {
		end = len(m.logs.Lines)
	}

	for i := start; i < end; i++ {
		line := m.logs.Lines[i]
		if len(line) > m.width {
			line = line[:m.width-1]
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m *Model) renderConfigCenter() string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)

	var sb strings.Builder

	// Category tab bar
	var tabs []string
	for i := 0; i < viewmodel.ConfigCategoryCount; i++ {
		cat := viewmodel.ConfigCategory(i)
		name := cat.Name()
		if cat == m.configCategory {
			tabs = append(tabs, cyan.Render("["+name+"]"))
		} else {
			tabs = append(tabs, dim.Render(" "+name+" "))
		}
	}
	sb.WriteString("  " + strings.Join(tabs, " "))
	sb.WriteString("\n")
	sb.WriteString(dim.Render(strings.Repeat("─", m.width)))
	sb.WriteString("\n")

	// Special handling for CategorySources
	if m.configCategory == viewmodel.CategorySources {
		sb.WriteString(m.renderSourceManager())
		return sb.String()
	}

	// Items for the active category
	items := m.configCategoryItems()
	if len(items) == 0 {
		sb.WriteString(dim.Render("  No settings in this category."))
		sb.WriteString("\n")
		return sb.String()
	}

	sb.WriteString(m.renderCategoryItems(items))
	return sb.String()
}

func (m *Model) renderCategoryItems(items []viewmodel.ConfigItemViewModel) string {
	bold := lipgloss.NewStyle().Bold(true)
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)

	var sb strings.Builder
	visibleItems := m.visibleConfigRows()
	scrollOffset := 0
	if m.configItemIndex >= scrollOffset+visibleItems {
		scrollOffset = m.configItemIndex - visibleItems + 1
	}
	if m.configItemIndex < scrollOffset {
		scrollOffset = m.configItemIndex
	}

	end := scrollOffset + visibleItems
	if end > len(items) {
		end = len(items)
	}

	maxLabel := 0
	for _, item := range items {
		if len(item.Label) > maxLabel {
			maxLabel = len(item.Label)
		}
	}
	if maxLabel > 25 {
		maxLabel = 25
	}

	for i := scrollOffset; i < end; i++ {
		item := items[i]
		cursor := "  "
		if i == m.configItemIndex {
			cursor = "> "
		}

		label := item.Label
		if len(label) > maxLabel {
			label = label[:maxLabel]
		}

		valueWidth := m.width - maxLabel - 8
		if valueWidth < 10 {
			valueWidth = 10
		}
		value := item.Value
		if len(value) > valueWidth {
			value = value[:valueWidth-1] + "…"
		}

		line := fmt.Sprintf("%s%-*s  %s", cursor, maxLabel, label, value)
		if i == m.configItemIndex {
			sb.WriteString(selectedStyle.Render(line))
		} else {
			sb.WriteString("  " + bold.Render(fmt.Sprintf("%-*s", maxLabel, label)) + "  " + value)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m *Model) renderSourceManager() string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)

	var sb strings.Builder

	// Modal prompt mode (Add or Edit)
	if m.sourceMode == sourceModeAdd || m.sourceMode == sourceModeEdit {
		title := "Add Subscription Source"
		actionHint := "[Enter] Add"
		if m.sourceMode == sourceModeEdit {
			title = "Edit Subscription Source"
			actionHint = "[Enter] Save"
		}

		boxWidth := m.width - 4
		if boxWidth > 72 {
			boxWidth = 72
		}
		if boxWidth < 40 {
			if m.width >= 44 {
				boxWidth = 40
			} else {
				boxWidth = m.width - 4
				if boxWidth < 20 {
					boxWidth = 20
				}
			}
		}

		urlPrefix := "  URL:  "
		namePrefix := "  Name: "
		if m.inputFocus == 0 {
			urlPrefix = cyan.Render("> URL:  ")
		} else {
			namePrefix = cyan.Render("> Name: ")
		}

		urlVal := m.inputURL
		if m.inputFocus == 0 {
			urlVal += "_"
		}
		nameVal := m.inputName
		if m.inputFocus == 1 {
			nameVal += "_"
		}
		if nameVal == "" && m.inputFocus != 1 {
			nameVal = dim.Render("(optional)")
		}

		titleWidth := lipgloss.Width(title)
		repeatTop := boxWidth - titleWidth - 5
		if repeatTop < 0 {
			repeatTop = 0
		}
		repeatBot := boxWidth - 2
		if repeatBot < 0 {
			repeatBot = 0
		}
		topBar := "┌─ " + title + " " + strings.Repeat("─", repeatTop) + "┐"
		botBar := "└" + strings.Repeat("─", repeatBot) + "┘"

		// Available width for field values:
		// Box width minus 2 (outer borders) minus 2 (inner 1-char margins) = boxWidth - 4
		// minus prefix width (8) = boxWidth - 12
		fieldWidth := boxWidth - 12
		if fieldWidth < 5 {
			fieldWidth = 5
		}

		dispURL := urlVal
		if lipgloss.Width(dispURL) > fieldWidth {
			dispURL = ansi.Truncate(dispURL, fieldWidth, "…")
		}
		urlPadLen := fieldWidth - lipgloss.Width(dispURL)
		if urlPadLen < 0 {
			urlPadLen = 0
		}
		urlPad := strings.Repeat(" ", urlPadLen)

		dispName := nameVal
		if lipgloss.Width(dispName) > fieldWidth {
			dispName = ansi.Truncate(dispName, fieldWidth, "…")
		}
		namePadLen := fieldWidth - lipgloss.Width(dispName)
		if namePadLen < 0 {
			namePadLen = 0
		}
		namePad := strings.Repeat(" ", namePadLen)

		blankPadLen := boxWidth - 4
		if blankPadLen < 0 {
			blankPadLen = 0
		}
		blankLine := strings.Repeat(" ", blankPadLen)

		hintContent := fmt.Sprintf(" %s   [Tab] Switch Field   [Esc] Cancel", actionHint)
		hintMax := boxWidth - 4
		if hintMax < 5 {
			hintMax = 5
		}
		if lipgloss.Width(hintContent) > hintMax {
			hintContent = ansi.Truncate(hintContent, hintMax, "…")
		}
		hintPadLen := hintMax - lipgloss.Width(hintContent)
		if hintPadLen < 0 {
			hintPadLen = 0
		}
		hintPad := strings.Repeat(" ", hintPadLen)

		sb.WriteString("  " + dim.Render(topBar) + "\n")
		sb.WriteString("  │ " + urlPrefix + dispURL + urlPad + " │\n")
		sb.WriteString("  │ " + namePrefix + dispName + namePad + " │\n")
		sb.WriteString("  │ " + blankLine + " │\n")
		sb.WriteString("  │ " + hintContent + hintPad + " │\n")
		sb.WriteString("  " + dim.Render(botBar) + "\n")
		return sb.String()
	}

	sources := m.configCategorySources()
	if len(sources) == 0 {
		items := m.configCategoryItems()
		if len(items) > 0 {
			return m.renderCategoryItems(items)
		}
		sb.WriteString(dim.Render("  No subscription sources configured."))
		sb.WriteString("\n")
		sb.WriteString(dim.Render("  Press 'a' to add a new subscription source."))
		sb.WriteString("\n")
		return sb.String()
	}

	visibleRows := m.visibleConfigRows()
	scrollOffset := 0
	if m.configItemIndex >= scrollOffset+visibleRows {
		scrollOffset = m.configItemIndex - visibleRows + 1
	}
	if m.configItemIndex < scrollOffset {
		scrollOffset = m.configItemIndex
	}

	end := scrollOffset + visibleRows
	if end > len(sources) {
		end = len(sources)
	}

	for i := scrollOffset; i < end; i++ {
		src := sources[i]
		cursor := "  "
		if i == m.configItemIndex {
			cursor = "> "
		}

		badge := green.Render("[ENABLED] ")
		if !src.Enabled {
			badge = dim.Render("[DISABLED]")
		}

		nameStr := src.Name
		if src.HasCount {
			nameStr = fmt.Sprintf("%s (%d)", src.Name, src.CandidateCount)
		} else if src.CandidateCount > 0 {
			nameStr = fmt.Sprintf("%s (%d)", src.Name, src.CandidateCount)
		}

		nameColWidth := 20
		if lipgloss.Width(nameStr) > nameColWidth {
			nameStr = ansi.Truncate(nameStr, nameColWidth, "…")
		}
		namePadLen := nameColWidth - lipgloss.Width(nameStr)
		if namePadLen < 0 {
			namePadLen = 0
		}
		namePadded := nameStr + strings.Repeat(" ", namePadLen)

		urlWidth := m.width - 2 - 11 - nameColWidth - 4
		if urlWidth < 10 && m.width >= 47 {
			urlWidth = 10
		} else if urlWidth < 3 {
			urlWidth = 3
		}
		urlStr := src.URL
		if lipgloss.Width(urlStr) > urlWidth {
			urlStr = ansi.Truncate(urlStr, urlWidth, "…")
		}

		line := fmt.Sprintf("%s%s  %s  %s", cursor, badge, namePadded, urlStr)
		if m.width > 0 && lipgloss.Width(line) > m.width {
			line = ansi.Truncate(line, m.width, "")
		}

		if i == m.configItemIndex {
			sb.WriteString(selectedStyle.Render(line))
		} else {
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}

	if m.sourceMode == sourceModeDeleteConfirm {
		sb.WriteString("\n")
		confirmPrompt := fmt.Sprintf("  Delete source %q? Press 'y' to confirm, any other key to cancel", m.confirmDeleteName)
		if m.width > 0 && lipgloss.Width(confirmPrompt) > m.width {
			confirmPrompt = ansi.Truncate(confirmPrompt, m.width, "…")
		}
		sb.WriteString(yellow.Render(confirmPrompt))
		sb.WriteString("\n")
	}

	return sb.String()
}

// visibleConfigRows returns the number of item rows visible in the config center.
func (m *Model) visibleConfigRows() int {
	// Total height minus Header (3), Divider (1), Tab bar (1), Tab divider (1), Footer (1)
	avail := m.height - 7
	if avail < 3 {
		return 3
	}
	return avail
}

func (m *Model) renderFooter() string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)

	statusStr := ""
	if m.statusMessage != "" {
		statusStr = cyan.Render("[" + m.statusMessage + "] ")
	}

	filterState := "[All]"
	switch m.filterMode {
	case viewmodel.FilterGemini:
		filterState = "[Gemini]"
	case viewmodel.FilterGeneric:
		filterState = "[Generic]"
	}

	var hints string
	if m.showDetail {
		hints = "[Esc] Close Detail  [y] Copy Link  [q] Quit"
	} else if m.activeView == ViewConfig {
		if m.configCategory == viewmodel.CategorySources {
			switch m.sourceMode {
			case sourceModeDeleteConfirm:
				hints = fmt.Sprintf("%s Press 'y' to confirm deletion, any other key to cancel", statusStr)
			case sourceModeAdd:
				hints = fmt.Sprintf("%s [Enter] Add  [Tab] Switch Field  [Esc] Cancel", statusStr)
			case sourceModeEdit:
				hints = fmt.Sprintf("%s [Enter] Save  [Tab] Switch Field  [Esc] Cancel", statusStr)
			default:
				hints = fmt.Sprintf("%s [Space] Toggle [a] Add [Enter] Edit [d] Delete [Tab] Cat [Esc] Back [q] Quit", statusStr)
			}
		} else {
			hints = fmt.Sprintf("%s [Tab/h/l] Category  [j/k] Navigate  [Esc] Back  [q] Quit", statusStr)
		}
	} else if m.activeView == ViewCandidates {
		hints = fmt.Sprintf("%s [s] Filter %s  [Enter] Detail  [y] Copy  [c] Config  [Tab] Logs  [q] Quit",
			statusStr, filterState)
	} else {
		hints = fmt.Sprintf("%s [l] Level  [g/G] Top/Bottom  [c] Config  [Tab] Candidates  [q] Quit",
			statusStr)
	}

	rendered := dim.Render(hints)
	return truncateToWidth(rendered, m.width)
}

// truncateToWidth truncates a string (preserving UTF-8 integrity and ANSI styling)
// so that its terminal visual width does not exceed maxWidth cells.
// It handles zero or negative widths safely without panicking.
func truncateToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	return ansi.Truncate(s, maxWidth, "")
}

// formatHostPort formats host and port using bracketed host:port notation for IPv6 addresses.
func formatHostPort(host, port string) string {
	if host == "" && port == "" {
		return ""
	}
	cleanHost := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if strings.Contains(cleanHost, ":") {
		host = "[" + cleanHost + "]"
	}
	if port != "" {
		return host + ":" + port
	}
	return host
}
