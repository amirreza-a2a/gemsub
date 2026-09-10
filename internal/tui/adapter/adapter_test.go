package adapter_test

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gemsub/internal/events"
	"gemsub/internal/logging"
	"gemsub/internal/store"
	"gemsub/internal/tui/adapter"
	"gemsub/internal/tui/country"
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

	// c4: Failed transport (unservable)
	st.PutWithTransition(store.Result{Link: c4, Status: store.StatusFailed, Category: store.ErrProxyError, TestedAt: now})

	rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
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

	// Rule 3: Reliability score descending within proven servable.
	// Both c1 and c2 have score 0.90, so score doesn't differentiate.
	// Rule 4: Effective latency ascending.
	// c1 has latency 100ms, c2 has latency 200ms.
	// So c1 must come before c2.
	if rows[0].Remark != "C1" {
		t.Errorf("expected 1st row to be C1 (lower latency), got %q", rows[0].Remark)
	}
	if rows[1].Remark != "C2" {
		t.Errorf("expected 2nd row to be C2, got %q", rows[1].Remark)
	}
}

func TestAdapter_OpaqueIDMappingAndDetail(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	link := "vless://user@1.1.1.1:443#TestNode"
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Reason:   "ok",
		Latency:  120 * time.Millisecond,
		TestedAt: time.Now(),
	})

	rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
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
	if detail.Remark != "TestNode" {
		t.Errorf("expected detail remark %q, got %q", "TestNode", detail.Remark)
	}

	// Detail contains masked active link
	if strings.Contains(detail.MaskedLink, "user@") {
		t.Errorf("detail.MaskedLink leaked credentials: %q", detail.MaskedLink)
	}
	if !strings.Contains(detail.MaskedLink, "[REDACTED]") {
		t.Errorf("detail.MaskedLink missing [REDACTED]: %q", detail.MaskedLink)
	}

	// Non-existent opaque ID returns false
	_, okMissing := ad.CandidateDetail("cand-999")
	if okMissing {
		t.Error("expected ok=false for non-existent opaque ID")
	}
}

