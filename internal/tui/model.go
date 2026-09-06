package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gemsub/internal/tui/viewmodel"
)

// Controller represents the presentation query and command boundary for Bubble Tea.
// Bubble Tea operates exclusively on presentation ViewModels and opaque identifiers,
// without performing application-state projection or retaining references to internal
// adapters or stores.
type Controller interface {
	Snapshot(servableOnly bool) viewmodel.SnapshotViewModel
	PollSnapshot(servableOnly bool) (viewmodel.SnapshotViewModel, bool)
	CandidateDetail(opaqueID string) (viewmodel.CandidateDetailViewModel, bool)
	CycleLogLevel() (viewmodel.LogViewModel, string)
	CopyCandidateLink(opaqueID string) error
}

// ActiveView represents the primary content pane currently displayed.
type ActiveView int

const (
	ViewCandidates ActiveView = iota
	ViewLogs
)

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
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

	// View state
	activeView       ActiveView
	servableFilter   bool
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
}

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
		m.applySnapshot(ctrl.Snapshot(false), true)
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
		return m, nil

	case tickMsg:
		// Background Store/ViewModel refreshes are throttled to a maximum 10 Hz rate (100 ms interval).
		// When dirty == false, no Store snapshot or ViewModel rebuild is performed.
		// Interactive navigation (keys, resizes) responds and renders immediately without frame drops.
		if m.ctrl != nil {
			if snap, ok := m.ctrl.PollSnapshot(m.servableFilter); ok {
				m.applySnapshot(snap, false)
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
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

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
		m.servableFilter = !m.servableFilter
		if m.ctrl != nil {
			m.applySnapshot(m.ctrl.Snapshot(m.servableFilter), true)
		}
		if m.servableFilter {
			m.setStatus("Filter: Servable only")
		} else {
			m.setStatus("Filter: All candidates")
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
	switch msg.String() {
	case "up", "k":
		if m.cursorIndex > 0 {
			m.cursorIndex--
			m.adjustScroll()
		}
	case "down", "j":
		if m.cursorIndex < len(m.rows)-1 {
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
		if m.cursorIndex >= len(m.rows) {
			m.cursorIndex = len(m.rows) - 1
		}
		if m.cursorIndex < 0 {
			m.cursorIndex = 0
		}
		m.adjustScroll()
	case "g":
		m.cursorIndex = 0
		m.adjustScroll()
	case "G":
		if len(m.rows) > 0 {
			m.cursorIndex = len(m.rows) - 1
			m.adjustScroll()
		}
	case "enter", " ":
		if len(m.rows) > 0 && m.cursorIndex >= 0 && m.cursorIndex < len(m.rows) {
			selectedID := m.rows[m.cursorIndex].ID
			if m.ctrl != nil {
				if detail, ok := m.ctrl.CandidateDetail(selectedID); ok {
					m.detail = detail
					m.showDetail = true
				}
			}
		}
	case "y":
		if len(m.rows) > 0 && m.cursorIndex >= 0 && m.cursorIndex < len(m.rows) {
			selectedID := m.rows[m.cursorIndex].ID
			if m.ctrl != nil {
				if err := m.ctrl.CopyCandidateLink(selectedID); err == nil {
					m.setStatus("Copied active link to clipboard")
				} else {
					m.setStatus("Clipboard unavailable: could not copy")
				}
			}
		}
	}
	return m, nil
}

func (m *Model) handleDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter", " ":
		m.showDetail = false
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

func (m *Model) applySnapshot(snap viewmodel.SnapshotViewModel, resetCursor bool) {
	m.header = snap.Header
	m.rows = snap.Rows
	m.logs = snap.Logs

	if resetCursor {
		m.cursorIndex = 0
		m.tableOffset = 0
	} else if len(m.rows) > 0 {
		if m.cursorIndex >= len(m.rows) {
			m.cursorIndex = len(m.rows) - 1
		}
	} else {
		m.cursorIndex = 0
	}
	m.adjustScroll()

	if m.showDetail && m.cursorIndex < len(m.rows) {
		selectedID := m.rows[m.cursorIndex].ID
		if m.ctrl != nil {
			if detail, ok := m.ctrl.CandidateDetail(selectedID); ok {
				m.detail = detail
			}
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

	line2 := fmt.Sprintf("Servable: %s / %d  |  Pass: %d  Fail: %d  Incon: %d  |  %s",
		green.Render(fmt.Sprintf("%d", m.header.ServableCount)),
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
	headerStr := fmt.Sprintf("  %-4s %-6s %-6s %-6s %-7s %-12s %s",
		"SERV", "PROTO", "STATUS", "SCORE", "LAT", "HISTORY", "ENDPOINT / REMARK")
	var sb strings.Builder
	sb.WriteString(bold.Render(headerStr))
	sb.WriteString("\n")

	if len(m.rows) == 0 {
		sb.WriteString(dim.Render("  No candidates in current view."))
		sb.WriteString("\n")
		return sb.String()
	}

	vis := m.visibleTableRows()
	end := m.tableOffset + vis
	if end > len(m.rows) {
		end = len(m.rows)
	}

	endpointWidth := m.width - 48
	if endpointWidth < 15 {
		endpointWidth = 15
	}

	for i := m.tableOffset; i < end; i++ {
		row := m.rows[i]

		servChar := "[ ]"
		if row.Servable {
			servChar = green.Render("[✓]")
		}

		statusColor := dim
		switch row.Status {
		case "PASS":
			statusColor = green
		case "FAIL":
			statusColor = red
		case "INCON":
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

		line := fmt.Sprintf("%s%-4s %-6s %-6s %-6s %-7s %-12s %s",
			cursor,
			servChar,
			row.Protocol,
			statusColor.Render(fmt.Sprintf("%-6s", row.Status)),
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

	servStr := red.Render("NO")
	if m.detail.Servable {
		servStr = green.Render("YES")
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
	sb.WriteString(fmt.Sprintf("  Servable:  %s (Gate: %s)\n", servStr, m.detail.ServabilityGate))
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
		sb.WriteString(dim.Render("  #  AGE        STATUS    CATEGORY              CODE  LATENCY    ATTEMPTS\n"))
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
			latStr := "---"
			if s.Latency > 0 {
				latStr = s.Latency.Round(time.Millisecond).String()
			}
			sb.WriteString(fmt.Sprintf("  %-2d %-10s %-9s %-21s %-5d %-10s %-8d\n",
				s.Index, s.Age, statusColor.Render(s.Status), cat, s.StatusCode, latStr, s.Attempts))
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

func (m *Model) renderFooter() string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)

	statusStr := ""
	if m.statusMessage != "" {
		statusStr = cyan.Render("[" + m.statusMessage + "] ")
	}

	filterState := "[All]"
	if m.servableFilter {
		filterState = "[Servable Only]"
	}

	var hints string
	if m.showDetail {
		hints = "[Esc] Close Detail  [y] Copy Link  [q] Quit"
	} else if m.activeView == ViewCandidates {
		hints = fmt.Sprintf("%s [s] Filter %s  [Enter] Detail  [y] Copy  [Tab] Logs  [q] Quit",
			statusStr, filterState)
	} else {
		hints = fmt.Sprintf("%s [l] Level  [g/G] Top/Bottom  [Tab] Candidates  [q] Quit",
			statusStr)
	}

	return dim.Render(hints)
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
