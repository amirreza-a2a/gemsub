package adapter_test

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gemsub/internal/events"
	"gemsub/internal/logging"
	"gemsub/internal/store"
	"gemsub/internal/tui/adapter"
	"gemsub/internal/tui/viewmodel"
)

func setupTestAdapter(t *testing.T) (*adapter.Adapter, *store.Store, *events.EventBus, *logging.RingLogHandler) {
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

	return ad, st, bus, ring
}

func TestAdapter_DeterministicOrdering(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	// Create 4 candidates with known attributes:
	// c1: Servable, HasPassed=true, Score=0.90, Latency=100ms
	// c2: Servable, HasPassed=true, Score=0.90, Latency=200ms
	// c3: Servable, HasPassed=false, Score=0.95 (unproven)
	// c4: Unservable (failed target), Score=0.10
	c1 := "vless://c1@1.1.1.1:443#C1"
	c2 := "vless://c2@2.2.2.2:443#C2"
	c3 := "vless://c3@3.3.3.3:443#C3"
	c4 := "vless://c4@4.4.4.4:443#C4"

	now := time.Now()

	// c1: Pass 100ms
	st.PutWithTransition(store.Result{Link: c1, Status: store.StatusPassed, Latency: 100 * time.Millisecond, TestedAt: now})
	// c2: Pass 200ms
	st.PutWithTransition(store.Result{Link: c2, Status: store.StatusPassed, Latency: 200 * time.Millisecond, TestedAt: now})

	// c3: Inconclusive (grace period or unproven pass)
	// Put multiple inconclusive to test unproven ordering
	st.PutWithTransition(store.Result{Link: c3, Status: store.StatusInconclusive, TestedAt: now})

	// c4: Failed target
	st.PutWithTransition(store.Result{Link: c4, Status: store.StatusFailed, Category: store.ErrRegionBlocked, TestedAt: now})

	rows := ad.CandidateRows(false)
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(rows))
	}

	// Rule 1: Servable before unservable.
	// c4 is unservable, so it must be last.
	if rows[3].Remark != "C4" {
		t.Errorf("expected last row to be unservable C4, got %q", rows[3].Remark)
	}

	// Rule 2: Proven before unproven among servable.
	// c1 and c2 are proven (HasPassed=true). c3 has HasPassed=false.
	// So c1 and c2 must come before c3.
	if rows[2].Remark != "C3" {
		t.Errorf("expected 3rd row to be unproven C3, got %q", rows[2].Remark)
	}

	// Rule 3 & 4: Same score (0.90 / 1.0), latency ascending (100ms < 200ms)
	// c1 (100ms) before c2 (200ms)
	if rows[0].Remark != "C1" || rows[1].Remark != "C2" {
		t.Errorf("expected rows order [C1, C2], got [%s, %s]", rows[0].Remark, rows[1].Remark)
	}
}

func TestAdapter_OpaqueIDMappingAndDetail(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	link := "vless://secret-uuid@1.1.1.1:443?security=reality&sni=example.com#MyNode"
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Reason:   "ok",
		Latency:  120 * time.Millisecond,
		TestedAt: time.Now(),
	})

	rows := ad.CandidateRows(false)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	row := rows[0]
	// Opaque ID must NOT be the link
	if row.ID == link || row.ID == store.CanonicalizeLink(link) {
		t.Errorf("row.ID leaked canonical link: %q", row.ID)
	}

	// Detail retrieval via opaque ID
	detail, ok := ad.CandidateDetail(row.ID)
	if !ok {
		t.Fatalf("failed to retrieve candidate detail for opaque ID %q", row.ID)
	}

	if detail.Remark != "MyNode" {
		t.Errorf("expected remark 'MyNode', got %q", detail.Remark)
	}
	if detail.Protocol != "vless" {
		t.Errorf("expected protocol 'vless', got %q", detail.Protocol)
	}
	if detail.Endpoint != "1.1.1.1:443" {
		t.Errorf("expected endpoint '1.1.1.1:443', got %q", detail.Endpoint)
	}
	if strings.Contains(detail.MaskedLink, "secret-uuid") {
		t.Errorf("detail.MaskedLink leaked secret-uuid: %q", detail.MaskedLink)
	}
	if !detail.Servable {
		t.Errorf("expected detail to be servable")
	}
	if len(detail.Samples) != 1 {
		t.Errorf("expected 1 sample in detail, got %d", len(detail.Samples))
	}
}

