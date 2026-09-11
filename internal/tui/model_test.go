package tui_test

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/logging"
	"gemsub/internal/store"
	"gemsub/internal/tui"
	"gemsub/internal/tui/adapter"
	"gemsub/internal/tui/country"
	"gemsub/internal/tui/viewmodel"
)

func setupTestModel(t *testing.T) (*tui.Model, *store.Store, *events.EventBus, *logging.RingLogHandler) {
	t.Helper()
	t.Setenv("COLORTERM", "")
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	st := store.New(stateFile, 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)

	ad := adapter.New(st, bus, ring)
	t.Cleanup(func() {
		ad.Close()
		bus.Close()
	})

	m := tui.New(ad)
	return m, st, bus, ring
}

func TestModel_TerminalSizeFallback(t *testing.T) {
	m, _, _, _ := setupTestModel(t)

	// 1. Initial size is 80x24 (normal)
	viewNormal := m.View()
	if strings.Contains(viewNormal, "Terminal window too small") {
		t.Errorf("expected normal view at 80x24, got small fallback: %s", viewNormal)
	}

	// 2. Resize to 79x24 (width too small)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 79, Height: 24})
	m = updated.(*tui.Model)
	viewSmall := m.View()
	expectedMsg := "Terminal window too small (minimum: 80x24). Please enlarge."
	if !strings.Contains(viewSmall, expectedMsg) {
		t.Errorf("expected fallback message %q for 79x24, got: %s", expectedMsg, viewSmall)
	}

	// 3. Resize to 80x23 (height too small)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 23})
	m = updated.(*tui.Model)
	viewSmallH := m.View()
	if !strings.Contains(viewSmallH, expectedMsg) {
		t.Errorf("expected fallback message %q for 80x23, got: %s", expectedMsg, viewSmallH)
	}

	// 4. Resize back to 100x30 (normal)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(*tui.Model)
	viewBack := m.View()
	if strings.Contains(viewBack, expectedMsg) {
		t.Errorf("expected normal view after enlarging, got fallback: %s", viewBack)
	}
}

func TestModel_KeyboardNavigationAndDetail(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	// Populate 2 candidates
	st.PutWithTransition(store.Result{Link: "vless://c1@1.1.1.1:443#First", Status: store.StatusPassed, TestedAt: time.Now()})
	st.PutWithTransition(store.Result{Link: "vless://c2@2.2.2.2:443#Second", Status: store.StatusPassed, TestedAt: time.Now()})

	// Trigger tick/refresh
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}) // toggle filter back and forth
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(*tui.Model)

	// Move cursor down: 'j'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(*tui.Model)

	// Press Enter to open detail
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	viewDetail := m.View()
	if !strings.Contains(viewDetail, "CANDIDATE INSPECTION") {
		t.Fatalf("expected CANDIDATE INSPECTION view, got:\n%s", viewDetail)
	}
	if !strings.Contains(viewDetail, "Second") {
		t.Errorf("expected detail for 'Second', got:\n%s", viewDetail)
	}

	// Press Esc to close detail
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(*tui.Model)
	viewClosed := m.View()
	if strings.Contains(viewClosed, "CANDIDATE INSPECTION") {
		t.Errorf("expected detail view to be closed, got:\n%s", viewClosed)
	}
}

func TestModel_TabSwitchesView(t *testing.T) {
	m, _, _, _ := setupTestModel(t)

	// Initial view is candidate table
	view1 := m.View()
	if !strings.Contains(view1, "SERV") || !strings.Contains(view1, "ENDPOINT") {
		t.Errorf("expected candidate table initially, got:\n%s", view1)
	}

	// Press Tab -> switch to Logs
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	view2 := m.View()
	if !strings.Contains(view2, "LOG VIEWER") {
		t.Errorf("expected LOG VIEWER after Tab, got:\n%s", view2)
	}

	// Press Tab -> switch back to Candidates
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	view3 := m.View()
	if !strings.Contains(view3, "SERV") {
		t.Errorf("expected candidate table after second Tab, got:\n%s", view3)
	}
}

func TestModel_QuitKeys(t *testing.T) {
	m, _, _, _ := setupTestModel(t)

	// 'q' quits
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Error("expected quit cmd on 'q'")
	}

	// 'ctrl+c' quits
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected quit cmd on ctrl+c")
	}
}

func TestModel_DetailIPv6BracketedHostPort(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	// Populate an IPv6 candidate
	st.PutWithTransition(store.Result{
		Link:     "vless://uuid@[2001:db8::1]:443#IPv6Node",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})

	// Trigger tick/refresh
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(*tui.Model)

	// Open detail
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	viewDetail := m.View()
	if !strings.Contains(viewDetail, "Host/Port: [2001:db8::1]:443") {
		t.Errorf("expected bracketed IPv6 Host/Port '[2001:db8::1]:443', got:\n%s", viewDetail)
	}
}

func TestModel_HeaderRendersCurrentCycleMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	st := store.New(stateFile, 2)
	// Historical records in store: 2 passed, 1 failed
	now := time.Now()
	st.PutWithTransition(store.Result{Link: "vless://old1@1.1.1.1:443#Old1", Status: store.StatusPassed, TestedAt: now})
	st.PutWithTransition(store.Result{Link: "vless://old2@1.1.1.2:443#Old2", Status: store.StatusPassed, TestedAt: now})
	st.PutWithTransition(store.Result{Link: "vless://old3@1.1.1.3:443#Old3", Status: store.StatusFailed, TestedAt: now})

	bus := events.New()
	defer bus.Close()
	ring := logging.NewRingLogHandler(100)

	ad := adapter.New(st, bus, ring)
	ad.Subscribe()
	defer ad.Close()

	m := tui.New(ad)

	// Initial render: store has 2 passed, 1 failed, but current cycle metrics must be 0!
	viewInit := m.View()
	if !strings.Contains(viewInit, "Cycle: Pass: 0  Fail: 0  Incon: 0") {
		t.Errorf("expected initial header to render 'Cycle: Pass: 0  Fail: 0  Incon: 0', got:\n%s", viewInit)
	}

	// Cycle starts and probes complete
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	bus.Publish(events.ProbeCompleted{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    1,
			Total:        1,
			Passed:       1,
			Failed:       0,
			Inconclusive: 0,
		},
	})
	time.Sleep(30 * time.Millisecond)

	// Exercise the actual tick/update path directly via TickMsg
	updated, cmd := m.Update(tui.TickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("expected non-nil reschedule tick command from Update(TickMsg)")
	}
	m = updated.(*tui.Model)

	viewUpdated := m.View()
	if !strings.Contains(viewUpdated, "Cycle: Pass: 1  Fail: 0  Incon: 0") {
		t.Errorf("expected updated header to render 'Cycle: Pass: 1  Fail: 0  Incon: 0', got:\n%s", viewUpdated)
	}
}

func TestModel_CountryFlagRendering(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "store.json"), 2)
	bus := events.New()
	defer bus.Close()
	ad := adapter.New(st, bus, nil)
	ad.Subscribe()
	defer ad.Close()

	st.PutWithTransition(store.Result{
		Link:     "vless://node1@1.1.1.1:443#🇩🇪 Germany",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})

	m := tui.New(ad)

	// 1. ASCII mode: table and detail render [DE] Germany
	ad.SetFlagMode(country.ModeASCII)
	updated, _ := m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)

	viewASCII := m.View()
	if !strings.Contains(viewASCII, "[DE] Germany") {
		t.Errorf("expected view to contain '[DE] Germany', got:\n%s", viewASCII)
	}
	if strings.Contains(viewASCII, "🇩🇪") {
		t.Errorf("expected ASCII view not to contain emoji flag '🇩🇪', got:\n%s", viewASCII)
	}

	// Open detail view
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)
	detailASCII := m.View()
	if !strings.Contains(detailASCII, "Remark:    [DE] Germany") {
		t.Errorf("expected detail to contain 'Remark:    [DE] Germany', got:\n%s", detailASCII)
	}

	// Close detail
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*tui.Model)

	// 2. Unicode mode: table and detail render 🇩🇪 Germany
	ad.SetFlagMode(country.ModeUnicode)
	updated, _ = m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)

	viewUnicode := m.View()
	if !strings.Contains(viewUnicode, "🇩🇪 Germany") {
		t.Errorf("expected view to contain '🇩🇪 Germany', got:\n%s", viewUnicode)
	}

	// Open detail view
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)
	detailUnicode := m.View()
	if !strings.Contains(detailUnicode, "Remark:    🇩🇪 Germany") {
		t.Errorf("expected detail to contain 'Remark:    🇩🇪 Germany', got:\n%s", detailUnicode)
	}
}