func TestAdapter_FilterServableOnly(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	linkPass := "vless://pass@1.1.1.1:443#Pass"
	linkFail := "vless://fail@2.2.2.2:443#Fail"

	st.PutWithTransition(store.Result{Link: linkPass, Status: store.StatusPassed, TestedAt: time.Now()})
	st.PutWithTransition(store.Result{Link: linkFail, Status: store.StatusFailed, Category: store.ErrRegionBlocked, TestedAt: time.Now()})

	allRows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	if len(allRows) != 2 {
		t.Fatalf("expected 2 all rows, got %d", len(allRows))
	}

	servableRows := ad.CandidateRowsWindow(viewmodel.FilterGemini, 0, 10)
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
			Completed:    10,
			Total:        42,
			Passed:       8,
			Failed:       1,
			Inconclusive: 1,
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
	if header.PassedCount != 8 || header.FailedCount != 1 || header.InconclusiveCount != 1 {
		t.Errorf("expected cycle metrics Pass:8 Fail:1 Incon:1, got Pass:%d Fail:%d Incon:%d",
			header.PassedCount, header.FailedCount, header.InconclusiveCount)
	}

	// 4. CycleFinished
	bus.Publish(events.CycleFinished{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    42,
			Total:        42,
			Passed:       35,
			Failed:       5,
			Inconclusive: 2,
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
	if header.PassedCount != 35 || header.FailedCount != 5 || header.InconclusiveCount != 2 {
		t.Errorf("expected final cycle metrics Pass:35 Fail:5 Incon:2, got Pass:%d Fail:%d Incon:%d",
			header.PassedCount, header.FailedCount, header.InconclusiveCount)
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
	ad.SetIndexThrottleIntervalForTest(0)
	defer ad.Close()
	defer bus.Close()

	// Cycle 1: candidates A and B
	st.PutWithTransition(store.Result{Link: "vless://a@1.1.1.1:443#NodeA", Status: store.StatusPassed, TestedAt: time.Now()})
	st.PutWithTransition(store.Result{Link: "vless://b@2.2.2.2:443#NodeB", Status: store.StatusPassed, TestedAt: time.Now()})

	rows1 := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
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

	linkCount1, idCount1 := ad.OpaqueIDMapCountsForTest()
	if linkCount1 != 2 || idCount1 != 2 {
		t.Fatalf("expected initial map sizes (2, 2), got (%d, %d)", linkCount1, idCount1)
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

	// Call CandidateRowsWindow on the SAME adapter `ad`
	rows2 := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	if len(rows2) != 2 {
		t.Fatalf("expected 2 rows after reload, got %d", len(rows2))
	}

	// Verify candidate A was pruned from `ad` internal maps and detail retrieval fails
	_, foundAAfter := ad.CandidateDetail(idA)
	if foundAAfter {
		t.Errorf("expected candidate A (%s) to be pruned from adapter, but was found", idA)
	}
	linkCount2, idCount2 := ad.OpaqueIDMapCountsForTest()
	if linkCount2 != 2 || idCount2 != 2 {
		t.Errorf("expected pruned map sizes (2, 2), got (%d, %d)", linkCount2, idCount2)
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

	rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
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
	snap := ad.Snapshot(viewmodel.FilterAll)
	if len(snap.Rows) != 1 {
		t.Fatalf("expected 1 row in snapshot, got %d", len(snap.Rows))
	}
	if snap.Header.TotalCandidates != 1 {
		t.Errorf("expected 1 total candidate in header, got %d", snap.Header.TotalCandidates)
	}

	// PollSnapshot when dirty
	ad.MarkDirty()
	snap2, updated := ad.PollSnapshot(viewmodel.FilterAll)
	if !updated {
		t.Fatal("expected PollSnapshot updated=true when dirty")
	}
	if len(snap2.Rows) != 1 {
		t.Errorf("expected 1 row in snap2, got %d", len(snap2.Rows))
	}

	// PollSnapshot when clean
	_, updatedClean := ad.PollSnapshot(viewmodel.FilterAll)
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

func TestAdapter_CurrentCycleMetricsLifecycle(t *testing.T) {
	ad, st, bus, _ := setupTestAdapter(t)
	ad.Subscribe()

	// 1. Pre-populate Store with historical records (simulating past cycles)
	now := time.Now()
	st.PutWithTransition(store.Result{Link: "vless://p1@1.1.1.1:443#P1", Status: store.StatusPassed, TestedAt: now})
	st.PutWithTransition(store.Result{Link: "vless://p2@1.1.1.2:443#P2", Status: store.StatusPassed, TestedAt: now})
	st.PutWithTransition(store.Result{Link: "vless://f1@2.2.2.1:443#F1", Status: store.StatusFailed, TestedAt: now})
	st.PutWithTransition(store.Result{Link: "vless://i1@3.3.3.1:443#I1", Status: store.StatusInconclusive, TestedAt: now})

	// Invariant: Store aggregate statistics reflect historical state
	storeStats := ad.StoreStats()
	if storeStats.Total != 4 || storeStats.Passed != 2 || storeStats.Failed != 1 || storeStats.Inconclusive != 1 {
		t.Fatalf("unexpected store stats: %+v", storeStats)
	}

	// Initial HeaderViewModel before cycle execution begins:
	// Pass/Fail/Incon counts must be 0 for current cycle, NOT store aggregates!
	initHdr := ad.Header()
	if initHdr.PassedCount != 0 || initHdr.FailedCount != 0 || initHdr.InconclusiveCount != 0 {
		t.Errorf("expected initial cycle counters 0/0/0, got Pass:%d Fail:%d Incon:%d",
			initHdr.PassedCount, initHdr.FailedCount, initHdr.InconclusiveCount)
	}
	if initHdr.CycleStatus != viewmodel.CycleIdle {
		t.Errorf("expected initial status CycleIdle, got %s", initHdr.CycleStatus)
	}
	if initHdr.TotalCandidates != 4 {
		t.Errorf("expected TotalCandidates 4 from store, got %d", initHdr.TotalCandidates)
	}

	// 2. CycleStarted resets counters and sets CycleRunning
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	time.Sleep(20 * time.Millisecond)

	startHdr := ad.Header()
	if startHdr.CycleStatus != viewmodel.CycleRunning {
		t.Errorf("expected CycleRunning, got %s", startHdr.CycleStatus)
	}
	if startHdr.PassedCount != 0 || startHdr.FailedCount != 0 || startHdr.InconclusiveCount != 0 {
		t.Errorf("expected reset cycle counters at CycleStarted, got Pass:%d Fail:%d Incon:%d",
			startHdr.PassedCount, startHdr.FailedCount, startHdr.InconclusiveCount)
	}

	// 3. CandidatesLoaded updates ProgressTotal
	bus.Publish(events.CandidatesLoaded{Total: 4})
	time.Sleep(20 * time.Millisecond)

	loadedHdr := ad.Header()
	if loadedHdr.ProgressTotal != 4 {
		t.Errorf("expected ProgressTotal 4, got %d", loadedHdr.ProgressTotal)
	}

	// 4. ProbeCompleted events update current-cycle counters incrementally
	bus.Publish(events.ProbeCompleted{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    1,
			Total:        4,
			Passed:       1,
			Failed:       0,
			Inconclusive: 0,
		},
	})
	time.Sleep(20 * time.Millisecond)

	p1Hdr := ad.Header()
	if p1Hdr.PassedCount != 1 || p1Hdr.FailedCount != 0 || p1Hdr.InconclusiveCount != 0 || p1Hdr.ProgressCurrent != 1 {
		t.Errorf("expected step 1 counters Pass:1 Fail:0 Incon:0 Probes:1/4, got Pass:%d Fail:%d Incon:%d Probes:%d/%d",
			p1Hdr.PassedCount, p1Hdr.FailedCount, p1Hdr.InconclusiveCount, p1Hdr.ProgressCurrent, p1Hdr.ProgressTotal)
	}

	bus.Publish(events.ProbeCompleted{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    2,
			Total:        4,
			Passed:       1,
			Failed:       1,
			Inconclusive: 0,
		},
	})
	time.Sleep(20 * time.Millisecond)

	p2Hdr := ad.Header()
	if p2Hdr.PassedCount != 1 || p2Hdr.FailedCount != 1 || p2Hdr.InconclusiveCount != 0 || p2Hdr.ProgressCurrent != 2 {
		t.Errorf("expected step 2 counters Pass:1 Fail:1 Incon:0 Probes:2/4, got Pass:%d Fail:%d Incon:%d Probes:%d/%d",
			p2Hdr.PassedCount, p2Hdr.FailedCount, p2Hdr.InconclusiveCount, p2Hdr.ProgressCurrent, p2Hdr.ProgressTotal)
	}

	bus.Publish(events.ProbeCompleted{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    3,
			Total:        4,
			Passed:       1,
			Failed:       1,
			Inconclusive: 1,
		},
	})
	time.Sleep(20 * time.Millisecond)

	p3Hdr := ad.Header()
	if p3Hdr.PassedCount != 1 || p3Hdr.FailedCount != 1 || p3Hdr.InconclusiveCount != 1 || p3Hdr.ProgressCurrent != 3 {
		t.Errorf("expected step 3 counters Pass:1 Fail:1 Incon:1 Probes:3/4, got Pass:%d Fail:%d Incon:%d Probes:%d/%d",
			p3Hdr.PassedCount, p3Hdr.FailedCount, p3Hdr.InconclusiveCount, p3Hdr.ProgressCurrent, p3Hdr.ProgressTotal)
	}

	// 5. CycleFinished reaches final cycle values and sets CycleIdle
	bus.Publish(events.CycleFinished{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    4,
			Total:        4,
			Passed:       2,
			Failed:       1,
			Inconclusive: 1,
		},
	})
	time.Sleep(20 * time.Millisecond)

	finHdr := ad.Header()
	if finHdr.CycleStatus != viewmodel.CycleIdle {
		t.Errorf("expected CycleIdle after CycleFinished, got %s", finHdr.CycleStatus)
	}
	if finHdr.PassedCount != 2 || finHdr.FailedCount != 1 || finHdr.InconclusiveCount != 1 || finHdr.ProgressCurrent != 4 {
		t.Errorf("expected final cycle counters Pass:2 Fail:1 Incon:1 Probes:4/4, got Pass:%d Fail:%d Incon:%d Probes:%d/%d",
			finHdr.PassedCount, finHdr.FailedCount, finHdr.InconclusiveCount, finHdr.ProgressCurrent, finHdr.ProgressTotal)
	}

	// Verify authoritative aggregate store stats remain intact and correct
	finalStoreStats := ad.StoreStats()
	if finalStoreStats.Passed != 2 || finalStoreStats.Failed != 1 || finalStoreStats.Inconclusive != 1 {
		t.Errorf("expected store aggregate stats to remain correct, got %+v", finalStoreStats)
	}

	// 6. Next CycleStarted unconditionally resets counters back to 0
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	time.Sleep(20 * time.Millisecond)

	nextHdr := ad.Header()
	if nextHdr.CycleStatus != viewmodel.CycleRunning {
		t.Errorf("expected CycleRunning on next cycle, got %s", nextHdr.CycleStatus)
	}
	if nextHdr.PassedCount != 0 || nextHdr.FailedCount != 0 || nextHdr.InconclusiveCount != 0 || nextHdr.ProgressCurrent != 0 {
		t.Errorf("expected reset to 0 on next cycle start, got Pass:%d Fail:%d Incon:%d Probes:%d",
			nextHdr.PassedCount, nextHdr.FailedCount, nextHdr.InconclusiveCount, nextHdr.ProgressCurrent)
	}
}

func TestAdapter_LostEventBusEventsPreserveCandidateStateProjection(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	// Clean initial dirty state
	_ = ad.CheckAndResetDirty()

	// Simulate complete loss of EventBus events (no bus.Publish calls)
	// Mutate the authoritative Store directly
	cand := "vless://authoritative@1.1.1.1:443#AuthNode"
	st.PutWithTransition(store.Result{
		Link:     cand,
		Status:   store.StatusPassed,
		Latency:  95 * time.Millisecond,
		TestedAt: time.Now(),
	})

	// Adapter detects revision divergence via PollSnapshot (which calls CheckAndResetDirty internally)
	// and projects authoritative candidate state without corruption
	snap, updated := ad.PollSnapshot(viewmodel.FilterAll)
	if !updated {
		t.Fatal("expected PollSnapshot updated=true after store revision change")
	}
	if len(snap.Rows) != 1 {
		t.Fatalf("expected 1 candidate row projected, got %d", len(snap.Rows))
	}
	if snap.Rows[0].Remark != "AuthNode" || snap.Rows[0].Status != "PASS" {
		t.Errorf("expected correctly projected AuthNode row, got %+v", snap.Rows[0])
	}
	if snap.Header.TotalCandidates != 1 {
		t.Errorf("expected TotalCandidates 1 in header, got %d", snap.Header.TotalCandidates)
	}
}

func TestAdapter_CycleFinishedFinalValuesOnAbort(t *testing.T) {
	ad, _, bus, _ := setupTestAdapter(t)
	ad.Subscribe()

	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	time.Sleep(20 * time.Millisecond)

	// Cycle aborts early (e.g. empty fetch error)
	bus.Publish(events.CycleFinished{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    0,
			Total:        0,
			Passed:       0,
			Failed:       0,
			Inconclusive: 0,
		},
		Cancelled: false,
	})
	time.Sleep(20 * time.Millisecond)

	hdr := ad.Header()
	if hdr.CycleStatus != viewmodel.CycleIdle {
		t.Errorf("expected CycleIdle, got %s", hdr.CycleStatus)
	}
	if hdr.PassedCount != 0 || hdr.FailedCount != 0 || hdr.InconclusiveCount != 0 {
		t.Errorf("expected 0/0/0 counters on aborted cycle, got Pass:%d Fail:%d Incon:%d",
			hdr.PassedCount, hdr.FailedCount, hdr.InconclusiveCount)
	}
}

func TestAdapter_LostIntermediateProbeCompletedEvents(t *testing.T) {
	ad, _, bus, _ := setupTestAdapter(t)
	ad.Subscribe()

	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	bus.Publish(events.CandidatesLoaded{Total: 10})
	time.Sleep(20 * time.Millisecond)

	// Probe 1 is published
	bus.Publish(events.ProbeCompleted{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    1,
			Total:        10,
			Passed:       1,
			Failed:       0,
			Inconclusive: 0,
		},
	})
	time.Sleep(20 * time.Millisecond)

	h1 := ad.Header()
	if h1.PassedCount != 1 || h1.ProgressCurrent != 1 {
		t.Fatalf("expected 1 pass, 1 completed, got Pass:%d Probes:%d", h1.PassedCount, h1.ProgressCurrent)
	}

	// SIMULATE DROPPED EVENTS: Probes 2, 3, 4, 5 complete, but their events are NEVER delivered (lost due to queue saturation).
	// Probe 6 arrives with cumulative metrics reflecting all 6 probes:
	bus.Publish(events.ProbeCompleted{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    6,
			Total:        10,
			Passed:       4,
			Failed:       1,
			Inconclusive: 1,
		},
	})
	time.Sleep(20 * time.Millisecond)

	// Adapter immediately catches up to cumulative counts without corruption
	h6 := ad.Header()
	if h6.PassedCount != 4 || h6.FailedCount != 1 || h6.InconclusiveCount != 1 || h6.ProgressCurrent != 6 {
		t.Errorf("expected cumulative recovery Pass:4 Fail:1 Incon:1 Probes:6/10, got Pass:%d Fail:%d Incon:%d Probes:%d/%d",
			h6.PassedCount, h6.FailedCount, h6.InconclusiveCount, h6.ProgressCurrent, h6.ProgressTotal)
	}
}

