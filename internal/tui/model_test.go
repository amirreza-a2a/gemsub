package tui_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gemsub/internal/events"
	"gemsub/internal/logging"
	"gemsub/internal/store"
	"gemsub/internal/tui"
	"gemsub/internal/tui/adapter"
	"gemsub/internal/tui/country"
)

func setupTestModel(t *testing.T) (*tui.Model, *store.Store, *events.EventBus, *logging.RingLogHandler) {
	t.Helper()
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