func TestModel_HeaderRendersDualServabilityCounts(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	now := time.Now()
	// c1: Gemini-servable (2 passed)
	c1 := "vless://c1@1.1.1.1:443#GeminiNode"
	st.PutWithTransition(store.Result{Link: c1, Status: store.StatusPassed, TestedAt: now.Add(-time.Minute)})
	st.PutWithTransition(store.Result{Link: c1, Status: store.StatusPassed, TestedAt: now})

	// c2: Generic-servable only (RegionBlocked)
	c2 := "vless://c2@2.2.2.2:443#GenericNode"
	st.PutWithTransition(store.Result{
		Link:                   c2,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       50 * time.Millisecond,
		TestedAt:               now.Add(-time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:                   c2,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       45 * time.Millisecond,
		TestedAt:               now,
	})

	// c3: Dead node
	c3 := "vless://c3@3.3.3.3:443#DeadNode"
	st.PutWithTransition(store.Result{Link: c3, Status: store.StatusFailed, Category: store.ErrProxyError, TestedAt: now})

	// Trigger snapshot refresh
	updated, _ := m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)

	view := m.View()
	// Line 2 should display: Servable: Gemini: 1  Generic: 2 / 3
	if !strings.Contains(view, "Gemini: 1") || !strings.Contains(view, "Generic: 2 / 3") {
		t.Errorf("expected view to contain dual servability counts 'Gemini: 1' and 'Generic: 2 / 3', got:\n%s", view)
	}
}

func TestModel_TableRendersDualBadgesAndBlockedStatus(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	now := time.Now()
	c1 := "vless://c1@1.1.1.1:443#PassNode"
	st.PutWithTransition(store.Result{
		Link:                   c1,
		Status:                 store.StatusPassed,
		Latency:                100 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       100 * time.Millisecond,
		TestedAt:               now,
	})

	c2 := "vless://c2@2.2.2.2:443#BlockedNode"
	st.PutWithTransition(store.Result{
		Link:                   c2,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       50 * time.Millisecond,
		TestedAt:               now,
	})

	c3 := "vless://c3@3.3.3.3:443#DeadNode"
	st.PutWithTransition(store.Result{
		Link:                   c3,
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TestedAt:               now,
	})

	updated, _ := m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)

	view := m.View()

	// Dual servability markers
	// c1 (Gemini servable): [✓✓]
	if !strings.Contains(view, "[✓✓]") {
		t.Errorf("expected view to contain [✓✓] for Gemini servable node, got:\n%s", view)
	}
	// c2 (Generic-only servable): [G ]
	if !strings.Contains(view, "[G ]") {
		t.Errorf("expected view to contain [G ] for generic-only servable node, got:\n%s", view)
	}
	// c3 (Dead node): [  ]
	if !strings.Contains(view, "[  ]") {
		t.Errorf("expected view to contain [  ] for dead node, got:\n%s", view)
	}

	// Status distinction
	// c2 (RegionBlocked) must display BLOCKED, NOT red FAIL
	if !strings.Contains(view, "BLOCKED") {
		t.Errorf("expected view to contain 'BLOCKED' for RegionBlocked node, got:\n%s", view)
	}
	// c3 (Dead) must display FAIL
	if !strings.Contains(view, "FAIL") {
		t.Errorf("expected view to contain 'FAIL' for dead node, got:\n%s", view)
	}
}

func TestModel_DetailRendersDualServabilityAndTransport(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	now := time.Now()
	link := "vless://c1@1.1.1.1:443#BlockedDetail"
	st.PutWithTransition(store.Result{
		Link:                   link,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		Reason:                 "location not supported",
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       88 * time.Millisecond,
		TestedAt:               now,
	})

	// Refresh and open detail (press Enter)
	updated, _ := m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	view := m.View()
	if !strings.Contains(view, "CANDIDATE INSPECTION") {
		t.Fatalf("expected CANDIDATE INSPECTION view, got:\n%s", view)
	}

	// Servable line: Gemini: NO ... Generic: YES
	if !strings.Contains(view, "Gemini: NO") || !strings.Contains(view, "Generic: YES") {
		t.Errorf("expected detail to render dual servability 'Gemini: NO' and 'Generic: YES', got:\n%s", view)
	}

	// Transport line: Status=OK  Latency=88ms  Evidence=explicit
	if !strings.Contains(view, "Transport: Status=OK") || !strings.Contains(view, "Latency=88ms") || !strings.Contains(view, "Evidence=explicit") {
		t.Errorf("expected detail to render Stage 1 transport inspection line, got:\n%s", view)
	}
}

func TestModel_FilterModeCycling(t *testing.T) {
	m, _, _, _ := setupTestModel(t)

	// Initial view: [All]
	view0 := m.View()
	if !strings.Contains(view0, "[s] Filter [All]") {
		t.Errorf("expected initial filter hint '[s] Filter [All]', got:\n%s", view0)
	}

	// 1st press 's' -> Filter: Gemini servable
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(*tui.Model)
	view1 := m.View()
	if !strings.Contains(view1, "[Filter: Gemini servable]") || !strings.Contains(view1, "[s] Filter [Gemini]") {
		t.Errorf("expected Gemini servable filter state, got:\n%s", view1)
	}

	// 2nd press 's' -> Filter: Generic servable
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(*tui.Model)
	view2 := m.View()
	if !strings.Contains(view2, "[Filter: Generic servable]") || !strings.Contains(view2, "[s] Filter [Generic]") {
		t.Errorf("expected Generic servable filter state, got:\n%s", view2)
	}

	// 3rd press 's' -> Filter: All candidates
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(*tui.Model)
	view3 := m.View()
	if !strings.Contains(view3, "[Filter: All candidates]") || !strings.Contains(view3, "[s] Filter [All]") {
		t.Errorf("expected All candidates filter state, got:\n%s", view3)
	}
}

func TestModel_VirtualizationScrollAndInspection(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	// Populate 100 candidates
	for i := 0; i < 100; i++ {
		link := "vless://cand" + string(rune('A'+i%26)) + "@1.1.1.1:443#Node-" + strings.Repeat("x", i%5)
		st.PutWithTransition(store.Result{
			Link:     link,
			Status:   store.StatusPassed,
			Latency:  time.Duration(100+i) * time.Millisecond,
			TestedAt: time.Now(),
		})
	}

	// Trigger initial snapshot via tick
	updated, _ := m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)

	// 1. Move cursor to bottom using 'G'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(*tui.Model)

	// 2. Open detail on the selected row
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)
	viewDetail := m.View()
	if !strings.Contains(viewDetail, "CANDIDATE INSPECTION") {
		t.Fatalf("expected detail view after pressing Enter on virtualized row, got:\n%s", viewDetail)
	}

	// 3. Close detail
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(*tui.Model)

	// 4. Jump back to top using 'g'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(*tui.Model)

	// 5. Move down with 'j' and pgdown
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = updated.(*tui.Model)

	// 6. View should render candidate table normally without line wrapping
	viewNormal := m.View()
	if !strings.Contains(viewNormal, "SERV") || !strings.Contains(viewNormal, "ENDPOINT") {
		t.Errorf("expected table view, got:\n%s", viewNormal)
	}
}

func TestModel_DynamicMembershipClamping(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	// Populate 10 candidates
	for i := 0; i < 10; i++ {
		link := "vless://cand" + string(rune('a'+i)) + "@1.1.1.1:443#Cand-" + string(rune('a'+i))
		st.PutWithTransition(store.Result{
			Link:     link,
			Status:   store.StatusPassed,
			Latency:  100 * time.Millisecond,
			TestedAt: time.Now(),
		})
	}

	updated, _ := m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)

	// Jump to bottom (cursor at 9)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(*tui.Model)

	// Remove all candidates by starting a new cycle with only 1 link
	st.StartCycle(map[string]struct{}{
		store.CanonicalizeLink("vless://canda@1.1.1.1:443#Cand-a"): {},
	})
	st.FinishCycle()
	st.FinishCycle()
	st.FinishCycle() // Evicts past MaxAbsentCycles=2

	// Poll via tick
	updated, _ = m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)

	// Cursor must be clamped safely to 0 (only 1 candidate left) without panicking
	view := m.View()
	if !strings.Contains(view, "SERV") {
		t.Errorf("expected table after eviction, got:\n%s", view)
	}
}

func TestModel_EmptyCandidatesView(t *testing.T) {
	m, _, _, _ := setupTestModel(t)

	view := m.View()
	if !strings.Contains(view, "No candidates in current view.") {
		t.Errorf("expected 'No candidates in current view.', got:\n%s", view)
	}

	// Pressing keys on empty table must not panic
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(*tui.Model)
}

// BenchmarkDownKey_WithScroll_50k measures the latency and allocations of pressing
// the Down arrow key when the cursor is at the viewport boundary, forcing a viewport scroll
// across a 50,000 candidate dataset.
func BenchmarkDownKey_WithScroll_50k(b *testing.B) {
	tmpDir := b.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)

	now := time.Now()
	for i := 0; i < 50000; i++ {
		link := fmt.Sprintf("vless://user-%d@1.1.1.1:443#Node-%d", i, i)
		st.PutWithTransition(store.Result{
			Link:                   link,
			Status:                 store.StatusPassed,
			Latency:                time.Duration(50+i%300) * time.Millisecond,
			TransportOK:            true,
			TransportEvidenceKnown: true,
			TransportLatency:       time.Duration(20+i%100) * time.Millisecond,
			TestedAt:               now,
		})
	}

	m := tui.New(ad)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(*tui.Model)

	// Scroll cursor to bottom of viewport so every Down key forces a scroll
	visibleRows := 32 // height 40 - 8 = 32
	for i := 0; i < visibleRows-1; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(*tui.Model)
	}

	b.Cleanup(func() {
		ad.Close()
		bus.Close()
	})

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(*tui.Model)
		output := m.View()
		if len(output) == 0 {
			b.Fatal("empty view")
		}
	}
}