func TestAdapter_FilterServableOnly(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	linkPass := "vless://pass@1.1.1.1:443#Pass"
	linkFail := "vless://fail@2.2.2.2:443#Fail"

	st.PutWithTransition(store.Result{Link: linkPass, Status: store.StatusPassed, TestedAt: time.Now()})
	st.PutWithTransition(store.Result{Link: linkFail, Status: store.StatusFailed, Category: store.ErrRegionBlocked, TestedAt: time.Now()})

	allRows := ad.CandidateRows(false)
	if len(allRows) != 2 {
		t.Fatalf("expected 2 all rows, got %d", len(allRows))
	}

	servableRows := ad.CandidateRows(true)
	if len(servableRows) != 1 {
		t.Fatalf("expected 1 servable row, got %d", len(servableRows))
	}
	if servableRows[0].Remark != "Pass" {
		t.Errorf("expected servable row 'Pass', got %q", servableRows[0].Remark)
	}
}

func TestAdapter_EventBusDrivenDirtyTracking(t *testing.T) {
	ad, _, bus, _ := setupTestAdapter(t)

	// Subscribe before scheduler starts
	ad.Subscribe()

	// Initially not dirty (after creation)
	_ = ad.CheckAndResetDirty()

	// 1. CycleStarted
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	time.Sleep(20 * time.Millisecond)

	if !ad.CheckAndResetDirty() {
		t.Error("expected dirty after CycleStarted")
	}
	header := ad.Header()
	if header.CycleStatus != viewmodel.CycleRunning {
		t.Errorf("expected CycleRunning, got %s", header.CycleStatus)
	}

	// 2. CandidatesLoaded
	bus.Publish(events.CandidatesLoaded{Total: 42})
	time.Sleep(20 * time.Millisecond)

	if !ad.CheckAndResetDirty() {
		t.Error("expected dirty after CandidatesLoaded")
	}
	header = ad.Header()
	if header.ProgressTotal != 42 {
		t.Errorf("expected ProgressTotal 42, got %d", header.ProgressTotal)
	}

	// 3. ProbeCompleted
	bus.Publish(events.ProbeCompleted{
		ProgressMetrics: events.ProgressMetrics{
			Completed: 10,
			Total:     42,
		},
	})
	time.Sleep(20 * time.Millisecond)

	if !ad.CheckAndResetDirty() {
		t.Error("expected dirty after ProbeCompleted")
	}
	header = ad.Header()
	if header.ProgressCurrent != 10 {
		t.Errorf("expected ProgressCurrent 10, got %d", header.ProgressCurrent)
	}

	// 4. CycleFinished
	bus.Publish(events.CycleFinished{
		ProgressMetrics: events.ProgressMetrics{
			Completed: 42,
			Total:     42,
		},
	})
	time.Sleep(20 * time.Millisecond)

	if !ad.CheckAndResetDirty() {
		t.Error("expected dirty after CycleFinished")
	}
	header = ad.Header()
	if header.CycleStatus != viewmodel.CycleIdle {
		t.Errorf("expected CycleIdle after CycleFinished, got %s", header.CycleStatus)
	}
}

func TestAdapter_LogViewModelAndFiltering(t *testing.T) {
	ad, _, _, ring := setupTestAdapter(t)

	logger := slog.New(ring)
	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")

	// Default filter is INFO
	logVM := ad.Logs()
	if logVM.MinLevel != slog.LevelInfo {
		t.Errorf("expected default level INFO, got %s", logVM.MinLevel)
	}
	if len(logVM.Lines) != 3 { // info, warn, error
		t.Errorf("expected 3 lines for INFO level, got %d", len(logVM.Lines))
	}

	// Switch to WARN
	ad.SetMinLogLevel(slog.LevelWarn)
	logVM = ad.Logs()
	if len(logVM.Lines) != 2 { // warn, error
		t.Errorf("expected 2 lines for WARN level, got %d", len(logVM.Lines))
	}

	// Switch to DEBUG
	ad.SetMinLogLevel(slog.LevelDebug)
	logVM = ad.Logs()
	if len(logVM.Lines) != 4 { // debug, info, warn, error
		t.Errorf("expected 4 lines for DEBUG level, got %d", len(logVM.Lines))
	}
}

