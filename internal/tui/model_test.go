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