var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiEscapeRe.ReplaceAllString(s, "")
}

func TestModel_DetailBoundedSampleHistoryRendering(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	now := time.Now()
	link := "vless://c1@1.1.1.1:443#DetailSampleTest"
	// Push 3 distinct samples into history
	st.PutWithTransition(store.Result{
		Link:       link,
		Status:     store.StatusInconclusive,
		Category:   store.ErrTimeout,
		Latency:    10706 * time.Millisecond,
		Attempts:   1,
		StatusCode: 0,
		TestedAt:   now.Add(-23 * time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:       link,
		Status:     store.StatusPassed,
		Category:   store.ErrNone,
		Latency:    120 * time.Millisecond,
		Attempts:   1,
		StatusCode: 200,
		TestedAt:   now.Add(-10 * time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:       link,
		Status:     store.StatusFailed,
		Category:   store.ErrTargetRateLimited,
		Latency:    0,
		Attempts:   12,
		StatusCode: 429,
		TestedAt:   now.Add(-2 * time.Minute),
	})

	updated, _ := m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	view := m.View()
	t.Logf("RENDERED DETAIL VIEW:\n%s\n", view)
	lines := strings.Split(view, "\n")

	var headerLine string
	var rowLines []string
	foundHeader := false
	for _, l := range lines {
		clean := stripANSI(l)
		if strings.Contains(clean, "Bounded Sample History") {
			continue
		}
		if strings.Contains(clean, "AGE") && strings.Contains(clean, "STATUS") && strings.Contains(clean, "CATEGORY") {
			headerLine = clean
			foundHeader = true
			continue
		}
		if foundHeader {
			if strings.TrimSpace(clean) == "" || strings.Contains(clean, "[Esc]") {
				break
			}
			rowLines = append(rowLines, clean)
		}
	}

	if !foundHeader {
		t.Fatalf("sample history header line not found in view:\n%s", view)
	}
	if len(rowLines) != 3 {
		t.Fatalf("expected 3 sample rows, got %d:\n%s", len(rowLines), strings.Join(rowLines, "\n"))
	}

	colNames := []string{"#", "AGE", "STATUS", "CATEGORY", "CODE", "LATENCY", "ATTEMPTS"}
	headerColIndices := make(map[string]int)
	for _, name := range colNames {
		idx := strings.Index(headerLine, name)
		if idx == -1 {
			t.Fatalf("column %q not found in header line: %q", name, headerLine)
		}
		headerColIndices[name] = idx
	}

	expectedValues := [][]string{
		{"1", "23m ago", "inconclusive", "timeout", "0", "10.706s", "1"},
		{"2", "10m ago", "passed", "none", "200", "120ms", "1"},
		{"3", "2m ago", "failed", "target_rate_limited", "429", "---", "12"},
	}

	for rowIdx, row := range rowLines {
		if strings.HasPrefix(row, strings.Repeat(" ", 20)) {
			t.Errorf("row %d has excessive leading whitespace (misaligned to right): %q", rowIdx+1, row)
		}
		for cIdx, colName := range colNames {
			val := expectedValues[rowIdx][cIdx]
			expectedCol := headerColIndices[colName]
			if expectedCol+len(val) > len(row) {
				t.Errorf("row %d column %q (%q): row line too short (len %d), expected start at %d\nHeader: %s\nRow:    %s",
					rowIdx+1, colName, val, len(row), expectedCol, headerLine, row)
				continue
			}
			actualVal := row[expectedCol : expectedCol+len(val)]
			if actualVal != val {
				t.Errorf("row %d column %q: expected %q at column %d, got %q\nHeader: %s\nRow:    %s",
					rowIdx+1, colName, val, expectedCol, actualVal, headerLine, row)
			}
		}
	}
}

func TestModel_DetailBoundedSampleHistoryRendering_ANSIColor(t *testing.T) {
	// Enable ANSI color output explicitly
	prevProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(prevProfile)

	m, st, _, _ := setupTestModel(t)

	now := time.Now()
	link := "vless://c1@1.1.1.1:443#DetailSampleTest"
	st.PutWithTransition(store.Result{
		Link:       link,
		Status:     store.StatusPassed,
		Category:   store.ErrNone,
		Latency:    120 * time.Millisecond,
		Attempts:   1,
		StatusCode: 200,
		TestedAt:   now.Add(-10 * time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:       link,
		Status:     store.StatusFailed,
		Category:   store.ErrTimeout,
		Latency:    5000 * time.Millisecond,
		Attempts:   2,
		StatusCode: 0,
		TestedAt:   now.Add(-5 * time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:       link,
		Status:     store.StatusInconclusive,
		Category:   store.ErrProxyRateLimited,
		Latency:    10706 * time.Millisecond,
		Attempts:   3,
		StatusCode: 429,
		TestedAt:   now.Add(-1 * time.Minute),
	})

	updated, _ := m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	view := m.View()
	lines := strings.Split(view, "\n")

	var headerLine string
	var rowLines []string
	foundHeader := false
	for _, l := range lines {
		clean := stripANSI(l)
		if strings.Contains(clean, "Bounded Sample History") {
			continue
		}
		if strings.Contains(clean, "AGE") && strings.Contains(clean, "STATUS") && strings.Contains(clean, "CATEGORY") {
			headerLine = clean
			foundHeader = true
			continue
		}
		if foundHeader {
			if strings.TrimSpace(clean) == "" || strings.Contains(clean, "[Esc]") {
				break
			}
			rowLines = append(rowLines, clean)
		}
	}

	if !foundHeader {
		t.Fatalf("sample history header line not found in view:\n%s", view)
	}
	if len(rowLines) != 3 {
		t.Fatalf("expected 3 sample rows, got %d:\n%s", len(rowLines), strings.Join(rowLines, "\n"))
	}

	colNames := []string{"#", "AGE", "STATUS", "CATEGORY", "CODE", "LATENCY", "ATTEMPTS"}
	headerColIndices := make(map[string]int)
	for _, name := range colNames {
		idx := strings.Index(headerLine, name)
		if idx == -1 {
			t.Fatalf("column %q not found in header line: %q", name, headerLine)
		}
		headerColIndices[name] = idx
	}

	expectedValues := [][]string{
		{"1", "10m ago", "passed", "none", "200", "120ms", "1"},
		{"2", "5m ago", "failed", "timeout", "0", "5s", "2"},
		{"3", "1m ago", "inconclusive", "proxy_rate_limited", "429", "10.706s", "3"},
	}

	for rowIdx, row := range rowLines {
		for cIdx, colName := range colNames {
			val := expectedValues[rowIdx][cIdx]
			expectedCol := headerColIndices[colName]
			if expectedCol+len(val) > len(row) {
				t.Errorf("row %d column %q (%q): row line too short (len %d), expected start at %d\nHeader: %s\nRow:    %s",
					rowIdx+1, colName, val, len(row), expectedCol, headerLine, row)
				continue
			}
			actualVal := row[expectedCol : expectedCol+len(val)]
			if actualVal != val {
				t.Errorf("row %d column %q: expected %q at column %d, got %q\nHeader: %s\nRow:    %s",
					rowIdx+1, colName, val, expectedCol, actualVal, headerLine, row)
			}
		}
	}
}

func TestModel_DetailBoundedSampleHistoryRendering_FitsMinimumTerminalWidth(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	now := time.Now()
	link := "vless://c1@1.1.1.1:443#DetailSampleTest"
	st.PutWithTransition(store.Result{
		Link:       link,
		Status:     store.StatusInconclusive,
		Category:   store.ErrTargetRateLimited, // 19 chars (longest standard category)
		Latency:    10706 * time.Millisecond,
		Attempts:   99,
		StatusCode: 429,
		TestedAt:   now.Add(-23 * time.Minute),
	})

	// Minimum supported terminal geometry is 80x24
	const minWidth = 80
	updated, _ := m.Update(tea.WindowSizeMsg{Width: minWidth, Height: 24})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	view := m.View()
	lines := strings.Split(view, "\n")

	var historyLines []string
	inHistory := false
	for _, l := range lines {
		clean := stripANSI(l)
		if strings.Contains(clean, "Bounded Sample History") {
			inHistory = true
		}
		if inHistory {
			if strings.TrimSpace(clean) == "" || strings.Contains(clean, "[Esc]") {
				break
			}
			historyLines = append(historyLines, clean)
		}
	}

	if len(historyLines) < 2 {
		t.Fatalf("expected at least header and row in history, got %d lines", len(historyLines))
	}

	// Verify every history line fits strictly within minWidth without wrapping
	for _, l := range historyLines {
		if len(l) > minWidth {
			t.Errorf("history line exceeds minimum terminal width %d (len %d): %q", minWidth, len(l), l)
		}
	}
}

func TestModel_DetailBoundedSampleHistoryRendering_MultiDigitAndClamping(t *testing.T) {
	m, st, _, _ := setupTestModel(t)

	now := time.Now()
	link := "vless://c1@1.1.1.1:443#DetailSampleTest"
	// Push 10 samples to test multi-digit index (10) and multi-digit attempts (e.g. 99)
	for i := 1; i <= 9; i++ {
		st.PutWithTransition(store.Result{
			Link:       link,
			Status:     store.StatusPassed,
			Category:   store.ErrNone,
			Latency:    time.Duration(i*10) * time.Millisecond,
			Attempts:   1,
			StatusCode: 200,
			TestedAt:   now.Add(time.Duration(-10+i) * time.Minute),
		})
	}
	// 10th sample: index 10, attempts 99
	st.PutWithTransition(store.Result{
		Link:       link,
		Status:     store.StatusFailed,
		Category:   store.ErrTargetRateLimited, // 19 chars
		Latency:    9999 * time.Millisecond,
		Attempts:   99,
		StatusCode: 429,
		TestedAt:   now,
	})

	updated, _ := m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	view := m.View()
	lines := strings.Split(view, "\n")

	var headerLine string
	var row10 string
	foundHeader := false
	for _, l := range lines {
		clean := stripANSI(l)
		if strings.Contains(clean, "AGE") && strings.Contains(clean, "STATUS") {
			headerLine = clean
			foundHeader = true
			continue
		}
		if foundHeader && strings.Contains(clean, "10 ") {
			row10 = clean
			break
		}
	}

	if headerLine == "" || row10 == "" {
		t.Fatalf("failed to locate header or row 10 in view:\n%s", view)
	}

	// In row 10, check that column # starts with "10"
	idxCol := strings.Index(headerLine, "#")
	if row10[idxCol:idxCol+2] != "10" {
		t.Errorf("row 10 index expected '10' at col %d, got %q\nHeader: %s\nRow:    %s",
			idxCol, row10[idxCol:idxCol+2], headerLine, row10)
	}

	// AGE must start at exact header AGE column
	ageCol := strings.Index(headerLine, "AGE")
	if !strings.HasPrefix(row10[ageCol:], "0s ago") {
		t.Errorf("row 10 AGE expected '0s ago' at col %d\nHeader: %s\nRow:    %s",
			ageCol, headerLine, row10)
	}

	// STATUS must start at exact header STATUS column
	statusCol := strings.Index(headerLine, "STATUS")
	if !strings.HasPrefix(row10[statusCol:], "failed") {
		t.Errorf("row 10 STATUS expected 'failed' at col %d\nHeader: %s\nRow:    %s",
			statusCol, headerLine, row10)
	}

	// CATEGORY must start at exact header CATEGORY column
	catCol := strings.Index(headerLine, "CATEGORY")
	if !strings.HasPrefix(row10[catCol:], "target_rate_limited") {
		t.Errorf("row 10 CATEGORY expected 'target_rate_limited' at col %d\nHeader: %s\nRow:    %s",
			catCol, headerLine, row10)
	}

	// CODE must start at exact header CODE column
	codeCol := strings.Index(headerLine, "CODE")
	if !strings.HasPrefix(row10[codeCol:], "429") {
		t.Errorf("row 10 CODE expected '429' at col %d\nHeader: %s\nRow:    %s",
			codeCol, headerLine, row10)
	}

	// LATENCY must start at exact header LATENCY column
	latCol := strings.Index(headerLine, "LATENCY")
	if !strings.HasPrefix(row10[latCol:], "9.999s") {
		t.Errorf("row 10 LATENCY expected '9.999s' at col %d\nHeader: %s\nRow:    %s",
			latCol, headerLine, row10)
	}

	// ATTEMPTS must start at exact header ATTEMPTS column
	attCol := strings.Index(headerLine, "ATTEMPTS")
	if !strings.HasPrefix(row10[attCol:], "99") {
		t.Errorf("row 10 ATTEMPTS expected '99' at col %d\nHeader: %s\nRow:    %s",
			attCol, headerLine, row10)
	}
}

func TestModel_ConfigCenterViewSwitching(t *testing.T) {
	m, _, _, _ := setupTestModel(t)

	// 1. Initial view is Candidates view
	view0 := m.View()
	if !strings.Contains(view0, "SERV") || !strings.Contains(view0, "ENDPOINT") {
		t.Fatalf("expected candidate table initially, got:\n%s", view0)
	}
	if !strings.Contains(view0, "[c] Config") {
		t.Errorf("expected '[c] Config' hint in Candidates footer, got:\n%s", view0)
	}

	// 2. Press 'c' from Candidates -> switches to ViewConfig
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(*tui.Model)

	viewConfig := m.View()
	if !strings.Contains(viewConfig, "CONFIGURATION CENTER") {
		t.Fatalf("expected CONFIGURATION CENTER view after pressing 'c', got:\n%s", viewConfig)
	}
	if !strings.Contains(viewConfig, "[General]") {
		t.Errorf("expected [General] active tab in config center, got:\n%s", viewConfig)
	}
	if !strings.Contains(viewConfig, "[Esc] Back") {
		t.Errorf("expected '[Esc] Back' hint in Config Center footer, got:\n%s", viewConfig)
	}

	// 3. Press 'Esc' from ViewConfig -> returns cleanly to Candidates
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(*tui.Model)

	viewBack := m.View()
	if !strings.Contains(viewBack, "SERV") || !strings.Contains(viewBack, "ENDPOINT") {
		t.Fatalf("expected return to Candidates table after Esc, got:\n%s", viewBack)
	}
	if strings.Contains(viewBack, "CONFIGURATION CENTER") {
		t.Errorf("did not expect CONFIGURATION CENTER after Esc, got:\n%s", viewBack)
	}

	// 4. Press 'Tab' to switch to Logs view
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	viewLogs := m.View()
	if !strings.Contains(viewLogs, "LOG VIEWER") {
		t.Fatalf("expected LOG VIEWER after Tab, got:\n%s", viewLogs)
	}
	if !strings.Contains(viewLogs, "[c] Config") {
		t.Errorf("expected '[c] Config' hint in Logs footer, got:\n%s", viewLogs)
	}

	// 5. Press 'c' from Logs -> switches to ViewConfig
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(*tui.Model)

	viewConfigFromLogs := m.View()
	if !strings.Contains(viewConfigFromLogs, "CONFIGURATION CENTER") {
		t.Fatalf("expected CONFIGURATION CENTER from Logs, got:\n%s", viewConfigFromLogs)
	}

	// 6. Press 'Esc' from ViewConfig -> returns cleanly to Logs
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(*tui.Model)

	viewBackToLogs := m.View()
	if !strings.Contains(viewBackToLogs, "LOG VIEWER") {
		t.Fatalf("expected return to LOG VIEWER after Esc, got:\n%s", viewBackToLogs)
	}
}

func TestModel_ConfigCenterCategoryNavigation(t *testing.T) {
	m, _, _, _ := setupTestModel(t)

	// Enter Config Center
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(*tui.Model)

	expectedOrder := []string{"General", "Sources", "Testing", "Gemini", "Scheduler", "Publishing"}

	// Verify initial category is General
	view := m.View()
	if !strings.Contains(view, "["+expectedOrder[0]+"]") {
		t.Fatalf("expected active category [%s], got:\n%s", expectedOrder[0], view)
	}

	// 1. Forward navigation with Tab
	for i := 1; i < len(expectedOrder); i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(*tui.Model)
		v := m.View()
		if !strings.Contains(v, "["+expectedOrder[i]+"]") {
			t.Errorf("after Tab %d: expected active category [%s], got:\n%s", i, expectedOrder[i], v)
		}
	}

	// Tab wraps from Publishing -> General
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)
	if v := m.View(); !strings.Contains(v, "[General]") {
		t.Errorf("expected Tab to wrap to [General], got:\n%s", v)
	}

	// 2. Forward navigation with 'l' and 'right'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = updated.(*tui.Model)
	if v := m.View(); !strings.Contains(v, "[Sources]") {
		t.Errorf("expected 'l' to navigate to [Sources], got:\n%s", v)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(*tui.Model)
	if v := m.View(); !strings.Contains(v, "[Testing]") {
		t.Errorf("expected 'right' to navigate to [Testing], got:\n%s", v)
	}

	// 3. Backward navigation with 'h' and 'left'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = updated.(*tui.Model)
	if v := m.View(); !strings.Contains(v, "[Sources]") {
		t.Errorf("expected 'h' to navigate back to [Sources], got:\n%s", v)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(*tui.Model)
	if v := m.View(); !strings.Contains(v, "[General]") {
		t.Errorf("expected 'left' to navigate back to [General], got:\n%s", v)
	}

	// 4. Backward navigation with Shift+Tab (wraps to Publishing)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(*tui.Model)
	if v := m.View(); !strings.Contains(v, "[Publishing]") {
		t.Errorf("expected Shift+Tab from General to wrap to [Publishing], got:\n%s", v)
	}

	// Shift+Tab from Publishing -> Scheduler
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(*tui.Model)
	if v := m.View(); !strings.Contains(v, "[Scheduler]") {
		t.Errorf("expected Shift+Tab from Publishing to move to [Scheduler], got:\n%s", v)
	}
}

func TestModel_ConfigCenterItemNavigation(t *testing.T) {
	m, _, _, _ := setupTestModel(t)

	// Enter Config Center
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(*tui.Model)

	// In General category, cursor should initially highlight the first item
	view0 := m.View()
	if !strings.Contains(view0, "> Listen Address") {
		t.Fatalf("expected cursor '> ' on 'Listen Address', got:\n%s", view0)
	}

	// Move cursor down: 'j'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(*tui.Model)
	view1 := m.View()
	if !strings.Contains(view1, "> Subscription Path") {
		t.Fatalf("expected cursor '> ' on 'Subscription Path', got:\n%s", view1)
	}

	// Move cursor up: 'k'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(*tui.Model)
	view2 := m.View()
	if !strings.Contains(view2, "> Listen Address") {
		t.Fatalf("expected cursor '> ' back on 'Listen Address', got:\n%s", view2)
	}

	// Jump to bottom: 'G'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(*tui.Model)
	viewBottom := m.View()
	if !strings.Contains(viewBottom, "> Headless Mode") {
		t.Fatalf("expected cursor on last item 'Headless Mode', got:\n%s", viewBottom)
	}

	// Jump to top: 'g'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(*tui.Model)
	viewTop := m.View()
	if !strings.Contains(viewTop, "> Listen Address") {
		t.Fatalf("expected cursor on first item 'Listen Address', got:\n%s", viewTop)
	}

	// Switch category: cursor index should reset to 0
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)
	viewSources := m.View()
	if !strings.Contains(viewSources, "[Sources]") {
		t.Fatalf("expected [Sources] tab, got:\n%s", viewSources)
	}
	// Cursor should be at the top of the new category
	if !strings.Contains(viewSources, "> ") {
		t.Errorf("expected cursor '> ' on first item in Sources, got:\n%s", viewSources)
	}
}

func TestModel_ConfigCenterTerminalBounds_80x24(t *testing.T) {
	m, _, _, _ := setupTestModel(t)

	// Set terminal to minimum supported geometry: 80x24
	const termWidth = 80
	const termHeight = 24
	updated, _ := m.Update(tea.WindowSizeMsg{Width: termWidth, Height: termHeight})
	m = updated.(*tui.Model)

	// Enter Config Center
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(*tui.Model)

	// Verify for all 6 categories that the output strictly conforms to 80x24 bounds
	for cat := 0; cat < viewmodel.ConfigCategoryCount; cat++ {
		view := m.View()
		cleanView := stripANSI(view)
		lines := strings.Split(cleanView, "\n")

		if len(lines) != termHeight {
			t.Errorf("category %d: expected exactly %d lines, got %d", cat, termHeight, len(lines))
		}

		for lineIdx, line := range lines {
			if w := lipgloss.Width(line); w > termWidth {
				t.Errorf("category %d line %d visual width exceeds width %d (got %d): %q",
					cat, lineIdx+1, termWidth, w, line)
			}
		}

		// Navigate to next category
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(*tui.Model)
	}
}

type testDecoupledController struct {
	configCenter viewmodel.ConfigCenterViewModel
}

func (c *testDecoupledController) Snapshot(filter viewmodel.FilterMode) viewmodel.SnapshotViewModel {
	return viewmodel.SnapshotViewModel{}
}

func (c *testDecoupledController) PollSnapshot(filter viewmodel.FilterMode) (viewmodel.SnapshotViewModel, bool) {
	return viewmodel.SnapshotViewModel{}, false
}

func (c *testDecoupledController) CandidateDetail(opaqueID string) (viewmodel.CandidateDetailViewModel, bool) {
	return viewmodel.CandidateDetailViewModel{}, false
}

func (c *testDecoupledController) CycleLogLevel() (viewmodel.LogViewModel, string) {
	return viewmodel.LogViewModel{}, "INFO"
}

func (c *testDecoupledController) CopyCandidateLink(opaqueID string) error {
	return nil
}

func (c *testDecoupledController) CandidateRowsWindow(filter viewmodel.FilterMode, offset, limit int) []viewmodel.CandidateRowViewModel {
	return nil
}

func (c *testDecoupledController) ConfigCenter() viewmodel.ConfigCenterViewModel {
	return c.configCenter
}

func (c *testDecoupledController) ToggleSource(id string) error {
	return nil
}

func (c *testDecoupledController) AddSource(rawURL string, name string) error {
	return nil
}

func (c *testDecoupledController) UpdateSource(id string, rawURL string, name string) error {
	return nil
}

func (c *testDecoupledController) DeleteSource(id string) error {
	return nil
}

func TestModel_ConfigCenterDecoupledController(t *testing.T) {
	mock := &testDecoupledController{
		configCenter: viewmodel.ConfigCenterViewModel{
			Categories: []viewmodel.ConfigCategoryViewModel{
				{
					Category: viewmodel.CategoryGeneral,
					Name:     "General",
					Items: []viewmodel.ConfigItemViewModel{
						{Label: "Mock Host", Value: "127.0.0.1:9090"},
						{Label: "Mock State", Value: "/var/tmp/mock.json"},
					},
				},
				{
					Category: viewmodel.CategorySources,
					Name:     "Sources",
					Items: []viewmodel.ConfigItemViewModel{
						{Label: "Mock Source", Value: "https://mock.example.com/sub"},
					},
				},
				{
					Category: viewmodel.CategoryTesting,
					Name:     "Testing",
					Items: []viewmodel.ConfigItemViewModel{
						{Label: "Mock Concurrency", Value: "42"},
					},
				},
				{
					Category: viewmodel.CategoryGemini,
					Name:     "Gemini",
					Items: []viewmodel.ConfigItemViewModel{
						{Label: "Mock Gemini URL", Value: "https://mock.gemini.dev"},
					},
				},
				{
					Category: viewmodel.CategoryScheduler,
					Name:     "Scheduler",
					Items: []viewmodel.ConfigItemViewModel{
						{Label: "Mock Daemon", Value: "Running Active"},
					},
				},
				{
					Category: viewmodel.CategoryPublishing,
					Name:     "Publishing",
					Items: []viewmodel.ConfigItemViewModel{
						{Label: "Mock Pub", Value: "Published 99 times"},
					},
				},
			},
		},
	}

	m := tui.New(mock)

	// Enter Config Center
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(*tui.Model)

	view := m.View()
	if !strings.Contains(view, "Mock Host") || !strings.Contains(view, "127.0.0.1:9090") {
		t.Errorf("expected view to render decoupled mock data 'Mock Host: 127.0.0.1:9090', got:\n%s", view)
	}

	// Switch to Sources
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)
	viewSources := m.View()
	if !strings.Contains(viewSources, "Mock Source") || !strings.Contains(viewSources, "https://mock.example.com/sub") {
		t.Errorf("expected view to render decoupled mock data for Sources, got:\n%s", viewSources)
	}
}

func TestModel_ConfigCenter_LiveRefreshWhileInViewConfig(t *testing.T) {
	t.Setenv("COLORTERM", "")
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	st := store.New(stateFile, 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)

	ad := adapter.New(st, bus, ring)
	ad.Subscribe()
	t.Cleanup(func() {
		ad.Close()
		bus.Close()
	})

	m := tui.New(ad)

	// 1. Enter ViewConfig
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(*tui.Model)

	// Verify we are in ViewConfig
	view := m.View()
	if !strings.Contains(view, "CONFIGURATION CENTER") {
		t.Fatalf("expected CONFIGURATION CENTER, got:\n%s", view)
	}

	// 2. Navigate to Scheduler category (Tab x4 from General: Sources -> Testing -> Gemini -> Scheduler)
	for i := 0; i < 4; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(*tui.Model)
	}
	if v := m.View(); !strings.Contains(v, "[Scheduler]") {
		t.Fatalf("expected [Scheduler] tab, got:\n%s", v)
	}
	if v := m.View(); strings.Contains(v, "77 candidates") {
		t.Fatalf("unexpected '77 candidates' before ConfigUpdated event, got:\n%s", v)
	}

	// 3. Emit ConfigUpdated while remaining in ViewConfig
	bus.Publish(events.ConfigUpdated{
		Old: config.Config{ProbeLimit: 0},
		New: config.Config{ProbeLimit: 77, FetchIntervalRaw: "7m"},
	})

	// Wait briefly for the adapter's event loop to mark dirty
	time.Sleep(50 * time.Millisecond)

	// Send TickMsg to trigger presentation polling and refresh
	updated, _ = m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)

	schedView := m.View()
	if !strings.Contains(schedView, "77 candidates") {
		t.Errorf("expected live refresh to show '77 candidates' while in ViewConfig, got:\n%s", schedView)
	}
	if !strings.Contains(schedView, "7m") {
		t.Errorf("expected live refresh to show '7m' interval while in ViewConfig, got:\n%s", schedView)
	}

	// 4. Navigate to Publishing category (Tab x1: Scheduler -> Publishing)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)
	if v := m.View(); !strings.Contains(v, "[Publishing]") {
		t.Fatalf("expected [Publishing] tab, got:\n%s", v)
	}

	// 5. Emit PublishingStarted while remaining in ViewConfig
	bus.Publish(events.PublishingStarted{
		StartedAt:  time.Now(),
		Repository: "test-repo",
		Branch:     "main",
	})
	time.Sleep(50 * time.Millisecond)

	updated, _ = m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)
	pubView1 := m.View()
	if !strings.Contains(pubView1, "Publishing...") {
		t.Errorf("expected live refresh to show 'Publishing...' on PublishingStarted, got:\n%s", pubView1)
	}

	// 6. Emit PublishingFinished while remaining in ViewConfig
	bus.Publish(events.PublishingFinished{
		StartedAt:  time.Now().Add(-1 * time.Second),
		FinishedAt: time.Now(),
		Duration:   time.Second,
		Repository: "test-repo",
		Branch:     "main",
	})
	time.Sleep(50 * time.Millisecond)

	updated, _ = m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)
	pubView2 := m.View()
	if !strings.Contains(pubView2, "Idle") {
		t.Errorf("expected live refresh to show 'Idle' on PublishingFinished, got:\n%s", pubView2)
	}
	if !strings.Contains(pubView2, "1 succeeded") {
		t.Errorf("expected live refresh to show '1 succeeded' on PublishingFinished, got:\n%s", pubView2)
	}

	// 7. Emit PublishingFailed while remaining in ViewConfig
	bus.Publish(events.PublishingFailed{
		StartedAt:  time.Now().Add(-1 * time.Second),
		FailedAt:   time.Now(),
		Duration:   time.Second,
		Repository: "test-repo",
		Branch:     "main",
		Error:      "git remote rejected credentials for https://oauth2:secretToken@github.com/test/repo",
	})
	time.Sleep(50 * time.Millisecond)

	updated, _ = m.Update(tui.TickMsg(time.Now()))
	m = updated.(*tui.Model)
	pubView3 := m.View()
	if !strings.Contains(pubView3, "1 failed") {
		t.Errorf("expected live refresh to show '1 failed' on PublishingFailed, got:\n%s", pubView3)
	}
	if !strings.Contains(pubView3, "git remote rejected") {
		t.Errorf("expected live refresh to display sanitized error on PublishingFailed, got:\n%s", pubView3)
	}
	if strings.Contains(pubView3, "secretToken") {
		t.Errorf("CRITICAL: secretToken leaked in failure message: %s", pubView3)
	}
}