func TestAdapter_OpaqueIDPruning(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	st := store.New(stateFile, 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)
	defer ad.Close()
	defer bus.Close()

	// Cycle 1: candidates A and B
	st.PutWithTransition(store.Result{Link: "vless://a@1.1.1.1:443#NodeA", Status: store.StatusPassed, TestedAt: time.Now()})
	st.PutWithTransition(store.Result{Link: "vless://b@2.2.2.2:443#NodeB", Status: store.StatusPassed, TestedAt: time.Now()})

	rows1 := ad.CandidateRows(false)
	if len(rows1) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows1))
	}
	var idA, idB string
	for _, r := range rows1 {
		if r.Remark == "NodeA" {
			idA = r.ID
		} else if r.Remark == "NodeB" {
			idB = r.ID
		}
	}

	detailA, foundA := ad.CandidateDetail(idA)
	if !foundA || detailA.Remark != "NodeA" {
		t.Fatal("expected detail for NodeA")
	}

	detailB, foundB := ad.CandidateDetail(idB)
	if !foundB || detailB.Remark != "NodeB" {
		t.Fatal("expected detail for NodeB")
	}

	// Cycle 2: candidate A is evicted from store, only candidate B remains, and candidate C is added
	bLink := "vless://b@2.2.2.2:443#NodeB"
	cLink := "vless://c@3.3.3.3:443#NodeC"
	// Recreate state file with only B and C
	stNew := store.New(stateFile, 2)
	stNew.PutWithTransition(store.Result{Link: bLink, Status: store.StatusPassed, TestedAt: time.Now()})
	stNew.PutWithTransition(store.Result{Link: cLink, Status: store.StatusPassed, TestedAt: time.Now()})
	if err := stNew.Save(); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}
	if err := st.Load(); err != nil {
		t.Fatalf("failed to reload store: %v", err)
	}

	// Call CandidateRows on the SAME adapter `ad`
	rows2 := ad.CandidateRows(false)
	if len(rows2) != 2 {
		t.Fatalf("expected 2 rows after reload, got %d", len(rows2))
	}

	// Verify candidate A was pruned from `ad`
	_, foundAAfter := ad.CandidateDetail(idA)
	if foundAAfter {
		t.Errorf("expected candidate A (%s) to be pruned from adapter, but was found", idA)
	}

	// Verify candidate B retained its existing ID (idB)
	detailB2, foundB2 := ad.CandidateDetail(idB)
	if !foundB2 || detailB2.Remark != "NodeB" {
		t.Errorf("expected candidate B to retain its ID (%s), found=%v", idB, foundB2)
	}

	// Verify candidate C was assigned a new ID
	var idC string
	for _, r := range rows2 {
		if r.Remark == "NodeC" {
			idC = r.ID
		}
	}
	if idC == "" || idC == idA || idC == idB {
		t.Errorf("expected new unique ID for candidate C, got %q", idC)
	}

	// Non-existent ID returns false
	_, foundMissing := ad.CandidateDetail("cand-999")
	if foundMissing {
		t.Errorf("expected cand-999 to not be found")
	}
}

func TestAdapter_StoreRevisionConvergenceWithoutEvents(t *testing.T) {
	// Tests that if EventBus drops 100% of events, Store revision mismatch
	// still triggers dirty refresh on the next check.
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)
	// EventBus with no subscribers or dropped events
	bus := events.New()
	defer bus.Close()
	ring := logging.NewRingLogHandler(100)

	ad := adapter.New(st, bus, ring)
	defer ad.Close()

	// Initially dirty is false after initial check
	_ = ad.CheckAndResetDirty()
	if ad.CheckAndResetDirty() {
		t.Error("expected dirty to be false initially")
	}

	// Store receives a transition directly without EventBus publishing
	st.PutWithTransition(store.Result{
		Link:     "vless://silent@1.1.1.1:443#Silent",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})

	// Adapter CheckAndResetDirty should detect the Store revision change
	if !ad.CheckAndResetDirty() {
		t.Error("expected CheckAndResetDirty to be true due to Store revision bump even with 0 events")
	}

	// Subsequent check should be false
	if ad.CheckAndResetDirty() {
		t.Error("expected CheckAndResetDirty to be reset to false")
	}
}