func TestAdapter_LostCycleFinishedRecoverableViaStore(t *testing.T) {
	ad, st, bus, _ := setupTestAdapter(t)
	ad.Subscribe()

	// Initial cycle count is 0
	if st.Stats().CycleCount != 0 {
		t.Fatalf("expected initial CycleCount 0, got %d", st.Stats().CycleCount)
	}

	// Cycle starts
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	bus.Publish(events.CandidatesLoaded{Total: 3})
	time.Sleep(20 * time.Millisecond)

	hStart := ad.Header()
	if hStart.CycleStatus != viewmodel.CycleRunning {
		t.Fatalf("expected CycleRunning, got %s", hStart.CycleStatus)
	}

	// Candidates tested into Store
	now := time.Now()
	st.PutWithTransition(store.Result{Link: "vless://c1@1.1.1.1:443#C1", Status: store.StatusPassed, TestedAt: now})
	st.PutWithTransition(store.Result{Link: "vless://c2@2.2.2.2:443#C2", Status: store.StatusFailed, TestedAt: now})
	st.PutWithTransition(store.Result{Link: "vless://c3@3.3.3.3:443#C3", Status: store.StatusInconclusive, TestedAt: now})

	// Probe 1 event delivered
	bus.Publish(events.ProbeCompleted{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    1,
			Total:        3,
			Passed:       1,
			Failed:       0,
			Inconclusive: 0,
		},
	})
	time.Sleep(20 * time.Millisecond)

	// SIMULATE SEVERE LOSS: Probe 2, Probe 3, AND CycleFinished events are ALL LOST!
	// Scheduler calls FinishCycle() on Store (authoritative store completes cycle)
	st.FinishCycle()

	// Adapter polls on tick (or queries Header)
	snap, updated := ad.PollSnapshot(viewmodel.FilterAll)
	if !updated {
		t.Fatal("expected PollSnapshot updated=true due to Store revision and cycle completion")
	}

	// Verify cycle status reconciled to CycleIdle despite missing CycleFinished event
	if snap.Header.CycleStatus != viewmodel.CycleIdle {
		t.Errorf("expected reconciled CycleIdle, got %s", snap.Header.CycleStatus)
	}
	// Verify final cycle counts reconciled from authoritative store history
	if snap.Header.PassedCount != 1 || snap.Header.FailedCount != 1 || snap.Header.InconclusiveCount != 1 {
		t.Errorf("expected reconciled counts Pass:1 Fail:1 Incon:1, got Pass:%d Fail:%d Incon:%d",
			snap.Header.PassedCount, snap.Header.FailedCount, snap.Header.InconclusiveCount)
	}
	if snap.Header.ProgressCurrent != 3 {
		t.Errorf("expected ProgressCurrent 3, got %d", snap.Header.ProgressCurrent)
	}
	if snap.Header.CycleCount != 1 {
		t.Errorf("expected CycleCount 1, got %d", snap.Header.CycleCount)
	}
}