type testSourceMockController struct {
	testDecoupledController
	sources []viewmodel.SourceItemViewModel

	toggledID   string
	addedURL    string
	addedName   string
	updatedID   string
	updatedURL  string
	updatedName string
	deletedID   string

	toggleErr error
	addErr    error
	updateErr error
	deleteErr error
}

func newTestSourceMockController() *testSourceMockController {
	c := &testSourceMockController{
		sources: []viewmodel.SourceItemViewModel{
			{ID: "src-1", Name: "Primary Feed", URL: "https://sub1.example.com/feed", Enabled: true, CandidateCount: 15, HasCount: true},
			{ID: "src-2", Name: "Backup Feed", URL: "https://sub2.example.com/feed", Enabled: false, CandidateCount: 0, HasCount: false},
		},
	}
	c.syncConfigCenter()
	return c
}

func (c *testSourceMockController) syncConfigCenter() {
	c.configCenter = viewmodel.ConfigCenterViewModel{
		Categories: []viewmodel.ConfigCategoryViewModel{
			{Category: viewmodel.CategoryGeneral, Name: "General"},
			{Category: viewmodel.CategorySources, Name: "Sources", Sources: c.sources},
			{Category: viewmodel.CategoryTesting, Name: "Testing"},
			{Category: viewmodel.CategoryGemini, Name: "Gemini"},
			{Category: viewmodel.CategoryScheduler, Name: "Scheduler"},
			{Category: viewmodel.CategoryPublishing, Name: "Publishing"},
		},
	}
}