func TestAdapter_LatencySemantics(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	// Candidate with failed probe: Latest.Latency = 500ms, but HasPassed = false
	cUnproven := "vless://unproven@1.1.1.1:443#Unproven"
	st.PutWithTransition(store.Result{
		Link:     cUnproven,
		Status:   store.StatusFailed,
		Latency:  500 * time.Millisecond,
		TestedAt: time.Now(),
	})

	// Candidate with passed probe: Latency = 150ms, HasPassed = true
	cProven := "vless://proven@2.2.2.2:443#Proven"
	st.PutWithTransition(store.Result{
		Link:     cProven,
		Status:   store.StatusPassed,
		Latency:  150 * time.Millisecond,
		TestedAt: time.Now(),
	})

	rows := ad.CandidateRows(false)
	var rowProven, rowUnproven viewmodel.CandidateRowViewModel
	for _, r := range rows {
		if r.Remark == "Proven" {
			rowProven = r
		} else if r.Remark == "Unproven" {
			rowUnproven = r
		}
	}

	// Proven candidate must show LastPassedLatency formatted
	if rowProven.LatencyFormatted != "150ms" {
		t.Errorf("expected proven row latency '150ms', got %q", rowProven.LatencyFormatted)
	}

	// Unproven candidate must display "---" for LatencyFormatted, NOT the probe duration
	if rowUnproven.LatencyFormatted != "---" {
		t.Errorf("expected unproven row latency '---', got %q", rowUnproven.LatencyFormatted)
	}

	// Verify CandidateDetail has both ProvenLatencyFormatted and Latest probe Latency
	detailUnproven, ok := ad.CandidateDetail(rowUnproven.ID)
	if !ok {
		t.Fatal("expected detail for unproven candidate")
	}
	if detailUnproven.ProvenLatencyFormatted != "---" {
		t.Errorf("expected detail ProvenLatencyFormatted '---', got %q", detailUnproven.ProvenLatencyFormatted)
	}
	if detailUnproven.Latency != 500*time.Millisecond {
		t.Errorf("expected detail probe Latency 500ms, got %v", detailUnproven.Latency)
	}
}

func TestAdapter_ControllerMethods(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	st.PutWithTransition(store.Result{
		Link:     "vless://ctrl@1.1.1.1:443?sni=example.com#CtrlNode",
		Status:   store.StatusPassed,
		Latency:  80 * time.Millisecond,
		TestedAt: time.Now(),
	})

	// Snapshot
	snap := ad.Snapshot(false)
	if len(snap.Rows) != 1 {
		t.Fatalf("expected 1 row in snapshot, got %d", len(snap.Rows))
	}
	if snap.Header.TotalCandidates != 1 {
		t.Errorf("expected 1 total candidate in header, got %d", snap.Header.TotalCandidates)
	}

	// PollSnapshot when dirty
	ad.MarkDirty()
	snap2, updated := ad.PollSnapshot(false)
	if !updated {
		t.Fatal("expected PollSnapshot updated=true when dirty")
	}
	if len(snap2.Rows) != 1 {
		t.Errorf("expected 1 row in snap2, got %d", len(snap2.Rows))
	}

	// PollSnapshot when clean
	_, updatedClean := ad.PollSnapshot(false)
	if updatedClean {
		t.Error("expected PollSnapshot updated=false when clean")
	}

	// CycleLogLevel
	_, lvlStr := ad.CycleLogLevel()
	if lvlStr != "WARN" { // INFO -> WARN
		t.Errorf("expected log level WARN, got %s", lvlStr)
	}

	// CopyCandidateLink
	err := ad.CopyCandidateLink(snap.Rows[0].ID)
	// Clipboard may or may not succeed in CI/headless, but must not panic
	_ = err

	// Invalid candidate ID
	errMissing := ad.CopyCandidateLink("non-existent-id")
	if errMissing == nil {
		t.Error("expected error for non-existent candidate link copy")
	}
}