func TestAdapter_NextCycleStartedUnconditionalReset(t *testing.T) {
	ad, _, bus, _ := setupTestAdapter(t)
	ad.Subscribe()

	// Cycle 1 finishes with non-zero counters
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	bus.Publish(events.CycleFinished{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    8,
			Total:        8,
			Passed:       5,
			Failed:       2,
			Inconclusive: 1,
		},
	})
	time.Sleep(20 * time.Millisecond)

	hPrev := ad.Header()
	if hPrev.CycleStatus != viewmodel.CycleIdle || hPrev.PassedCount != 5 || hPrev.FailedCount != 2 || hPrev.InconclusiveCount != 1 {
		t.Fatalf("expected cycle 1 metrics Pass:5 Fail:2 Incon:1, got %+v", hPrev)
	}

	// Next cycle begins
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	time.Sleep(20 * time.Millisecond)

	hNext := ad.Header()
	if hNext.CycleStatus != viewmodel.CycleRunning {
		t.Errorf("expected CycleRunning, got %s", hNext.CycleStatus)
	}
	if hNext.PassedCount != 0 || hNext.FailedCount != 0 || hNext.InconclusiveCount != 0 {
		t.Errorf("expected all counters reset to 0, got Pass:%d Fail:%d Incon:%d",
			hNext.PassedCount, hNext.FailedCount, hNext.InconclusiveCount)
	}
	if hNext.ProgressCurrent != 0 || hNext.ProgressTotal != 0 {
		t.Errorf("expected progress reset to 0/0, got %d/%d", hNext.ProgressCurrent, hNext.ProgressTotal)
	}
}

func TestAdapter_CancellationAndAbortPaths(t *testing.T) {
	ad, _, bus, _ := setupTestAdapter(t)
	ad.Subscribe()

	// Path 1: Cancellation mid-cycle
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	bus.Publish(events.CandidatesLoaded{Total: 10})
	bus.Publish(events.ProbeCompleted{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    2,
			Total:        10,
			Passed:       1,
			Failed:       1,
			Inconclusive: 0,
		},
	})
	time.Sleep(20 * time.Millisecond)

	// Scheduler cancels and publishes CycleFinished with Cancelled=true and partial metrics
	bus.Publish(events.CycleFinished{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    2,
			Total:        10,
			Passed:       1,
			Failed:       1,
			Inconclusive: 0,
		},
		Cancelled: true,
	})
	time.Sleep(20 * time.Millisecond)

	hCancel := ad.Header()
	if hCancel.CycleStatus != viewmodel.CycleIdle {
		t.Errorf("expected CycleIdle on cancellation, got %s", hCancel.CycleStatus)
	}
	if hCancel.PassedCount != 1 || hCancel.FailedCount != 1 || hCancel.InconclusiveCount != 0 {
		t.Errorf("expected partial metrics Pass:1 Fail:1 Incon:0, got Pass:%d Fail:%d Incon:%d",
			hCancel.PassedCount, hCancel.FailedCount, hCancel.InconclusiveCount)
	}
	if hCancel.ProgressCurrent != 2 || hCancel.ProgressTotal != 10 {
		t.Errorf("expected progress 2/10, got %d/%d", hCancel.ProgressCurrent, hCancel.ProgressTotal)
	}

	// Path 2: Empty fetch abort
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	time.Sleep(20 * time.Millisecond)
	bus.Publish(events.CycleFinished{
		ProgressMetrics: events.ProgressMetrics{
			Completed:    0,
			Total:        0,
			Passed:       0,
			Failed:       0,
			Inconclusive: 0,
		},
		Cancelled: false,
	})
	time.Sleep(20 * time.Millisecond)

	hAbort := ad.Header()
	if hAbort.CycleStatus != viewmodel.CycleIdle {
		t.Errorf("expected CycleIdle on abort, got %s", hAbort.CycleStatus)
	}
	if hAbort.PassedCount != 0 || hAbort.FailedCount != 0 || hAbort.InconclusiveCount != 0 {
		t.Errorf("expected 0/0/0 on abort, got Pass:%d Fail:%d Incon:%d",
			hAbort.PassedCount, hAbort.FailedCount, hAbort.InconclusiveCount)
	}
}