func (c *testSourceMockController) ConfigCenter() viewmodel.ConfigCenterViewModel {
	return c.configCenter
}

func (c *testSourceMockController) ToggleSource(id string) error {
	c.toggledID = id
	if c.toggleErr != nil {
		return c.toggleErr
	}
	for i := range c.sources {
		if c.sources[i].ID == id {
			c.sources[i].Enabled = !c.sources[i].Enabled
			break
		}
	}
	c.syncConfigCenter()
	return nil
}

func (c *testSourceMockController) AddSource(rawURL string, name string) error {
	c.addedURL = rawURL
	c.addedName = name
	if c.addErr != nil {
		return c.addErr
	}
	c.sources = append(c.sources, viewmodel.SourceItemViewModel{
		ID:      fmt.Sprintf("src-%d", len(c.sources)+1),
		Name:    name,
		URL:     rawURL,
		Enabled: true,
	})
	c.syncConfigCenter()
	return nil
}

func (c *testSourceMockController) UpdateSource(id string, rawURL string, name string) error {
	c.updatedID = id
	c.updatedURL = rawURL
	c.updatedName = name
	if c.updateErr != nil {
		return c.updateErr
	}
	for i := range c.sources {
		if c.sources[i].ID == id {
			if rawURL != "" {
				c.sources[i].URL = rawURL
			}
			if name != "" {
				c.sources[i].Name = name
			}
			break
		}
	}
	c.syncConfigCenter()
	return nil
}

