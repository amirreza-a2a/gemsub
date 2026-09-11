package tui_test

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