func TestAdapter_FlagPresentationModes(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "store.json"), 2)
	bus := events.New()
	t.Cleanup(bus.Close)
	ad := adapter.New(st, bus, nil)

	// Candidate with Unicode flag emoji in remark
	cand1 := "vless://user@1.1.1.1:443#🇩🇪 Germany"
	// Candidate with ASCII bracketed code in remark
	cand2 := "vless://user@2.2.2.2:443#[FR] France"

	st.PutWithTransition(store.Result{
		Link:     cand1,
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})
	st.PutWithTransition(store.Result{
		Link:     cand2,
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})

	// 1. Test ASCII mode
	ad.SetFlagMode(country.ModeASCII)
	if ad.FlagMode() != country.ModeASCII {
		t.Fatalf("expected FlagMode ASCII, got %v", ad.FlagMode())
	}

	rowsASCII := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	if len(rowsASCII) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rowsASCII))
	}

	for _, r := range rowsASCII {
		if r.Endpoint == "1.1.1.1:443" {
			if r.Remark != "[DE] Germany" {
				t.Errorf("expected ASCII remark [DE] Germany, got %q", r.Remark)
			}
			detail, ok := ad.CandidateDetail(r.ID)
			if !ok || detail.Remark != "[DE] Germany" {
				t.Errorf("expected detail remark [DE] Germany, got %q", detail.Remark)
			}
		} else if r.Endpoint == "2.2.2.2:443" {
			if r.Remark != "[FR] France" {
				t.Errorf("expected ASCII remark [FR] France, got %q", r.Remark)
			}
		}
	}

	// 2. Test Unicode mode
	ad.SetFlagMode(country.ModeUnicode)
	if ad.FlagMode() != country.ModeUnicode {
		t.Fatalf("expected FlagMode Unicode, got %v", ad.FlagMode())
	}

	rowsUnicode := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	for _, r := range rowsUnicode {
		if r.Endpoint == "1.1.1.1:443" {
			if r.Remark != "🇩🇪 Germany" {
				t.Errorf("expected Unicode remark 🇩🇪 Germany, got %q", r.Remark)
			}
			detail, ok := ad.CandidateDetail(r.ID)
			if !ok || detail.Remark != "🇩🇪 Germany" {
				t.Errorf("expected detail remark 🇩🇪 Germany, got %q", detail.Remark)
			}
		} else if r.Endpoint == "2.2.2.2:443" {
			if r.Remark != "🇫🇷 France" {
				t.Errorf("expected Unicode remark 🇫🇷 France, got %q", r.Remark)
			}
		}
	}

	// 3. Verify Candidate Identity and Store state are completely untouched
	rec1, ok := st.GetRecord(cand1)
	if !ok || rec1.CanonicalLink != cand1 {
		t.Errorf("Store state or identity corrupted: expected %q, got %v", cand1, rec1)
	}
	rawLink, ok := ad.ActiveLink(rowsASCII[0].ID)
	if !ok || (rawLink != cand1 && rawLink != cand2) {
		t.Errorf("ActiveLink corrupted: got %q", rawLink)
	}
}