func (c *testSourceMockController) DeleteSource(id string) error {
	c.deletedID = id
	if c.deleteErr != nil {
		return c.deleteErr
	}
	for i := range c.sources {
		if c.sources[i].ID == id {
			c.sources = append(c.sources[:i], c.sources[i+1:]...)
			break
		}
	}
	c.syncConfigCenter()
	return nil
}

func TestModel_SourceManager_ListRenderingAndNavigation(t *testing.T) {
	mock := newTestSourceMockController()
	m := tui.New(mock)

	// Enter Config Center
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*tui.Model)

	// Navigate to Sources category (tab from General)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	view := m.View()

	// Verify badges, names, URLs, and telemetry
	if !strings.Contains(view, "[ENABLED]") {
		t.Errorf("expected view to contain [ENABLED] badge, got:\n%s", view)
	}
	if !strings.Contains(view, "[DISABLED]") {
		t.Errorf("expected view to contain [DISABLED] badge, got:\n%s", view)
	}
	if !strings.Contains(view, "Primary Feed") {
		t.Errorf("expected view to contain 'Primary Feed', got:\n%s", view)
	}
	if !strings.Contains(view, "Backup Feed") {
		t.Errorf("expected view to contain 'Backup Feed', got:\n%s", view)
	}
	if !strings.Contains(view, "https://sub1.example.com/feed") {
		t.Errorf("expected view to contain 'https://sub1.example.com/feed', got:\n%s", view)
	}
	// Telemetry candidate count
	if !strings.Contains(view, "15") {
		t.Errorf("expected view to contain telemetry count '15', got:\n%s", view)
	}

	// Verify footer hints for sources
	if !strings.Contains(view, "Toggle") || !strings.Contains(view, "Add") || !strings.Contains(view, "Delete") {
		t.Errorf("expected footer to show source management controls, got:\n%s", view)
	}
}

func TestModel_SourceManager_ToggleSource(t *testing.T) {
	mock := newTestSourceMockController()
	m := tui.New(mock)

	// Enter Config Center and navigate to Sources
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	// Press Space to toggle Primary Feed (src-1) from enabled to disabled
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(*tui.Model)

	if mock.toggledID != "src-1" {
		t.Fatalf("expected ToggleSource called with src-1, got %q", mock.toggledID)
	}
	if mock.sources[0].Enabled != false {
		t.Errorf("expected src-1 to be disabled in mock")
	}

	view := m.View()
	if strings.Contains(view, "Source disabled") == false && strings.Contains(view, "disabled") == false {
		t.Errorf("expected status or view to reflect disabled state, got:\n%s", view)
	}
}

func TestModel_SourceManager_DeleteSource(t *testing.T) {
	mock := newTestSourceMockController()
	m := tui.New(mock)

	// Enter Config Center and navigate to Sources
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	// Press 'd' to delete selected source (src-1)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = updated.(*tui.Model)

	// Should prompt for confirmation, NOT delete yet
	if mock.deletedID != "" {
		t.Fatalf("DeleteSource was called without confirmation")
	}
	view := m.View()
	if !strings.Contains(view, "confirm") && !strings.Contains(view, "y") {
		t.Errorf("expected confirmation prompt for deletion, got:\n%s", view)
	}

	// Press 'n' to cancel
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(*tui.Model)
	if mock.deletedID != "" {
		t.Fatalf("DeleteSource called despite cancellation")
	}

	// Press 'd' again, then 'y' to confirm
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(*tui.Model)

	if mock.deletedID != "src-1" {
		t.Fatalf("expected DeleteSource called with src-1, got %q", mock.deletedID)
	}
	if len(mock.sources) != 1 {
		t.Errorf("expected 1 source remaining, got %d", len(mock.sources))
	}
}

func TestModel_SourceManager_AddSource(t *testing.T) {
	mock := newTestSourceMockController()
	m := tui.New(mock)

	// Enter Config Center and navigate to Sources
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	// Press 'a' to enter Add mode
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*tui.Model)

	// View should show Add Source prompt
	view := m.View()
	if !strings.Contains(view, "URL") && !strings.Contains(view, "Add") {
		t.Errorf("expected Add Source form/prompt in view, got:\n%s", view)
	}

	// Type URL
	for _, r := range "https://new.example.com/sub" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(*tui.Model)
	}

	// Press Tab to switch to Name field
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	// Type Name
	for _, r := range "Tertiary Feed" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(*tui.Model)
	}

	// Press Enter to submit
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	if mock.addedURL != "https://new.example.com/sub" {
		t.Errorf("expected addedURL 'https://new.example.com/sub', got %q", mock.addedURL)
	}
	if mock.addedName != "Tertiary Feed" {
		t.Errorf("expected addedName 'Tertiary Feed', got %q", mock.addedName)
	}
	if len(mock.sources) != 3 {
		t.Errorf("expected 3 sources, got %d", len(mock.sources))
	}
}