func TestAdapter_DualServabilityStats(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	now := time.Now()
	// To satisfy MinObservationsForServing (which is 2 in setupTestAdapter):
	// c1: Gemini-servable and Generic-servable (2 passed observations)
	c1 := "vless://c1@1.1.1.1:443#GeminiServable"
	st.PutWithTransition(store.Result{Link: c1, Status: store.StatusPassed, TestedAt: now.Add(-time.Minute)})
	st.PutWithTransition(store.Result{Link: c1, Status: store.StatusPassed, TestedAt: now})

	// c2: Generic-servable only (2 observations with Stage 1 TransportOK, Stage 2 RegionBlocked)
	c2 := "vless://c2@2.2.2.2:443#GenericServable"
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

	// c3: Unservable dead candidate (Stage 1 transport failed)
	c3 := "vless://c3@3.3.3.3:443#DeadNode"
	st.PutWithTransition(store.Result{
		Link:                   c3,
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TestedAt:               now.Add(-time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:                   c3,
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TestedAt:               now,
	})

	hdr := ad.Header()
	if hdr.TotalCandidates != 3 {
		t.Fatalf("expected 3 total candidates, got %d", hdr.TotalCandidates)
	}
	if hdr.ServableCount != 1 {
		t.Errorf("expected 1 ServableCount (Gemini), got %d", hdr.ServableCount)
	}
	if hdr.GenericServableCount != 2 {
		t.Errorf("expected 2 GenericServableCount (Gemini + Generic), got %d", hdr.GenericServableCount)
	}
}

func TestAdapter_TieredRankingAndFiltering(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	now := time.Now()
	// c1: Tier 1 (Gemini-servable)
	c1 := "vless://c1@1.1.1.1:443#Tier1"
	st.PutWithTransition(store.Result{
		Link:                   c1,
		Status:                 store.StatusPassed,
		Latency:                150 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       150 * time.Millisecond,
		TestedAt:               now.Add(-time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:                   c1,
		Status:                 store.StatusPassed,
		Latency:                120 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       120 * time.Millisecond,
		TestedAt:               now,
	})

	// c2: Tier 2 (Generic-servable only)
	c2 := "vless://c2@2.2.2.2:443#Tier2"
	st.PutWithTransition(store.Result{
		Link:                   c2,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       80 * time.Millisecond,
		TestedAt:               now.Add(-time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:                   c2,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       75 * time.Millisecond,
		TestedAt:               now,
	})

	// c3: Tier 3 (Unservable)
	c3 := "vless://c3@3.3.3.3:443#Tier3"
	st.PutWithTransition(store.Result{
		Link:                   c3,
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TestedAt:               now.Add(-time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:                   c3,
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TestedAt:               now,
	})

	// 1. Check 3-tier ordering under FilterAll
	rowsAll := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	if len(rowsAll) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rowsAll))
	}
	if rowsAll[0].Remark != "Tier1" {
		t.Errorf("expected row 0 to be Tier1, got %q", rowsAll[0].Remark)
	}
	if rowsAll[1].Remark != "Tier2" {
		t.Errorf("expected row 1 to be Tier2, got %q", rowsAll[1].Remark)
	}
	if rowsAll[2].Remark != "Tier3" {
		t.Errorf("expected row 2 to be Tier3, got %q", rowsAll[2].Remark)
	}

	// 2. Check FilterGemini: only Tier 1
	rowsGemini := ad.CandidateRowsWindow(viewmodel.FilterGemini, 0, 10)
	if len(rowsGemini) != 1 {
		t.Fatalf("expected 1 row for FilterGemini, got %d", len(rowsGemini))
	}
	if rowsGemini[0].Remark != "Tier1" {
		t.Errorf("expected FilterGemini row to be Tier1, got %q", rowsGemini[0].Remark)
	}

	// 3. Check FilterGeneric: Tier 1 + Tier 2 (both are network healthy)
	rowsGeneric := ad.CandidateRowsWindow(viewmodel.FilterGeneric, 0, 10)
	if len(rowsGeneric) != 2 {
		t.Fatalf("expected 2 rows for FilterGeneric, got %d", len(rowsGeneric))
	}
	if rowsGeneric[0].Remark != "Tier1" || rowsGeneric[1].Remark != "Tier2" {
		t.Errorf("expected FilterGeneric rows [Tier1, Tier2], got [%s, %s]",
			rowsGeneric[0].Remark, rowsGeneric[1].Remark)
	}

	// 4. Verify CandidateRowViewModel fields and status mapping
	// Tier 1 row
	r1 := rowsAll[0]
	if !r1.Servable || !r1.NetworkHealthy || !r1.TransportOK || !r1.TransportEvidenceKnown {
		t.Errorf("Tier1 row flags unexpected: Servable=%v NetworkHealthy=%v TransportOK=%v TransportEvidenceKnown=%v",
			r1.Servable, r1.NetworkHealthy, r1.TransportOK, r1.TransportEvidenceKnown)
	}
	if r1.Status != "PASS" {
		t.Errorf("expected Tier1 Status PASS, got %s", r1.Status)
	}

	// Tier 2 row (RegionBlocked target restriction)
	r2 := rowsAll[1]
	if r2.Servable || !r2.NetworkHealthy || !r2.TransportOK || !r2.TransportEvidenceKnown {
		t.Errorf("Tier2 row flags unexpected: Servable=%v NetworkHealthy=%v TransportOK=%v TransportEvidenceKnown=%v",
			r2.Servable, r2.NetworkHealthy, r2.TransportOK, r2.TransportEvidenceKnown)
	}
	if r2.Status != "BLOCKED" {
		t.Errorf("expected Tier2 Status BLOCKED, got %s", r2.Status)
	}
	if r2.TransportLatency != 75*time.Millisecond {
		t.Errorf("expected Tier2 TransportLatency 75ms, got %v", r2.TransportLatency)
	}

	// Tier 3 row (transport failure)
	r3 := rowsAll[2]
	if r3.Servable || r3.NetworkHealthy || r3.TransportOK || !r3.TransportEvidenceKnown {
		t.Errorf("Tier3 row flags unexpected: Servable=%v NetworkHealthy=%v TransportOK=%v TransportEvidenceKnown=%v",
			r3.Servable, r3.NetworkHealthy, r3.TransportOK, r3.TransportEvidenceKnown)
	}
	if r3.Status != "FAIL" {
		t.Errorf("expected Tier3 Status FAIL, got %s", r3.Status)
	}

	// 5. Verify CandidateDetailViewModel fields
	detail2, ok := ad.CandidateDetail(r2.ID)
	if !ok {
		t.Fatalf("expected CandidateDetail for Tier2")
	}
	if detail2.Servable != false || detail2.NetworkHealthy != true {
		t.Errorf("expected Tier2 detail Servable=false NetworkHealthy=true, got Servable=%v NetworkHealthy=%v",
			detail2.Servable, detail2.NetworkHealthy)
	}
	if !detail2.TransportEvidenceKnown || !detail2.TransportOK || detail2.TransportLatency != 75*time.Millisecond {
		t.Errorf("expected Tier2 detail transport evidence known=true ok=true lat=75ms, got known=%v ok=%v lat=%v",
			detail2.TransportEvidenceKnown, detail2.TransportOK, detail2.TransportLatency)
	}
}

func TestAdapter_VirtualizationWindowAndBoundedMemory(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	// Populate 1000 candidates with distinct canonical links
	for i := 0; i < 1000; i++ {
		link := fmt.Sprintf("vless://user-%d@1.1.1.1:443#Node-%d", i, i)
		st.PutWithTransition(store.Result{
			Link:     link,
			Status:   store.StatusPassed,
			Latency:  time.Duration(50+i%200) * time.Millisecond,
			TestedAt: time.Now(),
		})
	}

	// 1. Snapshot returns bounded rows and accurate TotalRows
	snap := ad.Snapshot(viewmodel.FilterAll)
	if snap.TotalRows != 1000 {
		t.Fatalf("expected TotalRows 1000, got %d", snap.TotalRows)
	}
	if len(snap.Rows) > 50 {
		t.Fatalf("expected bounded rows <= 50, got %d", len(snap.Rows))
	}

	// 2. Window retrieval
	window := ad.CandidateRowsWindow(viewmodel.FilterAll, 200, 20)
	if len(window) != 20 {
		t.Fatalf("expected 20 rows in window, got %d", len(window))
	}

	// 3. Detail inspection for windowed rows
	detail, ok := ad.CandidateDetail(window[0].ID)
	if !ok {
		t.Fatalf("expected detail retrieval for window row ID %s", window[0].ID)
	}
	if detail.Status != "passed" {
		t.Errorf("expected status passed, got %s", detail.Status)
	}

	// 4. Boundary cases for CandidateRowsWindow
	emptyWindow := ad.CandidateRowsWindow(viewmodel.FilterAll, 2000, 20)
	if len(emptyWindow) != 0 {
		t.Errorf("expected empty window past end of list, got %d", len(emptyWindow))
	}

	negWindow := ad.CandidateRowsWindow(viewmodel.FilterAll, -5, 10)
	if len(negWindow) != 10 {
		t.Errorf("expected 10 rows for negative offset, got %d", len(negWindow))
	}
}

func TestAdapter_FilteredCountAndTierOrdering(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)

	now := time.Now()
	// Tier 1: Gemini servable
	st.PutWithTransition(store.Result{
		Link:     "vless://t1@1.1.1.1:443#Tier1",
		Status:   store.StatusPassed,
		Latency:  100 * time.Millisecond,
		TestedAt: now,
	})

	// Tier 2: Generic servable (target blocked)
	st.PutWithTransition(store.Result{
		Link:                   "vless://t2@2.2.2.2:443#Tier2",
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportOK:            true,
		TransportEvidenceKnown: true,
		TransportLatency:       50 * time.Millisecond,
		TestedAt:               now,
	})

	// Tier 3: Unservable (transport failure)
	st.PutWithTransition(store.Result{
		Link:                   "vless://t3@3.3.3.3:443#Tier3",
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		TransportOK:            false,
		TransportEvidenceKnown: true,
		TestedAt:               now,
	})

	if ad.Snapshot(viewmodel.FilterAll).TotalRows != 3 {
		t.Errorf("expected 3 in FilterAll, got %d", ad.Snapshot(viewmodel.FilterAll).TotalRows)
	}
	if ad.Snapshot(viewmodel.FilterGemini).TotalRows != 1 {
		t.Errorf("expected 1 in FilterGemini, got %d", ad.Snapshot(viewmodel.FilterGemini).TotalRows)
	}
	if ad.Snapshot(viewmodel.FilterGeneric).TotalRows != 2 {
		t.Errorf("expected 2 in FilterGeneric, got %d", ad.Snapshot(viewmodel.FilterGeneric).TotalRows)
	}

	// Verify window ordering conforms to Tier 1 -> Tier 2 -> Tier 3
	rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0].Remark != "Tier1" {
		t.Errorf("expected row 0 to be Tier1, got %s", rows[0].Remark)
	}
	if rows[1].Remark != "Tier2" {
		t.Errorf("expected row 1 to be Tier2, got %s", rows[1].Remark)
	}
	if rows[2].Remark != "Tier3" {
		t.Errorf("expected row 2 to be Tier3, got %s", rows[2].Remark)
	}
}