func TestModel_SourceManager_EditSource(t *testing.T) {
	mock := newTestSourceMockController()
	m := tui.New(mock)

	// Enter Config Center and navigate to Sources
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	// Press Enter to edit selected source (src-1)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	view := m.View()
	if !strings.Contains(view, "Edit") && !strings.Contains(view, "Primary Feed") {
		t.Errorf("expected Edit Source form in view, got:\n%s", view)
	}

	// Switch to Name field
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	// Backspace to clear and type new name
	for i := 0; i < 20; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = updated.(*tui.Model)
	}
	for _, r := range "Renamed Feed" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(*tui.Model)
	}

	// Press Enter to save
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	if mock.updatedID != "src-1" {
		t.Errorf("expected updatedID 'src-1', got %q", mock.updatedID)
	}
	if mock.updatedName != "Renamed Feed" {
		t.Errorf("expected updatedName 'Renamed Feed', got %q", mock.updatedName)
	}
}

func TestModel_SourceManager_ErrorFeedback(t *testing.T) {
	mock := newTestSourceMockController()
	mock.addErr = fmt.Errorf("duplicate source URL")
	m := tui.New(mock)

	// Enter Config Center and navigate to Sources
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	// Press 'a' to add
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*tui.Model)

	// Type URL and submit
	for _, r := range "https://dup.com" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(*tui.Model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)

	view := m.View()
	if !strings.Contains(view, "duplicate source URL") {
		t.Errorf("expected error feedback 'duplicate source URL' in view, got:\n%s", view)
	}
}

func TestModel_SourceManager_EdgeCases(t *testing.T) {
	mock := newTestSourceMockController()
	m := tui.New(mock)

	// Enter Config Center and navigate to Sources
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	// 1. Toggle with 'e'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(*tui.Model)
	if mock.toggledID != "src-1" {
		t.Errorf("expected toggle with 'e' key to toggle src-1, got %q", mock.toggledID)
	}

	// 2. Delete with 'x' then cancel
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(*tui.Model)
	view := m.View()
	if !strings.Contains(view, "Delete source") {
		t.Errorf("expected delete prompt with 'x' key, got:\n%s", view)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*tui.Model)
	view = m.View()
	if strings.Contains(view, "Delete source") {
		t.Errorf("expected delete prompt to be dismissed on Esc")
	}

	// 3. Add mode: empty URL submission
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)
	view = m.View()
	if !strings.Contains(view, "URL cannot be empty") {
		t.Errorf("expected empty URL validation error, got:\n%s", view)
	}

	// 4. Add mode: Esc cancels
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*tui.Model)
	view = m.View()
	if strings.Contains(view, "Add Subscription Source") {
		t.Errorf("expected Add prompt dismissed on Esc")
	}

	// 5. Edit mode: Esc cancels
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*tui.Model)
	view = m.View()
	if !strings.Contains(view, "Edit Subscription Source") {
		t.Errorf("expected Edit prompt on Enter, got:\n%s", view)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*tui.Model)
	view = m.View()
	if strings.Contains(view, "Edit Subscription Source") {
		t.Errorf("expected Edit prompt dismissed on Esc")
	}

	// 6. Navigation: j, k, g, G
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = updated.(*tui.Model)

	// 7. Terminal bounds test: 80x24
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*tui.Model)
	view = m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 24 {
		t.Errorf("expected at most 24 lines in 80x24 terminal, got %d", len(lines))
	}
}

func TestModel_SourceManager_InputFocusNavigation(t *testing.T) {
	modes := []struct {
		name      string
		enterKey  tea.KeyMsg
		promptKey string
	}{
		{
			name:      "AddMode",
			enterKey:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")},
			promptKey: "Add Subscription Source",
		},
		{
			name:      "EditMode",
			enterKey:  tea.KeyMsg{Type: tea.KeyEnter},
			promptKey: "Edit Subscription Source",
		},
	}

	for _, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			mock := newTestSourceMockController()
			m := tui.New(mock)

			// Enter Config Center and navigate to Sources category
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
			m = updated.(*tui.Model)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = updated.(*tui.Model)

			// Enter mode (Add or Edit)
			updated, _ = m.Update(tc.enterKey)
			m = updated.(*tui.Model)

			// Step 1: Start on URL (focus 0)
			if focus := m.SourceInputFocusForTest(); focus != 0 {
				t.Fatalf("step 1: expected initial focus 0 (URL), got %d", focus)
			}
			view := stripANSI(m.View())
			if !strings.Contains(view, "> URL:") || strings.Contains(view, "> Name:") {
				t.Errorf("step 1: expected focus indicator on URL, got:\n%s", view)
			}

			// Step 2: Tab -> Name (focus 1)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = updated.(*tui.Model)
			if focus := m.SourceInputFocusForTest(); focus != 1 {
				t.Fatalf("step 2 (Tab): expected focus 1 (Name), got %d", focus)
			}
			view = stripANSI(m.View())
			if !strings.Contains(view, "> Name:") || strings.Contains(view, "> URL:") {
				t.Errorf("step 2: expected focus indicator on Name, got:\n%s", view)
			}

			// Step 3: Shift+Tab -> URL (focus 0)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
			m = updated.(*tui.Model)
			if focus := m.SourceInputFocusForTest(); focus != 0 {
				t.Fatalf("step 3 (Shift+Tab): expected focus 0 (URL), got %d", focus)
			}
			view = stripANSI(m.View())
			if !strings.Contains(view, "> URL:") || strings.Contains(view, "> Name:") {
				t.Errorf("step 3: expected focus indicator on URL, got:\n%s", view)
			}

			// Step 4: Tab -> Name (focus 1)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = updated.(*tui.Model)
			if focus := m.SourceInputFocusForTest(); focus != 1 {
				t.Fatalf("step 4 (Tab): expected focus 1 (Name), got %d", focus)
			}
			view = stripANSI(m.View())
			if !strings.Contains(view, "> Name:") || strings.Contains(view, "> URL:") {
				t.Errorf("step 4: expected focus indicator on Name, got:\n%s", view)
			}

			// Step 5: Up -> URL (focus 0)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
			m = updated.(*tui.Model)
			if focus := m.SourceInputFocusForTest(); focus != 0 {
				t.Fatalf("step 5 (Up): expected focus 0 (URL), got %d", focus)
			}
			view = stripANSI(m.View())
			if !strings.Contains(view, "> URL:") || strings.Contains(view, "> Name:") {
				t.Errorf("step 5: expected focus indicator on URL, got:\n%s", view)
			}

			// Step 6: Down -> Name (focus 1)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m = updated.(*tui.Model)
			if focus := m.SourceInputFocusForTest(); focus != 1 {
				t.Fatalf("step 6 (Down): expected focus 1 (Name), got %d", focus)
			}
			view = stripANSI(m.View())
			if !strings.Contains(view, "> Name:") || strings.Contains(view, "> URL:") {
				t.Errorf("step 6: expected focus indicator on Name, got:\n%s", view)
			}

			// Step 7: Up -> URL (focus 0)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
			m = updated.(*tui.Model)
			if focus := m.SourceInputFocusForTest(); focus != 0 {
				t.Fatalf("step 7 (Up): expected focus 0 (URL), got %d", focus)
			}

			// Step 8: Up from URL -> Name (focus 1, modulo-safe reverse wrap)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
			m = updated.(*tui.Model)
			if focus := m.SourceInputFocusForTest(); focus != 1 {
				t.Fatalf("step 8 (Up wrap): expected focus 1 (Name), got %d", focus)
			}

			// Step 9: Shift+Tab -> URL (focus 0)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
			m = updated.(*tui.Model)
			if focus := m.SourceInputFocusForTest(); focus != 0 {
				t.Fatalf("step 9 (Shift+Tab): expected focus 0 (URL), got %d", focus)
			}

			// Step 10: Shift+Tab from URL -> Name (focus 1, modulo-safe reverse wrap)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
			m = updated.(*tui.Model)
			if focus := m.SourceInputFocusForTest(); focus != 1 {
				t.Fatalf("step 10 (Shift+Tab wrap): expected focus 1 (Name), got %d", focus)
			}
		})
	}
}

func TestModel_Footer_TerminalWidthSafeTruncation_UnicodeAndANSI(t *testing.T) {
	mock := newTestSourceMockController()
	m := tui.New(mock)

	// Set status message containing multi-byte Unicode runes and emojis
	unicodeStatus := "Status: 🚀 Привет سلام دنیا 世界 🎉"
	m.SetStatusMessageForTest(unicodeStatus)

	testWidths := []int{-10, 0, 1, 2, 5, 8, 12, 17, 25, 30, 40, 55, 80, 100, 140}

	for _, w := range testWidths {
		t.Run(fmt.Sprintf("Width_%d", w), func(t *testing.T) {
			m.SetWidthForTest(w)
			footer := m.RenderFooterForTest()

			// 1. Never panic on negative or zero widths; output must be empty if w <= 0
			if w <= 0 {
				if footer != "" {
					t.Errorf("expected empty string for width %d, got %q", w, footer)
				}
				return
			}

			// 2. Output must remain valid UTF-8
			if !utf8.ValidString(footer) {
				t.Fatalf("width %d: footer contains invalid UTF-8 bytes: %q", w, footer)
			}

			// 3. Rendered terminal display width must never exceed w
			dispWidth := lipgloss.Width(footer)
			if dispWidth > w {
				t.Errorf("width %d: rendered footer display width %d exceeds terminal width", w, dispWidth)
			}

			// 4. No malformed/truncated ANSI sequence (stripping valid ANSI leaves NO raw '\x1b')
			stripped := stripANSI(footer)
			if strings.Contains(stripped, "\x1b") {
				t.Errorf("width %d: truncated footer contains dangling/malformed ANSI escape byte: %q", w, footer)
			}
		})
	}
}