func TestAdapter_ReconcileCycleStateWithCandidateIndex(t *testing.T) {
	ad, st, bus, _ := setupTestAdapter(t)
	ad.Subscribe()

	// Simulate running cycle
	bus.Publish(events.CycleStarted{StartedAt: time.Now()})
	time.Sleep(20 * time.Millisecond)

	// Add candidates and finish cycle in Store directly (simulating lost event)
	st.PutWithTransition(store.Result{
		Link:     "vless://rec@1.1.1.1:443#Reconciled",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})
	st.FinishCycle()

	// CheckAndResetDirty will call reconcileCycleStateLocked
	dirty := ad.CheckAndResetDirty()
	if !dirty {
		t.Errorf("expected dirty after cycle finish")
	}

	snap := ad.Snapshot(viewmodel.FilterAll)
	if snap.Header.CycleStatus != viewmodel.CycleIdle {
		t.Errorf("expected CycleIdle after reconciliation, got %s", snap.Header.CycleStatus)
	}
	if snap.Header.PassedCount != 1 {
		t.Errorf("expected 1 passed count, got %d", snap.Header.PassedCount)
	}
}

func TestAdapter_IndexRebuildOverdueAfterThrottleExpires(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)
	// 50ms throttle interval
	ad.SetIndexThrottleIntervalForTest(50 * time.Millisecond)

	linkA := "vless://a@1.1.1.1:443#NodeA"
	st.PutWithTransition(store.Result{
		Link:     linkA,
		Status:   store.StatusPassed,
		Latency:  100 * time.Millisecond,
		TestedAt: time.Now(),
	})

	// Initial snapshot rebuilds index
	snap, updated := ad.PollSnapshot(viewmodel.FilterAll)
	if !updated || len(snap.Rows) != 1 {
		t.Fatalf("expected initial updated snapshot with 1 row")
	}
	initRev := ad.LastIndexRevForTest()
	if initRev != st.Revision() {
		t.Fatalf("expected index rev %d to match store rev %d", initRev, st.Revision())
	}

	// Mutate candidate inside throttle window (< 50ms)
	st.PutWithTransition(store.Result{
		Link:     linkA,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		TestedAt: time.Now(),
	})
	mutRev := st.Revision()
	if mutRev == initRev {
		t.Fatalf("expected store revision increment on mutation")
	}

	// Intermediate tick calls PollSnapshot, which checks dirty and runs rebuildIndexLocked(false).
	// Because < 50ms elapsed and candidate count is unchanged (1 == 1), index rebuild throttles.
	snap2, updated2 := ad.PollSnapshot(viewmodel.FilterAll)
	if !updated2 {
		t.Fatalf("expected PollSnapshot to return updated because store revision changed")
	}
	if ad.LastIndexRevForTest() != initRev {
		t.Fatalf("expected index to be throttled (rev %d, expected %d)", ad.LastIndexRevForTest(), initRev)
	}
	// Row status in throttled index is still old "PASS"
	if len(snap2.Rows) != 1 || snap2.Rows[0].Status != "PASS" {
		t.Fatalf("expected throttled rows to still show PASS, got %v", snap2.Rows[0].Status)
	}

	// Probing pauses. Immediate check before 50ms expires returns dirty=false
	if ad.CheckAndResetDirty() {
		t.Fatalf("expected not dirty before throttle expires with no store mutations")
	}

	// Wait for throttle duration to elapse
	time.Sleep(65 * time.Millisecond)

	// Invariant: overdue throttled index MUST trigger dirty automatically even with 0 new Store mutations
	snap3, updated3 := ad.PollSnapshot(viewmodel.FilterAll)
	if !updated3 {
		t.Fatalf("expected updated snapshot after throttle expiration for pending stale index")
	}
	if ad.LastIndexRevForTest() != mutRev {
		t.Fatalf("expected index rev %d to match mutated store rev %d", ad.LastIndexRevForTest(), mutRev)
	}
	if len(snap3.Rows) != 1 || snap3.Rows[0].Status != "BLOCKED" {
		t.Fatalf("expected rebuilt rows to show mutated status BLOCKED, got %s", snap3.Rows[0].Status)
	}
}

func TestAdapter_RapidStoreRevisionsEventuallyRebuilt(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)
	ad.SetIndexThrottleIntervalForTest(40 * time.Millisecond)

	link := "vless://rapid@1.1.1.1:443#Rapid"
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})
	ad.PollSnapshot(viewmodel.FilterAll)

	// 10 rapid mutations in tight loop
	for i := 1; i <= 10; i++ {
		st.PutWithTransition(store.Result{
			Link:     link,
			Status:   store.StatusPassed,
			Latency:  time.Duration(i*10) * time.Millisecond,
			TestedAt: time.Now(),
		})
	}
	finalRev := st.Revision()

	// Wait past throttle
	time.Sleep(55 * time.Millisecond)

	if !ad.CheckAndResetDirty() {
		t.Fatalf("expected dirty after rapid mutations and throttle expiration")
	}
	snap, updated := ad.PollSnapshot(viewmodel.FilterAll)
	if !updated {
		t.Fatalf("expected updated snapshot")
	}
	if ad.LastIndexRevForTest() != finalRev {
		t.Fatalf("expected index rev %d to match final rev %d", ad.LastIndexRevForTest(), finalRev)
	}
	if len(snap.Rows) != 1 || snap.Rows[0].LatencyFormatted != "100ms" {
		t.Errorf("expected final latency 100ms, got %s", snap.Rows[0].LatencyFormatted)
	}
}

func TestAdapter_OpaqueIDMapBoundedLifetimeAcrossChurn(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	// Store with MaxAbsentCycles = 1 (eviction after 1 cycle of absence)
	st := store.New(stateFile, 1)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)
	ad.SetIndexThrottleIntervalForTest(0)
	defer ad.Close()
	defer bus.Close()

	// Initial pool: 10 candidates (0..9)
	initialIDs := make(map[int]string)
	for i := 0; i < 10; i++ {
		link := fmt.Sprintf("vless://node-%d@1.1.1.1:443#Node-%d", i, i)
		st.PutWithTransition(store.Result{Link: link, Status: store.StatusPassed, TestedAt: time.Now()})
	}
	rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 20)
	if len(rows) != 10 {
		t.Fatalf("expected 10 initial rows, got %d", len(rows))
	}
	for _, r := range rows {
		var idx int
		fmt.Sscanf(r.Remark, "Node-%d", &idx)
		initialIDs[idx] = r.ID
	}

	linkCount, idCount := ad.OpaqueIDMapCountsForTest()
	if linkCount != 10 || idCount != 10 {
		t.Fatalf("expected initial map sizes (10, 10), got (%d, %d)", linkCount, idCount)
	}

	// Churn simulation: rotate 5 candidates out and 5 candidates in over 4 cycles
	for cycle := 1; cycle <= 4; cycle++ {
		startIdx := cycle * 5
		endIdx := startIdx + 10 // Active window of 10 candidates

		// Recreate state with the 10 currently active candidates
		stNew := store.New(stateFile, 1)
		for i := startIdx; i < endIdx; i++ {
			link := fmt.Sprintf("vless://node-%d@1.1.1.1:443#Node-%d", i, i)
			stNew.PutWithTransition(store.Result{Link: link, Status: store.StatusPassed, TestedAt: time.Now()})
		}
		if err := stNew.Save(); err != nil {
			t.Fatalf("failed to save state in cycle %d: %v", cycle, err)
		}
		if err := st.Load(); err != nil {
			t.Fatalf("failed to load state in cycle %d: %v", cycle, err)
		}

		// Re-fetch window
		activeRows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 20)
		if len(activeRows) != 10 {
			t.Fatalf("cycle %d: expected 10 active rows, got %d", cycle, len(activeRows))
		}

		// Invariant: Opaque ID maps MUST be strictly bounded to active candidate count (10)
		curLinks, curIDs := ad.OpaqueIDMapCountsForTest()
		if curLinks != 10 || curIDs != 10 {
			t.Fatalf("cycle %d: memory leak detected! Expected map sizes (10, 10), got (%d, %d)",
				cycle, curLinks, curIDs)
		}

		// Verify surviving candidates retained their stable IDs
		for _, r := range activeRows {
			var idx int
			fmt.Sscanf(r.Remark, "Node-%d", &idx)
			if prevID, hadPrev := initialIDs[idx]; hadPrev {
				if r.ID != prevID {
					t.Errorf("cycle %d: candidate Node-%d changed ID from %s to %s", cycle, idx, prevID, r.ID)
				}
			} else {
				initialIDs[idx] = r.ID
			}
		}

		// Verify evicted candidates from older cycles return false on detail query
		for i := 0; i < startIdx; i++ {
			if oldID, hadID := initialIDs[i]; hadID {
				if _, found := ad.CandidateDetail(oldID); found {
					t.Errorf("cycle %d: evicted candidate Node-%d (ID %s) still found in detail", cycle, i, oldID)
				}
			}
		}
	}
}

// TestAdapter_CandidateRowsWindow_DoesNotInvokeStats verifies that the interactive viewport
// window materialization path executes in sub-millisecond time without executing full Store.Stats() traversals.
func TestAdapter_CandidateRowsWindow_DoesNotInvokeStats(t *testing.T) {
	ad, st, bus, ring := setupTestAdapter(t)
	defer ad.Close()
	defer bus.Close()
	_ = ring

	// Seed 5,000 candidates
	now := time.Now()
	for i := 0; i < 5000; i++ {
		link := fmt.Sprintf("vless://user-%d@1.1.1.1:443#Node-%d", i, i)
		st.PutWithTransition(store.Result{
			Link:                   link,
			Status:                 store.StatusPassed,
			Latency:                time.Duration(50+i%200) * time.Millisecond,
			TransportOK:            true,
			TransportEvidenceKnown: true,
			TestedAt:               now,
		})
	}

	// Prime cached index
	_ = ad.Snapshot(viewmodel.FilterAll)

	// Warm up window query path
	_ = ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 25)

	// Measure baseline cost of 5 full Store.Stats() traversals
	startStats := time.Now()
	for i := 0; i < 5; i++ {
		_ = st.Stats()
	}
	statsDuration := time.Since(startStats)

	// Measure 20 consecutive window queries on the warm index
	startWindow := time.Now()
	for i := 0; i < 20; i++ {
		rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 100+i, 25)
		if len(rows) != 25 {
			t.Fatalf("expected 25 rows, got %d", len(rows))
		}
	}
	windowDuration := time.Since(startWindow)

	// Invariant: 20 window queries must NOT perform 20 full Stats() traversals (which would take ~4x statsDuration).
	// With O(1) Count(), 20 window queries complete in a fraction of that time.
	if windowDuration >= 2*statsDuration {
		t.Errorf("CandidateRowsWindow invoked expensive traversal: 20 window queries took %v, 5 Stats took %v",
			windowDuration, statsDuration)
	}
}