func TestModel_SourceManager_UnicodeAndWidthSafety(t *testing.T) {
	mock := newTestSourceMockController()
	// Populate sources with diverse Unicode, wide CJK characters, emojis, and long URLs
	mock.sources = []viewmodel.SourceItemViewModel{
		{
			ID:             "src-fa",
			Name:           "کانال تست پروکسی 🌐",
			URL:            "https://example.com/کانال/feed?token=xyz",
			Enabled:        true,
			CandidateCount: 12,
			HasCount:       true,
		},
		{
			ID:             "src-ru",
			Name:           "Тестовый канал подписки",
			URL:            "https://ru.example.com/sub/тест",
			Enabled:        false,
			CandidateCount: 0,
			HasCount:       false,
		},
		{
			ID:             "src-cjk",
			Name:           "订阅源中文测试 🚀",
			URL:            "https://cjk.example.com/sub/测试?param=1",
			Enabled:        true,
			CandidateCount: 5,
			HasCount:       true,
		},
		{
			ID:             "src-long",
			Name:           "Super Long Source Name That Definitely Exceeds The Maximum Column Width Of Twenty Cells",
			URL:            "https://extremely-long-domain-name.example.com/very/long/path/with/lots/of/parameters?token=1234567890&user=abcdefghij&format=json",
			Enabled:        true,
			CandidateCount: 99,
			HasCount:       true,
		},
	}
	mock.syncConfigCenter()

	widths := []int{40, 50, 60, 72, 80, 100, 120}

	for _, w := range widths {
		t.Run(fmt.Sprintf("Width_%d", w), func(t *testing.T) {
			m := tui.New(mock)
			m.SetWidthForTest(w)

			// Navigate to Config Center -> Sources
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
			m = updated.(*tui.Model)
			m.SetWidthForTest(w)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = updated.(*tui.Model)
			m.SetWidthForTest(w)

			// Render source manager list
			rendered := m.RenderSourceManagerForTest()

			// 1. Output must remain valid UTF-8
			if !utf8.ValidString(rendered) {
				t.Fatalf("width %d: rendered source manager contains invalid UTF-8 bytes", w)
			}

			// 2. Output must not have malformed/dangling ANSI escapes
			stripped := stripANSI(rendered)
			if strings.Contains(stripped, "\x1b") {
				t.Errorf("width %d: rendered source manager contains dangling ANSI escape: %q", w, rendered)
			}

			// 3. Every individual line must not exceed terminal width
			lines := strings.Split(rendered, "\n")
			for lineIdx, line := range lines {
				if line == "" {
					continue
				}
				lineWidth := lipgloss.Width(line)
				if lineWidth > w {
					t.Errorf("width %d: line %d visual width %d exceeds terminal width %d: %q",
						w, lineIdx+1, lineWidth, w, line)
				}
			}

			// 4. Test delete confirm prompt with long Unicode name
			m.SetSourceModeForTest(tui.SourceModeDeleteConfirm)
			m.SetConfirmDeleteNameForTest("کانال تست فوق‌العاده طولانی برای بررسی محدودیت عرض ترمینال")
			delRendered := m.RenderSourceManagerForTest()
			if !utf8.ValidString(delRendered) {
				t.Fatalf("width %d: delete confirm contains invalid UTF-8 bytes", w)
			}
			delLines := strings.Split(delRendered, "\n")
			for lineIdx, line := range delLines {
				if line == "" {
					continue
				}
				lineWidth := lipgloss.Width(line)
				if lineWidth > w {
					t.Errorf("width %d: delete confirm line %d visual width %d exceeds terminal width %d: %q",
						w, lineIdx+1, lineWidth, w, line)
				}
			}
		})
	}
}

func TestModel_SourceManager_Modal_UnicodeAndBorders(t *testing.T) {
	mock := newTestSourceMockController()

	testCases := []struct {
		name       string
		mode       int
		url        string
		inputName  string
		focus      int
		termWidths []int
	}{
		{
			name:       "Add_Unicode_Persian_CJK_FocusedURL",
			mode:       tui.SourceModeAdd,
			url:        "https://example.com/کانال/feed_🚀",
			inputName:  "کانال فارسی 🇮🇷",
			focus:      0,
			termWidths: []int{40, 50, 72, 80, 100},
		},
		{
			name:       "Add_EmptyName_OptionalANSI_FocusedName",
			mode:       tui.SourceModeAdd,
			url:        "https://example.com/sub",
			inputName:  "",
			focus:      1,
			termWidths: []int{40, 50, 72, 80, 100},
		},
		{
			name:       "Edit_VeryLongValues",
			mode:       tui.SourceModeEdit,
			url:        "https://extremely-long-domain-name.example.com/very/long/path/with/lots/of/parameters?token=1234567890&user=abcdefghij&format=json&extra=long",
			inputName:  "Super Long Source Name That Exceeds Modal Field Width By A Significant Margin",
			focus:      0,
			termWidths: []int{40, 50, 72, 80, 100},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, w := range tc.termWidths {
				t.Run(fmt.Sprintf("Width_%d", w), func(t *testing.T) {
					m := tui.New(mock)
					m.SetWidthForTest(w)
					m.SetSourceModeForTest(tc.mode)
					m.SetSourceInputFocusForTest(tc.focus)
					m.SetSourceInputURLForTest(tc.url)
					m.SetSourceInputNameForTest(tc.inputName)

					rendered := m.RenderSourceManagerForTest()

					// 1. Output must be valid UTF-8
					if !utf8.ValidString(rendered) {
						t.Fatalf("width %d: modal contains invalid UTF-8 bytes", w)
					}

					// 2. Output must not have malformed ANSI escape sequences
					stripped := stripANSI(rendered)
					if strings.Contains(stripped, "\x1b") {
						t.Errorf("width %d: modal contains dangling ANSI escape: %q", w, rendered)
					}

					// 3. Check border alignment:
					// All non-empty lines of the modal box must have the exact same visual cell width!
					lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
					if len(lines) != 6 {
						t.Fatalf("width %d: expected 6 modal lines, got %d:\n%s", w, len(lines), rendered)
					}

					expectedWidth := lipgloss.Width(lines[0])
					for idx, line := range lines {
						lineWidth := lipgloss.Width(line)
						if lineWidth != expectedWidth {
							t.Errorf("width %d: modal line %d visual width %d != top bar width %d:\nLine: %q",
								w, idx+1, lineWidth, expectedWidth, line)
						}
						// Ensure it does not exceed terminal width
						if lineWidth > w {
							t.Errorf("width %d: modal line %d visual width %d exceeds terminal width %d",
								w, idx+1, lineWidth, w)
						}
					}
				})
			}
		})
	}
}

func TestModel_SourceManager_Backspace_UTF8Safety(t *testing.T) {
	mock := newTestSourceMockController()
	m := tui.New(mock)

	// Enter Config Center and open Add modal ('a')
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*tui.Model)

	// Set multi-byte Persian string in URL: "سلام" (4 runes, 8 bytes: \xd8\xb3 \xd9\x84 \xd8\xa7 \xd9\x85)
	m.SetSourceInputURLForTest("سلام")

	// Backspace once -> should be "سلا" (3 runes, 6 bytes), never corrupted half-rune
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(*tui.Model)
	if url := m.SourceInputURLForTest(); url != "سلا" {
		t.Fatalf("expected 'سلا', got %q", url)
	}
	if !utf8.ValidString(m.SourceInputURLForTest()) {
		t.Fatalf("URL contains invalid UTF-8 after backspace")
	}

	// Backspace until empty
	for i := 0; i < 5; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = updated.(*tui.Model)
	}
	if url := m.SourceInputURLForTest(); url != "" {
		t.Fatalf("expected empty URL, got %q", url)
	}

	// Switch focus to Name
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*tui.Model)

	// Set CJK string: "测试" (2 runes, 6 bytes)
	m.SetSourceInputNameForTest("测试")

	// Backspace once -> should be "测" (1 rune, 3 bytes)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(*tui.Model)
	if name := m.SourceInputNameForTest(); name != "测" {
		t.Fatalf("expected '测', got %q", name)
	}
	if !utf8.ValidString(m.SourceInputNameForTest()) {
		t.Fatalf("Name contains invalid UTF-8 after backspace")
	}

	// Backspace again -> empty
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(*tui.Model)
	if name := m.SourceInputNameForTest(); name != "" {
		t.Fatalf("expected empty Name, got %q", name)
	}

	// Set emoji: "🚀" (1 rune, 4 bytes)
	m.SetSourceInputNameForTest("🚀")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(*tui.Model)
	if name := m.SourceInputNameForTest(); name != "" {
		t.Fatalf("expected empty Name after deleting emoji, got %q", name)
	}
}
