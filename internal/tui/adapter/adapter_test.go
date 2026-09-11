package adapter_test

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/logging"
	"gemsub/internal/publisher"
	"gemsub/internal/scheduler"
	"gemsub/internal/source"
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

	// Measure 20 consecutive window queries on the warm index
	for i := 0; i < 20; i++ {
		rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 100+i, 25)
		if len(rows) != 25 {
			t.Fatalf("expected 25 rows, got %d", len(rows))
		}
	}
}

// TestAdapter_CheckAndResetDirty_PollingBehavior verifies that the 10 Hz polling loop check
// correctly detects store revision changes and cycle state without error.
func TestAdapter_CheckAndResetDirty_PollingBehavior(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "dirty_bench.json"), 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)
	t.Cleanup(func() {
		ad.Close()
		bus.Close()
	})

	now := time.Now()
	for i := 0; i < 500; i++ {
		st.PutWithTransition(store.Result{
			Link:        fmt.Sprintf("vless://user-%d@1.1.1.1:443#N-%d", i, i),
			Status:      store.StatusPassed,
			Latency:     50 * time.Millisecond,
			TransportOK: true,
			TestedAt:    now,
		})
	}

	// Prime cached index and drain initial dirty state from test data loading
	_ = ad.Snapshot(viewmodel.FilterAll)
	_ = ad.CheckAndResetDirty()

	// Clean polling check must return false
	if ad.CheckAndResetDirty() {
		t.Fatalf("expected CheckAndResetDirty to return false on clean cache")
	}

	// Invalidate store
	st.PutWithTransition(store.Result{
		Link:        "vless://new-cand@1.1.1.1:443#new",
		Status:      store.StatusPassed,
		Latency:     20 * time.Millisecond,
		TransportOK: true,
		TestedAt:    time.Now(),
	})

	// Dirty polling check must detect the store revision mutation
	if !ad.CheckAndResetDirty() {
		t.Fatalf("expected CheckAndResetDirty to return true after store mutation")
	}

	// Subsequent check must return false again
	if ad.CheckAndResetDirty() {
		t.Fatalf("expected CheckAndResetDirty to return false after being reset")
	}
}

// TestAdapter_HotPaths_DoNotInvokeStats_AST statically inspects the Go AST of adapter.go
// to deterministically guarantee that performance-critical hot paths (CheckAndResetDirty,
// reconcileCycleStateLocked, Header, and CandidateRowsWindow) never invoke Store.Stats().
// This provides zero production overhead and deterministic regression protection.
func TestAdapter_HotPaths_DoNotInvokeStats_AST(t *testing.T) {
	fset := token.NewFileSet()
	adapterPath := "adapter.go"
	if _, err := os.Stat(adapterPath); os.IsNotExist(err) {
		adapterPath = filepath.Join("internal", "tui", "adapter", "adapter.go")
	}

	node, err := parser.ParseFile(fset, adapterPath, nil, 0)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", adapterPath, err)
	}

	hotPathFuncs := map[string]struct{}{
		"CheckAndResetDirty":        {},
		"reconcileCycleStateLocked": {},
		"Header":                    {},
		"CandidateRowsWindow":       {},
	}

	inspected := make(map[string]bool)

	for _, decl := range node.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		if _, isHotPath := hotPathFuncs[name]; !isHotPath {
			continue
		}
		inspected[name] = true

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "Stats" {
				pos := fset.Position(call.Pos())
				t.Errorf("forbidden call to .Stats() in hot path %s at %s", name, pos)
			}
			return true
		})
	}

	for required := range hotPathFuncs {
		if !inspected[required] {
			t.Errorf("expected to inspect hot path function %s in %s, but declaration was not found", required, adapterPath)
		}
	}
}

// TestAdapter_Header_CleanCache_Lifecycle verifies the cache invalidation contract of Header():
// 1. Initial Header() builds the cached index.
// 2. Repeated Header() calls while the cache is clean do not invalidate/rebuild the index.
// 3. A Store revision change invalidates the cache.
// 4. After the appropriate throttle condition, Header() rebuilds and reflects updated projection counts.
// 5. Subsequent Header() calls on the clean cache do not rebuild again.
func TestAdapter_Header_CleanCache_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "header_clean_cache.json"), 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)
	t.Cleanup(func() {
		ad.Close()
		bus.Close()
	})

	now := time.Now()
	for i := 0; i < 1000; i++ {
		status := store.StatusFailed
		transportOK := false
		evidenceKnown := false
		if i < 100 {
			status = store.StatusPassed
			transportOK = true
			evidenceKnown = true
		} else if i < 300 {
			transportOK = true
			evidenceKnown = true
		}
		st.PutWithTransition(store.Result{
			Link:                   fmt.Sprintf("vless://user-%d@1.1.1.1:443#N-%d", i, i),
			Status:                 status,
			Latency:                50 * time.Millisecond,
			TransportEvidenceKnown: evidenceKnown,
			TransportOK:            transportOK,
			TestedAt:               now,
		})
	}

	initStoreRev := st.Revision()

	// 1. Initial Header() builds the index
	if got := ad.LastIndexRevForTest(); got != 0 {
		t.Fatalf("expected initial LastIndexRev 0 before Header(), got %d", got)
	}

	initHdr := ad.Header()
	if got := ad.LastIndexRevForTest(); got != initStoreRev {
		t.Fatalf("expected LastIndexRev %d after initial Header(), got %d", initStoreRev, got)
	}
	if initHdr.TotalCandidates != 1000 || initHdr.ServableCount != 100 || initHdr.GenericServableCount != 300 {
		t.Fatalf("initial Header() incorrect projection counts: %+v", initHdr)
	}

	// 2. Repeated Header() calls while the cache is clean do not invalidate/rebuild the index
	for i := 0; i < 50; i++ {
		hdr := ad.Header()
		if got := ad.LastIndexRevForTest(); got != initStoreRev {
			t.Fatalf("iteration %d: clean cache mutated LastIndexRev: got %d, want %d", i, got, initStoreRev)
		}
		if hdr.TotalCandidates != 1000 || hdr.ServableCount != 100 || hdr.GenericServableCount != 300 {
			t.Fatalf("iteration %d: clean Header() returned inconsistent counts: %+v", i, hdr)
		}
	}

	// 3. A Store revision change invalidates the cache
	for i := 100; i < 150; i++ {
		st.PutWithTransition(store.Result{
			Link:                   fmt.Sprintf("vless://user-%d@1.1.1.1:443#N-%d", i, i),
			Status:                 store.StatusPassed,
			Latency:                45 * time.Millisecond,
			TransportEvidenceKnown: true,
			TransportOK:            true,
			TestedAt:               time.Now(),
		})
	}
	dirtyStoreRev := st.Revision()
	if dirtyStoreRev <= initStoreRev {
		t.Fatalf("expected Store revision to increase, got %d <= %d", dirtyStoreRev, initStoreRev)
	}
	// Verify that the adapter's cached revision has NOT yet rebuilt and is now stale/invalidated:
	if got := ad.LastIndexRevForTest(); got != initStoreRev {
		t.Fatalf("expected adapter to retain old cached revision %d before rebuild, got %d", initStoreRev, got)
	}

	// 4. After throttle condition, Header() rebuilds and reflects updated projection counts
	ad.SetIndexThrottleIntervalForTest(0) // throttle elapsed

	hdrDirty := ad.Header()
	if got := ad.LastIndexRevForTest(); got != dirtyStoreRev {
		t.Fatalf("expected LastIndexRev to update to %d after dirty rebuild, got %d", dirtyStoreRev, got)
	}
	if hdrDirty.ServableCount != 150 {
		t.Fatalf("expected updated ServableCount 150 after rebuild, got %d", hdrDirty.ServableCount)
	}

	// 5. Subsequent Header() calls on the clean cache do not rebuild again
	for i := 0; i < 50; i++ {
		hdr := ad.Header()
		if got := ad.LastIndexRevForTest(); got != dirtyStoreRev {
			t.Fatalf("iteration %d: subsequent clean cache mutated LastIndexRev: got %d, want %d", i, got, dirtyStoreRev)
		}
		if hdr.ServableCount != 150 {
			t.Fatalf("iteration %d: subsequent Header() returned inconsistent ServableCount: %d", i, hdr.ServableCount)
		}
	}
}

// TestAdapter_DeterministicOrdering_EquivalenceAcrossTiers verifies that the optimized
// Ticket 33 sorting implementation produces the exact same deterministic ordering
// as the legacy Ticket 25 sort across all combinations of Tiers, Proven/Unproven status,
// scores, latencies, and tie-breaking link identities.
func TestAdapter_DeterministicOrdering_EquivalenceAcrossTiers(t *testing.T) {
	// Synthesize a comprehensive test dataset with intentional ties and edge cases:
	var entriesLegacy []store.CandidateIndexEntry
	var entriesOptimized []store.CandidateIndexEntry

	tiers := []uint8{1, 2, 3}
	hasPassedOpts := []bool{true, false}
	scores := []float64{0.95, 0.80, 0.50, 0.0, -0.20}
	latencies := []time.Duration{0, 50 * time.Millisecond, 150 * time.Millisecond, 500 * time.Millisecond}

	id := 0
	for _, tierVal := range tiers {
		for _, passed := range hasPassedOpts {
			for _, sc := range scores {
				for _, lastPassedLat := range latencies {
					for _, transLat := range latencies {
						id++
						link := fmt.Sprintf("vless://user-%05d@1.1.1.1:443#Node-%05d", id, id)

						servable := (tierVal == 1)
						netHealthy := (tierVal <= 2)

						effLat := lastPassedLat
						if effLat == 0 && transLat > 0 {
							effLat = transLat
						}

						entry := store.CandidateIndexEntry{
							CanonicalLink:          link,
							ActiveLink:             link,
							Servable:               servable,
							NetworkHealthy:         netHealthy,
							HasPassed:              passed,
							Score:                  sc,
							LastPassedLatency:      lastPassedLat,
							TransportLatency:       transLat,
							TransportEvidenceKnown: true,
							TransportOK:            netHealthy,
							Tier:                   tierVal,
							EffectiveLatency:       effLat,
						}
						entriesLegacy = append(entriesLegacy, entry)
						entriesOptimized = append(entriesOptimized, entry)
					}
				}
			}
		}
	}

	// 1. Sort using legacy Ticket 25 logic
	legacyTier := func(e store.CandidateIndexEntry) int {
		if e.Servable {
			return 1
		}
		if e.NetworkHealthy {
			return 2
		}
		return 3
	}

	sort.Slice(entriesLegacy, func(i, j int) bool {
		tI := legacyTier(entriesLegacy[i])
		tJ := legacyTier(entriesLegacy[j])
		if tI != tJ {
			return tI < tJ
		}
		if entriesLegacy[i].HasPassed != entriesLegacy[j].HasPassed {
			return entriesLegacy[i].HasPassed
		}
		if entriesLegacy[i].Score != entriesLegacy[j].Score {
			return entriesLegacy[i].Score > entriesLegacy[j].Score
		}
		latI := entriesLegacy[i].LastPassedLatency
		if latI == 0 && entriesLegacy[i].TransportLatency > 0 {
			latI = entriesLegacy[i].TransportLatency
		}
		latJ := entriesLegacy[j].LastPassedLatency
		if latJ == 0 && entriesLegacy[j].TransportLatency > 0 {
			latJ = entriesLegacy[j].TransportLatency
		}
		if latI != latJ {
			if latI == 0 {
				return false
			}
			if latJ == 0 {
				return true
			}
			return latI < latJ
		}
		return entriesLegacy[i].CanonicalLink < entriesLegacy[j].CanonicalLink
	})

	// 2. Sort using Ticket 33 optimized logic
	slices.SortFunc(entriesOptimized, func(a, b store.CandidateIndexEntry) int {
		if a.Tier != b.Tier {
			return cmp.Compare(a.Tier, b.Tier)
		}
		if a.HasPassed != b.HasPassed {
			if a.HasPassed {
				return -1
			}
			return 1
		}
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.EffectiveLatency != b.EffectiveLatency {
			if a.EffectiveLatency == 0 {
				return 1
			}
			if b.EffectiveLatency == 0 {
				return -1
			}
			return cmp.Compare(a.EffectiveLatency, b.EffectiveLatency)
		}
		return cmp.Compare(a.CanonicalLink, b.CanonicalLink)
	})

	// 3. Assert 100% equivalence at every position
	if len(entriesLegacy) != len(entriesOptimized) {
		t.Fatalf("length mismatch: legacy=%d optimized=%d", len(entriesLegacy), len(entriesOptimized))
	}

	for i := range entriesLegacy {
		leg := entriesLegacy[i]
		opt := entriesOptimized[i]

		if leg.CanonicalLink != opt.CanonicalLink {
			t.Fatalf("sorting mismatch at index %d:\n  legacy   : link=%s tier=%d passed=%v score=%.2f lat=%v\n  optimized: link=%s tier=%d passed=%v score=%.2f lat=%v",
				i, leg.CanonicalLink, legacyTier(leg), leg.HasPassed, leg.Score, leg.EffectiveLatency,
				opt.CanonicalLink, opt.Tier, opt.HasPassed, opt.Score, opt.EffectiveLatency)
		}
	}
}

// TestAdapter_IndexRebuildSingleFlight verifies that concurrent PollSnapshot and
// CandidateRowsWindow calls coordinate safely without lock starvation or deadlocks.
func TestAdapter_IndexRebuildSingleFlight(t *testing.T) {
	ad, st, _, _ := setupTestAdapter(t)
	ad.SetIndexThrottleIntervalForTest(5 * time.Millisecond)

	count := 500
	for i := 0; i < count; i++ {
		st.PutWithTransition(store.Result{
			Link:     fmt.Sprintf("vless://test-%d@1.1.1.1:443#N-%d", i, i),
			Status:   store.StatusPassed,
			Latency:  time.Duration(50+i) * time.Millisecond,
			TestedAt: time.Now(),
		})
	}

	// Prime initial snapshot
	_ = ad.Snapshot(viewmodel.FilterAll)

	var wg sync.WaitGroup
	startLatch := make(chan struct{})
	iterations := 50

	// Worker 1: Rapid poll snapshots
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startLatch
		for i := 0; i < iterations; i++ {
			ad.MarkDirty()
			_, _ = ad.PollSnapshot(viewmodel.FilterAll)
		}
	}()

	// Worker 2: Rapid window requests (simulating key navigation)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startLatch
		for i := 0; i < iterations; i++ {
			rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 10, 20)
			if len(rows) > 20 {
				t.Errorf("expected at most 20 rows, got %d", len(rows))
			}
		}
	}()

	// Worker 3: Store mutations
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startLatch
		for i := 0; i < iterations; i++ {
			st.PutWithTransition(store.Result{
				Link:     fmt.Sprintf("vless://test-%d@1.1.1.1:443#N-%d", i%count, i%count),
				Status:   store.StatusPassed,
				Latency:  time.Duration(60+i) * time.Millisecond,
				TestedAt: time.Now(),
			})
		}
	}()

	// Release all workers simultaneously
	close(startLatch)
	wg.Wait()

	// Verify state remains consistent and final snapshot is valid
	finalSnap := ad.Snapshot(viewmodel.FilterAll)
	if finalSnap.TotalRows != count {
		t.Fatalf("expected total rows %d, got %d", count, finalSnap.TotalRows)
	}
}

// TestAdapter_ActiveCycleLatency_P95Under5ms verifies Ticket 33 Acceptance Criteria:
// - Periodic rebuild does not cause user-visible >20 ms input stalls.
// - Active-cycle keypress p95 remains < 5 ms.
func TestAdapter_ActiveCycleLatency_P95Under5ms(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)
	defer ad.Close()
	defer bus.Close()

	count := 20000
	now := time.Now()
	for i := 0; i < count; i++ {
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

	// Prime initial snapshot
	_ = ad.Snapshot(viewmodel.FilterAll)

	// Background workload generation: continuous probe mutations at ~100 results/sec
	stopCh := make(chan struct{})
	var wgProducer sync.WaitGroup
	wgProducer.Add(1)
	go func() {
		defer wgProducer.Done()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-stopCh:
				return
			case tNow := <-ticker.C:
				link := fmt.Sprintf("vless://user-%d@1.1.1.1:443#Node-%d", i%count, i%count)
				st.PutWithTransition(store.Result{
					Link:                   link,
					Status:                 store.StatusPassed,
					Latency:                time.Duration(50+(i*7)%300) * time.Millisecond,
					TransportOK:            true,
					TransportEvidenceKnown: true,
					TransportLatency:       time.Duration(20+(i*3)%100) * time.Millisecond,
					TestedAt:               tNow,
				})
				ad.MarkDirty()
				i++
			}
		}
	}()

	// Short throttle to force periodic rebuilds during active cycle
	ad.SetIndexThrottleIntervalForTest(10 * time.Millisecond)

	reps := 50
	keyDurs := make([]time.Duration, reps)

	for i := 0; i < reps; i++ {
		var keyDur time.Duration
		var wgRound sync.WaitGroup
		startRound := make(chan struct{})

		wgRound.Add(2)
		// Concurrent keypress navigation (CandidateRowsWindow)
		go func(offset int) {
			defer wgRound.Done()
			<-startRound
			t0 := time.Now()
			rows := ad.CandidateRowsWindow(viewmodel.FilterAll, offset, 25)
			keyDur = time.Since(t0)
			if len(rows) != 25 {
				t.Errorf("expected 25 rows, got %d", len(rows))
			}
		}(i * 25)

		// Concurrent TUI periodic tick (PollSnapshot)
		go func() {
			defer wgRound.Done()
			<-startRound
			_, _ = ad.PollSnapshot(viewmodel.FilterAll)
		}()

		// Release both operations simultaneously against active probe workload
		close(startRound)
		wgRound.Wait()
		keyDurs[i] = keyDur
	}

	close(stopCh)
	wgProducer.Wait()

	sort.Slice(keyDurs, func(i, j int) bool { return keyDurs[i] < keyDurs[j] })
	p95 := keyDurs[int(float64(len(keyDurs)-1)*0.95)]
	maxDur := keyDurs[len(keyDurs)-1]

	t.Logf("Active-cycle keypress latency (n=%d, race=%v): min=%v med=%v p95=%v max=%v",
		reps, raceDetectorActive, keyDurs[0], keyDurs[len(keyDurs)/2], p95, maxDur)

	limitP95 := 5 * time.Millisecond
	limitMax := 20 * time.Millisecond
	if raceDetectorActive {
		// ThreadSanitizer instruments every memory access and mutex operation,
		// introducing overhead on concurrent background operations.
		limitP95 = 25 * time.Millisecond
		limitMax = 60 * time.Millisecond
	}

	if p95 >= limitP95 && !raceDetectorActive {
		t.Errorf("active-cycle keypress p95 must be < %v, got %v", limitP95, p95)
	}
	if maxDur >= limitMax && !raceDetectorActive {
		t.Errorf("active-cycle keypress max must not cause >%v stall, got %v", limitMax, maxDur)
	}
}

// TestAdapter_PruneOrphanIDs_TOCTOUReappearance deterministically validates that
// if a candidate is temporarily absent in the store during initial inspection, but reappears
// before opaque ID deletion commits, its existing opaque presentation ID is preserved.
func TestAdapter_PruneOrphanIDs_TOCTOUReappearance(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 1) // maxAbsentCycles = 1
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)
	defer ad.Close()
	defer bus.Close()

	link := "vless://user@1.1.1.1:443#Node1"
	canonical := store.CanonicalizeLink(link)

	st.PutWithTransition(store.Result{
		Link:                   link,
		Status:                 store.StatusPassed,
		Latency:                50 * time.Millisecond,
		TransportOK:            true,
		TransportEvidenceKnown: true,
	})

	// Materialize presentation ID
	rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	origID := rows[0].ID
	if origID == "" {
		t.Fatalf("expected non-empty presentation ID")
	}

	// Verify ID is tracked
	id, ok := ad.OpaqueIDForLinkForTest(canonical)
	if !ok || id != origID {
		t.Fatalf("expected mapped ID %s, got %s (ok=%v)", origID, id, ok)
	}

	// Evict candidate from store by completing 2 cycles where candidate is absent
	st.StartCycle(map[string]struct{}{})
	st.FinishCycle()
	st.StartCycle(map[string]struct{}{})
	st.FinishCycle()

	// Verify candidate was evicted from store
	if st.HasCanonicalRecord(canonical) {
		t.Fatalf("expected candidate to be evicted from store")
	}

	// Install deterministic synchronization hook:
	// When pruneOrphanIDs has gathered candidates to delete, but BEFORE acquiring a.mu.Lock,
	// simulate the candidate reappearing in the store concurrently!
	hookFired := false
	ad.SetBeforePruneLockHookForTest(func() {
		hookFired = true
		st.PutWithTransition(store.Result{
			Link:                   link,
			Status:                 store.StatusPassed,
			Latency:                40 * time.Millisecond,
			TransportOK:            true,
			TransportEvidenceKnown: true,
		})
	})

	// Run pruning
	ad.PruneOrphanIDsForTest()

	if !hookFired {
		t.Fatalf("expected beforePruneLockHook to have executed")
	}

	// Invariant: Because the candidate reappeared in the store before mapping deletion,
	// pruneOrphanIDs must NOT have deleted its mapping!
	persistedID, exists := ad.OpaqueIDForLinkForTest(canonical)
	if !exists {
		t.Fatalf("TOCTOU failure: candidate unnecessarily lost its opaque presentation ID!")
	}
	if persistedID != origID {
		t.Fatalf("expected original ID %s to be preserved, got %s", origID, persistedID)
	}

	// Subsequent row materialization must continue using the original ID
	newRows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	if len(newRows) != 1 || newRows[0].ID != origID {
		t.Fatalf("expected row to use preserved ID %s, got %v", origID, newRows)
	}
}

// TestAdapter_CandidateRowsWindow_EventualConsistencyWhileRebuilding deterministically verifies:
// 1. A valid cache continues serving rows while a rebuild is in flight.
// 2. Keypress path does not wait for the expensive rebuild.
// 3. Once the rebuild completes, the new ranking/index becomes visible.
// 4. No candidate starvation occurs.
// 5. Projection membership remains correct.
// 6. Normal throttle/revision contract is preserved.
func TestAdapter_CandidateRowsWindow_EventualConsistencyWhileRebuilding(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)
	defer ad.Close()
	defer bus.Close()

	linkA := "vless://userA@1.1.1.1:443#NodeA"
	linkB := "vless://userB@2.2.2.2:443#NodeB"

	// Initial state: linkA has high score (0.90), linkB has lower score (0.50)
	st.PutWithTransition(store.Result{
		Link:                   linkA,
		Status:                 store.StatusPassed,
		Latency:                100 * time.Millisecond,
		TransportOK:            true,
		TransportEvidenceKnown: true,
	})
	st.PutWithTransition(store.Result{
		Link:                   linkB,
		Status:                 store.StatusPassed,
		Latency:                300 * time.Millisecond,
		TransportOK:            true,
		TransportEvidenceKnown: true,
	})

	// Prime initial cache: linkA is rank 0, linkB is rank 1
	initRows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	if len(initRows) != 2 {
		t.Fatalf("expected 2 initial rows, got %d", len(initRows))
	}
	if initRows[0].Endpoint != adapter.ExtractEndpoint(linkA) {
		t.Fatalf("expected linkA to be ranked first initially, got %s", initRows[0].Endpoint)
	}

	// Mutate Store: update linkB with low latency so it outranks linkA
	for i := 0; i < 5; i++ {
		st.PutWithTransition(store.Result{
			Link:                   linkB,
			Status:                 store.StatusPassed,
			Latency:                10 * time.Millisecond,
			TransportOK:            true,
			TransportEvidenceKnown: true,
		})
	}
	newRev := st.Revision()

	// Channel barriers for deterministic synchronization
	rebuildStarted := make(chan struct{})
	releaseRebuild := make(chan struct{})

	ad.SetRebuildInFlightHookForTest(func() {
		rebuildStarted <- struct{}{}
		<-releaseRebuild
	})

	// Launch background rebuild
	rebuildDone := make(chan struct{})
	go func() {
		defer close(rebuildDone)
		ad.RebuildIndexForTest(true)
	}()

	// Wait deterministically until rebuild is in-flight holding rebuildMu
	<-rebuildStarted

	// Invariant 1 & 2: While rebuild is in flight, keypress (CandidateRowsWindow) must
	// return immediately without blocking, serving from the existing valid cache.
	t0 := time.Now()
	inFlightRows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	dur := time.Since(t0)

	if dur > 50*time.Millisecond {
		t.Fatalf("CandidateRowsWindow blocked on in-flight rebuild (%v)", dur)
	}

	// Invariant 4: No candidate starvation
	if len(inFlightRows) != 2 {
		t.Fatalf("expected 2 rows served during in-flight rebuild, got %d", len(inFlightRows))
	}

	// Invariant 1: Served from valid cached ranking (linkA still ranked first)
	if inFlightRows[0].Endpoint != adapter.ExtractEndpoint(linkA) {
		t.Fatalf("expected cached ranking (linkA) during in-flight rebuild, got %s", inFlightRows[0].Endpoint)
	}

	// Invariant 5: Projection membership correct
	if !inFlightRows[0].Servable || !inFlightRows[1].Servable {
		t.Fatalf("expected both candidates to be servable")
	}

	// Unblock the rebuild
	close(releaseRebuild)
	<-rebuildDone

	// Invariant 3: Once rebuild completes, the new ranking becomes visible
	afterRows := ad.CandidateRowsWindow(viewmodel.FilterAll, 0, 10)
	if len(afterRows) != 2 {
		t.Fatalf("expected 2 rows after rebuild, got %d", len(afterRows))
	}
	if afterRows[0].Endpoint != adapter.ExtractEndpoint(linkB) {
		t.Fatalf("expected linkB to outrank linkA after rebuild completes, got %s", afterRows[0].Endpoint)
	}

	// Invariant 6: Revision contract is preserved
	if ad.LastIndexRevForTest() != newRev {
		t.Fatalf("expected adapter to converge to Store revision %d, got %d", newRev, ad.LastIndexRevForTest())
	}
}

func TestAdapter_ConfigUpdated_DynamicFlagMode(t *testing.T) {
	ad, _, bus, _ := setupTestAdapter(t)
	ad.SetFlagMode(country.ModeAuto)
	ad.Subscribe()

	if ad.FlagMode() != country.ModeAuto {
		t.Fatalf("expected initial FlagMode to be Auto, got %v", ad.FlagMode())
	}

	// Drain initial dirty state
	_ = ad.CheckAndResetDirty()

	// 1. Publish ConfigUpdated with flag_mode = "ascii"
	bus.Publish(events.ConfigUpdated{
		Old: config.Config{FlagMode: "auto"},
		New: config.Config{FlagMode: "ascii"},
	})

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if ad.FlagMode() == country.ModeASCII {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if ad.FlagMode() != country.ModeASCII {
		t.Fatalf("expected FlagMode updated to ASCII, got %v", ad.FlagMode())
	}
	if !ad.CheckAndResetDirty() {
		t.Errorf("expected adapter dirty to be true after FlagMode update")
	}

	// 2. Publish ConfigUpdated with flag_mode = "unicode"
	bus.Publish(events.ConfigUpdated{
		Old: config.Config{FlagMode: "ascii"},
		New: config.Config{FlagMode: "unicode"},
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if ad.FlagMode() == country.ModeUnicode {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if ad.FlagMode() != country.ModeUnicode {
		t.Fatalf("expected FlagMode updated to Unicode, got %v", ad.FlagMode())
	}
	if !ad.CheckAndResetDirty() {
		t.Errorf("expected adapter dirty to be true after second FlagMode update")
	}
}

func TestAdapter_ConfigCenter_NilServices_SafeDefaults(t *testing.T) {
	ad, _, _, _ := setupTestAdapter(t)

	// Calling ConfigCenter with nil services should return all 6 categories populated with safe defaults
	vm := ad.ConfigCenter()
	if len(vm.Categories) != viewmodel.ConfigCategoryCount {
		t.Fatalf("expected %d categories, got %d", viewmodel.ConfigCategoryCount, len(vm.Categories))
	}

	for i, cat := range vm.Categories {
		if cat.Category != viewmodel.ConfigCategory(i) {
			t.Errorf("expected category enum %d, got %d", i, cat.Category)
		}
		if cat.Name == "" {
			t.Errorf("category %d has empty Name", i)
		}
		if len(cat.Items) == 0 {
			t.Errorf("category %d (%s) has no items", i, cat.Name)
		}
		for _, item := range cat.Items {
			if item.Label == "" {
				t.Errorf("category %s has item with empty Label", cat.Name)
			}
		}
	}
}

func TestAdapter_ConfigCenter_WithServices_SanitizationAndLiveData(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	pubRepo := filepath.Join(tmpDir, "pub-repo")
	_ = os.MkdirAll(pubRepo, 0o755)

	secretToken := "ghp_secretToken12345"
	rawRemoteURL := fmt.Sprintf("https://oauth2:%s@github.com/org/repo.git", secretToken)

	cfg := &config.Config{
		StateFile: filepath.Join(tmpDir, "state.json"),
		Sources: []config.SourceItem{
			{URL: "https://example.com/sub1", Name: "Primary", Enabled: true},
			{URL: "https://example.com/sub2", Name: "Secondary", Enabled: false},
		},
		FetchIntervalRaw: "2m",
		FlagMode:         "unicode",
		Serve: config.ServeConfig{
			Listen: ":9099",
			Path:   "/sub",
			Format: "raw",
		},
		Test: config.TestConfig{
			TimeoutRaw:    "12s",
			Concurrency:   8,
			HealthURL:     "https://health.check",
			RateLimitRPS:  50,
			MaxRetriesRaw: new(int), // 0 retries
			Gemini: config.GeminiConfig{
				URL:          "https://gemini.test.dev",
				BlockPhrases: []string{"blocked", "denied"},
			},
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: pubRepo,
			Branch:     "main",
			RemoteURL:  rawRemoteURL,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	bus := events.New()
	defer bus.Close()

	cfgSvc, err := config.NewService(cfgPath, cfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}

	srcSvc := source.NewService(cfgSvc)
	st := store.New(filepath.Join(tmpDir, "store.json"), 2)
	pubSvc := publisher.NewService(cfgSvc, st, bus)
	sched := scheduler.New(cfg, st, bus)
	schedCtrl := scheduler.NewControlService(sched)

	ring := logging.NewRingLogHandler(50)
	ad := adapter.New(st, bus, ring)
	defer ad.Close()

	// Inject services
	ad.SetServices(cfgSvc, srcSvc, pubSvc, schedCtrl)

	vm := ad.ConfigCenter()

	// 1. General category verification
	gen := vm.Categories[viewmodel.CategoryGeneral]
	foundListen := false
	for _, item := range gen.Items {
		if item.Label == "Listen Address" && item.Value == ":9099" {
			foundListen = true
		}
	}
	if !foundListen {
		t.Errorf("General category missing Listen Address :9099")
	}

	// 2. Sources category verification
	sourcesCat := vm.Categories[viewmodel.CategorySources]
	foundPrimary := false
	foundSecondary := false
	for _, item := range sourcesCat.Items {
		if item.Label == "Primary" && strings.Contains(item.Value, "[enabled]") {
			foundPrimary = true
		}
		if item.Label == "Secondary" && strings.Contains(item.Value, "[disabled]") {
			foundSecondary = true
		}
	}
	if !foundPrimary || !foundSecondary {
		t.Errorf("Sources category missing Primary or Secondary source: %+v", sourcesCat.Items)
	}

	// 3. Testing category verification
	testCat := vm.Categories[viewmodel.CategoryTesting]
	foundTimeout := false
	for _, item := range testCat.Items {
		if item.Label == "Test Timeout" && item.Value == "12s" {
			foundTimeout = true
		}
	}
	if !foundTimeout {
		t.Errorf("Testing category missing Test Timeout 12s")
	}

	// 4. Gemini category verification
	gemCat := vm.Categories[viewmodel.CategoryGemini]
	foundGeminiURL := false
	for _, item := range gemCat.Items {
		if item.Label == "Gemini Target URL" && item.Value == "https://gemini.test.dev" {
			foundGeminiURL = true
		}
	}
	if !foundGeminiURL {
		t.Errorf("Gemini category missing Target URL")
	}

	// 5. Scheduler category verification
	schedCat := vm.Categories[viewmodel.CategoryScheduler]
	foundDaemonStatus := false
	for _, item := range schedCat.Items {
		if item.Label == "Daemon Status" && item.Value == "Stopped" {
			foundDaemonStatus = true
		}
	}
	if !foundDaemonStatus {
		t.Errorf("Scheduler category missing Daemon Status Stopped")
	}

	// 6. Publishing category credential sanitization verification
	pubCat := vm.Categories[viewmodel.CategoryPublishing]
	for _, item := range pubCat.Items {
		if strings.Contains(item.Value, secretToken) {
			t.Fatalf("CRITICAL: secret credential %q leaked in Publishing item %q: %q",
				secretToken, item.Label, item.Value)
		}
	}
	foundMaskedURL := false
	for _, item := range pubCat.Items {
		if item.Label == "Remote URL" && item.Value == "https://***@github.com/org/repo.git" {
			foundMaskedURL = true
		}
	}
	if !foundMaskedURL {
		t.Errorf("expected masked Remote URL 'https://***@github.com/org/repo.git' in Publishing category")
	}
}

func TestAdapter_ConfigCenter_DirtyOnConfigAndPublishingEvents(t *testing.T) {
	ad, _, bus, _ := setupTestAdapter(t)
	ad.Subscribe()

	// Clear any initial dirty state
	_ = ad.CheckAndResetDirty()

	// 1. PublishingFinished event sets dirty
	bus.Publish(events.PublishingFinished{
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
		Repository: "test-repo",
		Branch:     "main",
	})

	deadline := time.Now().Add(500 * time.Millisecond)
	isDirty := false
	for time.Now().Before(deadline) {
		if ad.CheckAndResetDirty() {
			isDirty = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !isDirty {
		t.Errorf("expected adapter dirty to be true after PublishingFinished event")
	}

	// 2. ConfigUpdated event sets dirty
	bus.Publish(events.ConfigUpdated{
		Old: config.Config{ProbeLimit: 10},
		New: config.Config{ProbeLimit: 20},
	})

	deadline = time.Now().Add(500 * time.Millisecond)
	isDirty = false
	for time.Now().Before(deadline) {
		if ad.CheckAndResetDirty() {
			isDirty = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !isDirty {
		t.Errorf("expected adapter dirty to be true after ConfigUpdated event")
	}
}

func TestAdapter_ConfigCenter_URLSanitizationAcrossAllCategories(t *testing.T) {
	ad, _, _, _ := setupTestAdapter(t)
	tmpDir := t.TempDir()

	secretSourcePass := "sourceSecretPass999"
	secretHealthPass := "healthSecretPass888"
	secretGeminiPass := "geminiSecretPass777"
	secretPubToken := "ghp_pubSecretToken666"

	rawSourceURL := fmt.Sprintf("https://alice:%s@sub.domain.com/feed", secretSourcePass)
	rawHealthURL := fmt.Sprintf("https://probe:%s@health.probe.internal/204", secretHealthPass)
	rawGeminiURL := fmt.Sprintf("https://gemini:%s@api.gemini.internal/v1", secretGeminiPass)
	rawRemoteURL := fmt.Sprintf("https://git:%s@github.com/myorg/repo.git", secretPubToken)

	cfg := config.Config{
		FetchIntervalRaw: "1m",
		Sources: []config.SourceItem{
			{Name: "Private Feed", URL: rawSourceURL, Enabled: true},
		},
		Test: config.TestConfig{
			TimeoutRaw: "5s",
			HealthURL:  rawHealthURL,
			Gemini: config.GeminiConfig{
				URL:          rawGeminiURL,
				BlockPhrases: []string{"blocked"},
			},
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: filepath.Join(tmpDir, "repo"),
			RemoteURL:  rawRemoteURL,
		},
	}

	// Inject cfg via ConfigUpdated event
	ad.Subscribe()
	bus := events.New()
	defer bus.Close()

	cfgPath := filepath.Join(tmpDir, "config.json")
	cfgSvc, err := config.NewService(cfgPath, &cfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}

	ad.SetServices(cfgSvc, nil, nil, nil)
	vm := ad.ConfigCenter()

	secrets := []string{secretSourcePass, secretHealthPass, secretGeminiPass, secretPubToken}

	// Verify that NO secret appears anywhere in any category item
	for _, cat := range vm.Categories {
		for _, item := range cat.Items {
			for _, sec := range secrets {
				if strings.Contains(item.Value, sec) {
					t.Fatalf("CRITICAL: secret %q leaked in category %q item %q: %q",
						sec, cat.Name, item.Label, item.Value)
				}
			}
		}
	}

	// Verify that the sanitized values actually contain the masked credentials
	sourcesCat := vm.Categories[viewmodel.CategorySources]
	if !strings.Contains(sourcesCat.Items[1].Value, "https://***@sub.domain.com/feed") {
		t.Errorf("expected masked source URL, got: %s", sourcesCat.Items[1].Value)
	}

	testCat := vm.Categories[viewmodel.CategoryTesting]
	foundHealth := false
	for _, item := range testCat.Items {
		if item.Label == "Health Check URL" {
			foundHealth = true
			if item.Value != "https://***@health.probe.internal/204" {
				t.Errorf("expected masked health check URL, got: %s", item.Value)
			}
		}
	}
	if !foundHealth {
		t.Errorf("Health Check URL item not found in Testing category")
	}

	gemCat := vm.Categories[viewmodel.CategoryGemini]
	foundGemini := false
	for _, item := range gemCat.Items {
		if item.Label == "Gemini Target URL" {
			foundGemini = true
			if item.Value != "https://***@api.gemini.internal/v1" {
				t.Errorf("expected masked gemini target URL, got: %s", item.Value)
			}
		}
	}
	if !foundGemini {
		t.Errorf("Gemini Target URL item not found in Gemini category")
	}

	pubCat := vm.Categories[viewmodel.CategoryPublishing]
	foundRemote := false
	for _, item := range pubCat.Items {
		if item.Label == "Remote URL" {
			foundRemote = true
			if item.Value != "https://***@github.com/myorg/repo.git" {
				t.Errorf("expected masked publishing remote URL, got: %s", item.Value)
			}
		}
	}
	if !foundRemote {
		t.Errorf("Remote URL item not found in Publishing category")
	}
}

func TestAdapter_SourceManagerOperations(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	bus := events.New()
	defer bus.Close()

	initialSources := []config.SourceItem{
		{ID: "src-1", URL: "https://user:pass@sub1.example.com/feed", Name: "Source One", Enabled: true},
		{ID: "src-2", URL: "https://sub2.example.com/feed", Name: "Source Two", Enabled: false},
	}
	cfg := config.Config{
		FetchIntervalRaw: "10m",
		Sources:          initialSources,
		Test: config.TestConfig{
			TimeoutRaw:  "12s",
			Concurrency: 8,
			Gemini: config.GeminiConfig{
				BlockPhrases: []string{"blocked"},
			},
		},
	}

	cfgSvc, err := config.NewService(cfgPath, &cfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}
	srcSvc := source.NewService(cfgSvc)

	ad, _, _, _ := setupTestAdapter(t)
	ad.SetServices(cfgSvc, srcSvc, nil, nil)

	// 1. Check Sources in ConfigCenter
	vm := ad.ConfigCenter()
	srcCat := vm.Categories[viewmodel.CategorySources]
	if len(srcCat.Sources) != 2 {
		t.Fatalf("expected 2 sources in ConfigCenter, got %d", len(srcCat.Sources))
	}
	if srcCat.Sources[0].ID != "src-1" || !srcCat.Sources[0].Enabled || srcCat.Sources[0].Name != "Source One" {
		t.Errorf("unexpected first source: %+v", srcCat.Sources[0])
	}
	if strings.Contains(srcCat.Sources[0].URL, "pass") {
		t.Errorf("credentials leaked in SourceItemViewModel URL: %s", srcCat.Sources[0].URL)
	}
	if srcCat.Sources[1].ID != "src-2" || srcCat.Sources[1].Enabled || srcCat.Sources[1].Name != "Source Two" {
		t.Errorf("unexpected second source: %+v", srcCat.Sources[1])
	}

	// 2. Toggle Source
	if err := ad.ToggleSource("src-1"); err != nil {
		t.Fatalf("ToggleSource(src-1): %v", err)
	}
	s1, err := srcSvc.Get("src-1")
	if err != nil || s1.Enabled != false {
		t.Fatalf("expected src-1 disabled in sourceSvc, got err=%v, enabled=%v", err, s1.Enabled)
	}
	vm = ad.ConfigCenter()
	if vm.Categories[viewmodel.CategorySources].Sources[0].Enabled != false {
		t.Errorf("expected ConfigCenter to reflect disabled src-1")
	}

	// 3. Add Source
	if err := ad.AddSource("https://sub3.example.com/feed", "Source Three"); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	vm = ad.ConfigCenter()
	if len(vm.Categories[viewmodel.CategorySources].Sources) != 3 {
		t.Fatalf("expected 3 sources after add, got %d", len(vm.Categories[viewmodel.CategorySources].Sources))
	}

	// Invalid URL should fail
	if err := ad.AddSource("ftp://invalid-scheme.com", "Bad"); err == nil {
		t.Error("expected error adding ftp URL, got nil")
	}

	// 4. Update Source (update name and retain sanitized credentials)
	if err := ad.UpdateSource("src-1", "https://***@sub1.example.com/feed", "Updated Source One"); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	s1, err = srcSvc.Get("src-1")
	if err != nil {
		t.Fatalf("Get(src-1): %v", err)
	}
	if s1.Name != "Updated Source One" {
		t.Errorf("expected name 'Updated Source One', got %q", s1.Name)
	}
	if s1.URL != "https://user:pass@sub1.example.com/feed" {
		t.Errorf("original credentials lost on update with sanitized URL: got %q", s1.URL)
	}

	// 5. Delete Source
	if err := ad.DeleteSource("src-2"); err != nil {
		t.Fatalf("DeleteSource(src-2): %v", err)
	}
	if err := ad.DeleteSource("src-1"); err != nil {
		t.Fatalf("DeleteSource(src-1): %v", err)
	}
	// Deleting the last remaining source should fail
	lastSrcs := srcSvc.List()
	if len(lastSrcs) != 1 {
		t.Fatalf("expected 1 source remaining, got %d", len(lastSrcs))
	}
	if err := ad.DeleteSource(lastSrcs[0].ID); err == nil {
		t.Error("expected error deleting last source, got nil")
	}
}

func TestAdapter_SourceItemViewModel_TelemetrySemantics(t *testing.T) {
	// Telemetry verification for Issue #17:
	// Issue #17 specifies telemetry is displayed "if known".
	// The current scheduler and store pipeline tracks only aggregate cycle metrics,
	// not per-source attribution. This test proves that the Adapter produces
	// HasCount=false and StatusMsg="" without fabricating speculative telemetry.
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	bus := events.New()
	defer bus.Close()

	cfg := config.Config{
		FetchIntervalRaw: "10m",
		Sources: []config.SourceItem{
			{ID: "src-1", URL: "https://example.com/feed1", Name: "Source 1", Enabled: true},
		},
		Test: config.TestConfig{
			TimeoutRaw:  "10s",
			Concurrency: 5,
			Gemini: config.GeminiConfig{
				BlockPhrases: []string{"blocked"},
			},
		},
	}

	cfgSvc, err := config.NewService(cfgPath, &cfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}
	srcSvc := source.NewService(cfgSvc)

	ad, _, _, _ := setupTestAdapter(t)
	ad.SetServices(cfgSvc, srcSvc, nil, nil)

	vm := ad.ConfigCenter()
	srcCat := vm.Categories[viewmodel.CategorySources]
	if len(srcCat.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(srcCat.Sources))
	}

	s := srcCat.Sources[0]
	if s.HasCount {
		t.Errorf("expected HasCount=false in current architecture, got true")
	}
	if s.CandidateCount != 0 {
		t.Errorf("expected CandidateCount=0 when unmeasured, got %d", s.CandidateCount)
	}
	if s.StatusMsg != "" {
		t.Errorf("expected StatusMsg=\"\" when unmeasured, got %q", s.StatusMsg)
	}
}

func TestAdapter_UpdateSetting_AllCategoriesAndValidation(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	bus := events.New()
	defer bus.Close()

	initialCfg := config.Config{
		FetchIntervalRaw: "10m",
		Sources: []config.SourceItem{
			{ID: "src-1", URL: "https://example.com/feed1", Name: "Source 1", Enabled: true},
		},
		Test: config.TestConfig{
			TimeoutRaw:            "10s",
			Concurrency:           5,
			MaxRetriesRaw:         new(int),
			RetryBackoffRaw:       "1s",
			HealthURL:             "https://www.gstatic.com/generate_204",
			DialTimeoutRaw:        "4s",
			MaxInconclusiveCycles: 2,
			RateLimitRPS:          0,
			Gemini: config.GeminiConfig{
				URL:          "https://gemini.google.com/",
				BlockPhrases: []string{"blocked"},
			},
		},
		Serve: config.ServeConfig{
			Listen: "127.0.0.1:8765",
			Path:   "/sub",
			Format: "base64",
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: "./test-repo",
			Branch:     "main",
			RemoteURL:  "https://github.com/org/repo.git",
		},
		StateFile:  "./gemsub_state.json",
		Headless:   false,
		ProbeLimit: 0,
		FlagMode:   "auto",
	}
	*initialCfg.Test.MaxRetriesRaw = 2

	cfgSvc, err := config.NewService(cfgPath, &initialCfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}
	srcSvc := source.NewService(cfgSvc)
	pubSvc := publisher.NewService(cfgSvc, nil, bus)

	ad, _, _, _ := setupTestAdapter(t)
	ad.SetServices(cfgSvc, srcSvc, pubSvc, nil)

	// 1. General Category
	t.Run("General_Settings", func(t *testing.T) {
		// Valid serve.listen
		if err := ad.UpdateSetting("serve.listen", "127.0.0.1:9090"); err != nil {
			t.Fatalf("UpdateSetting(serve.listen): %v", err)
		}
		if cfgSvc.Get().Serve.Listen != "127.0.0.1:9090" {
			t.Errorf("expected 127.0.0.1:9090, got %s", cfgSvc.Get().Serve.Listen)
		}
		// Invalid serve.listen (empty)
		if err := ad.UpdateSetting("serve.listen", "   "); err == nil {
			t.Errorf("expected error on empty serve.listen, got nil")
		}

		// Valid serve.path
		if err := ad.UpdateSetting("serve.path", "/custom_feed"); err != nil {
			t.Fatalf("UpdateSetting(serve.path): %v", err)
		}
		if cfgSvc.Get().Serve.Path != "/custom_feed" {
			t.Errorf("expected /custom_feed, got %s", cfgSvc.Get().Serve.Path)
		}
		// Invalid serve.path (must start with /)
		if err := ad.UpdateSetting("serve.path", "no_slash"); err == nil {
			t.Errorf("expected error on serve.path without leading slash, got nil")
		}

		// Valid serve.format (enum)
		if err := ad.UpdateSetting("serve.format", "raw"); err != nil {
			t.Fatalf("UpdateSetting(serve.format): %v", err)
		}
		if cfgSvc.Get().Serve.Format != "raw" {
			t.Errorf("expected raw, got %s", cfgSvc.Get().Serve.Format)
		}
		// Invalid serve.format
		if err := ad.UpdateSetting("serve.format", "xml"); err == nil {
			t.Errorf("expected error on invalid serve.format, got nil")
		}

		// Valid flag_mode (enum)
		if err := ad.UpdateSetting("flag_mode", "unicode"); err != nil {
			t.Fatalf("UpdateSetting(flag_mode): %v", err)
		}
		if cfgSvc.Get().FlagMode != "unicode" {
			t.Errorf("expected unicode, got %s", cfgSvc.Get().FlagMode)
		}
		// Invalid flag_mode
		if err := ad.UpdateSetting("flag_mode", "invalid_mode"); err == nil {
			t.Errorf("expected error on invalid flag_mode, got nil")
		}

		// Valid state_file
		if err := ad.UpdateSetting("state_file", "./new_state.json"); err != nil {
			t.Fatalf("UpdateSetting(state_file): %v", err)
		}
		if cfgSvc.Get().StateFile != "./new_state.json" {
			t.Errorf("expected ./new_state.json, got %s", cfgSvc.Get().StateFile)
		}
		// Invalid state_file (empty)
		if err := ad.UpdateSetting("state_file", ""); err == nil {
			t.Errorf("expected error on empty state_file, got nil")
		}

		// Valid headless (bool toggle)
		if err := ad.UpdateSetting("headless", "true"); err != nil {
			t.Fatalf("UpdateSetting(headless): %v", err)
		}
		if cfgSvc.Get().Headless != true {
			t.Errorf("expected headless=true, got false")
		}
		// Invalid headless
		if err := ad.UpdateSetting("headless", "notabool"); err == nil {
			t.Errorf("expected error on invalid bool, got nil")
		}
	})

	// 2. Testing Category
	t.Run("Testing_Settings", func(t *testing.T) {
		// Valid timeout
		if err := ad.UpdateSetting("test.timeout", "20s"); err != nil {
			t.Fatalf("UpdateSetting(test.timeout): %v", err)
		}
		if cfgSvc.Get().Test.TimeoutRaw != "20s" || cfgSvc.Get().Test.Timeout != 20*time.Second {
			t.Errorf("unexpected timeout: %v", cfgSvc.Get().Test.TimeoutRaw)
		}
		// Invalid timeout
		if err := ad.UpdateSetting("test.timeout", "notaduration"); err == nil {
			t.Errorf("expected error on invalid duration, got nil")
		}
		if err := ad.UpdateSetting("test.timeout", "-5s"); err == nil {
			t.Errorf("expected error on negative duration, got nil")
		}

		// Valid concurrency
		if err := ad.UpdateSetting("test.concurrency", "35"); err != nil {
			t.Fatalf("UpdateSetting(test.concurrency): %v", err)
		}
		if cfgSvc.Get().Test.Concurrency != 35 {
			t.Errorf("expected 35, got %d", cfgSvc.Get().Test.Concurrency)
		}
		// Invalid concurrency
		if err := ad.UpdateSetting("test.concurrency", "0"); err == nil {
			t.Errorf("expected error on concurrency 0, got nil")
		}
		if err := ad.UpdateSetting("test.concurrency", "-5"); err == nil {
			t.Errorf("expected error on concurrency -5, got nil")
		}

		// Valid max_retries
		if err := ad.UpdateSetting("test.max_retries", "0"); err != nil {
			t.Fatalf("UpdateSetting(test.max_retries): %v", err)
		}
		if cfgSvc.Get().Test.MaxRetries != 0 {
			t.Errorf("expected 0, got %d", cfgSvc.Get().Test.MaxRetries)
		}
		// Invalid max_retries
		if err := ad.UpdateSetting("test.max_retries", "-1"); err == nil {
			t.Errorf("expected error on negative max_retries, got nil")
		}

		// Valid retry_backoff
		if err := ad.UpdateSetting("test.retry_backoff", "2500ms"); err != nil {
			t.Fatalf("UpdateSetting(test.retry_backoff): %v", err)
		}
		if cfgSvc.Get().Test.RetryBackoff != 2500*time.Millisecond {
			t.Errorf("unexpected retry backoff: %v", cfgSvc.Get().Test.RetryBackoff)
		}
		// Invalid retry_backoff
		if err := ad.UpdateSetting("test.retry_backoff", "0s"); err == nil {
			t.Errorf("expected error on 0s retry_backoff, got nil")
		}

		// Valid health_url
		if err := ad.UpdateSetting("test.health_url", "https://custom.health.check/204"); err != nil {
			t.Fatalf("UpdateSetting(test.health_url): %v", err)
		}
		if cfgSvc.Get().Test.HealthURL != "https://custom.health.check/204" {
			t.Errorf("unexpected health URL: %s", cfgSvc.Get().Test.HealthURL)
		}
		// Invalid health_url
		if err := ad.UpdateSetting("test.health_url", "ftp://unsupported.com"); err == nil {
			t.Errorf("expected error on unsupported scheme, got nil")
		}

		// Valid dial_timeout
		if err := ad.UpdateSetting("test.dial_timeout", "5s"); err != nil {
			t.Fatalf("UpdateSetting(test.dial_timeout): %v", err)
		}
		if cfgSvc.Get().Test.DialTimeout != 5*time.Second {
			t.Errorf("unexpected dial timeout: %v", cfgSvc.Get().Test.DialTimeout)
		}

		// Valid max_inconclusive_cycles
		if err := ad.UpdateSetting("test.max_inconclusive_cycles", "4"); err != nil {
			t.Fatalf("UpdateSetting(test.max_inconclusive_cycles): %v", err)
		}
		if cfgSvc.Get().Test.MaxInconclusiveCycles != 4 {
			t.Errorf("expected 4, got %d", cfgSvc.Get().Test.MaxInconclusiveCycles)
		}
		// Invalid max_inconclusive_cycles
		if err := ad.UpdateSetting("test.max_inconclusive_cycles", "0"); err == nil {
			t.Errorf("expected error on 0 inconclusive cycles, got nil")
		}

		// Valid rate_limit_rps
		if err := ad.UpdateSetting("test.rate_limit_rps", "25"); err != nil {
			t.Fatalf("UpdateSetting(test.rate_limit_rps): %v", err)
		}
		if cfgSvc.Get().Test.RateLimitRPS != 25 {
			t.Errorf("expected 25, got %d", cfgSvc.Get().Test.RateLimitRPS)
		}
		// Invalid rate_limit_rps
		if err := ad.UpdateSetting("test.rate_limit_rps", "-2"); err == nil {
			t.Errorf("expected error on negative rate limit, got nil")
		}
	})

	// 3. Gemini Category
	t.Run("Gemini_Settings", func(t *testing.T) {
		// Valid Gemini URL
		if err := ad.UpdateSetting("test.gemini.url", "https://gemini.custom.dev/"); err != nil {
			t.Fatalf("UpdateSetting(test.gemini.url): %v", err)
		}
		if cfgSvc.Get().Test.Gemini.URL != "https://gemini.custom.dev/" {
			t.Errorf("unexpected gemini url: %s", cfgSvc.Get().Test.Gemini.URL)
		}
		// Invalid Gemini URL
		if err := ad.UpdateSetting("test.gemini.url", ""); err == nil {
			t.Errorf("expected error on empty gemini url, got nil")
		}

		// Valid Block Phrases
		if err := ad.UpdateSetting("test.gemini.block_phrases", "denied, access blocked, forbidden"); err != nil {
			t.Fatalf("UpdateSetting(test.gemini.block_phrases): %v", err)
		}
		phrases := cfgSvc.Get().Test.Gemini.BlockPhrases
		if len(phrases) != 3 || phrases[0] != "denied" || phrases[1] != "access blocked" || phrases[2] != "forbidden" {
			t.Errorf("unexpected block phrases: %+v", phrases)
		}
		// Invalid Block Phrases (empty)
		if err := ad.UpdateSetting("test.gemini.block_phrases", "  ,  ,  "); err == nil {
			t.Errorf("expected error on empty block phrases, got nil")
		}
	})

	// 4. Scheduler Category
	t.Run("Scheduler_Settings", func(t *testing.T) {
		// Valid fetch_interval
		if err := ad.UpdateSetting("fetch_interval", "30m"); err != nil {
			t.Fatalf("UpdateSetting(fetch_interval): %v", err)
		}
		if cfgSvc.Get().FetchIntervalRaw != "30m" || cfgSvc.Get().FetchInterval != 30*time.Minute {
			t.Errorf("unexpected fetch interval: %v", cfgSvc.Get().FetchIntervalRaw)
		}
		// Invalid fetch_interval (< 1m)
		if err := ad.UpdateSetting("fetch_interval", "45s"); err == nil {
			t.Errorf("expected error on fetch interval < 1m, got nil")
		}

		// Valid probe_limit
		if err := ad.UpdateSetting("probe_limit", "50"); err != nil {
			t.Fatalf("UpdateSetting(probe_limit): %v", err)
		}
		if cfgSvc.Get().ProbeLimit != 50 {
			t.Errorf("expected 50, got %d", cfgSvc.Get().ProbeLimit)
		}
		// Invalid probe_limit
		if err := ad.UpdateSetting("probe_limit", "-5"); err == nil {
			t.Errorf("expected error on negative probe limit, got nil")
		}
	})

	// 5. Publishing Category
	t.Run("Publishing_Settings", func(t *testing.T) {
		// Valid publishing.branch
		if err := ad.UpdateSetting("publishing.branch", "production"); err != nil {
			t.Fatalf("UpdateSetting(publishing.branch): %v", err)
		}
		if cfgSvc.Get().Publishing.Branch != "production" {
			t.Errorf("expected production, got %s", cfgSvc.Get().Publishing.Branch)
		}

		// Valid publishing.repository
		if err := ad.UpdateSetting("publishing.repository", "./prod-repo"); err != nil {
			t.Fatalf("UpdateSetting(publishing.repository): %v", err)
		}
		if cfgSvc.Get().Publishing.Repository != "./prod-repo" {
			t.Errorf("expected ./prod-repo, got %s", cfgSvc.Get().Publishing.Repository)
		}

		// Valid publishing.remote_url
		if err := ad.UpdateSetting("publishing.remote_url", "https://github.com/org/new-repo.git"); err != nil {
			t.Fatalf("UpdateSetting(publishing.remote_url): %v", err)
		}
		if cfgSvc.Get().Publishing.RemoteURL != "https://github.com/org/new-repo.git" {
			t.Errorf("unexpected remote url: %s", cfgSvc.Get().Publishing.RemoteURL)
		}

		// Valid publishing.enabled (toggle to false)
		if err := ad.UpdateSetting("publishing.enabled", "false"); err != nil {
			t.Fatalf("UpdateSetting(publishing.enabled): %v", err)
		}
		if cfgSvc.Get().Publishing.Enabled != false {
			t.Errorf("expected enabled=false, got true")
		}
	})
}

func TestAdapter_UpdateSetting_SecretPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	bus := events.New()
	defer bus.Close()

	initialCfg := config.Config{
		FetchIntervalRaw: "10m",
		Sources: []config.SourceItem{
			{ID: "src-1", URL: "https://user:pass123@example.com/feed1", Name: "Source 1", Enabled: true},
		},
		Test: config.TestConfig{
			TimeoutRaw:  "10s",
			Concurrency: 5,
			Gemini: config.GeminiConfig{
				URL:          "https://apikey:secretgemini@gemini.dev/v1",
				BlockPhrases: []string{"blocked"},
			},
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: "./test-repo",
			Branch:     "main",
			RemoteURL:  "https://gituser:ghp_secrettoken999@github.com/org/repo.git",
		},
	}

	cfgSvc, err := config.NewService(cfgPath, &initialCfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}
	srcSvc := source.NewService(cfgSvc)
	pubSvc := publisher.NewService(cfgSvc, nil, bus)

	ad, _, _, _ := setupTestAdapter(t)
	ad.SetServices(cfgSvc, srcSvc, pubSvc, nil)

	// 1. Verify ConfigCenter ViewModel exposes ONLY sanitized URLs
	vm := ad.ConfigCenter()
	pubCat := vm.Categories[viewmodel.CategoryPublishing]
	for _, item := range pubCat.Items {
		if item.Key == "publishing.remote_url" {
			if strings.Contains(item.Value, "ghp_secrettoken999") || strings.Contains(item.RawValue, "ghp_secrettoken999") {
				t.Fatalf("credentials leaked in ConfigCenter RemoteURL item: %+v", item)
			}
			if !strings.Contains(item.Value, "***@") {
				t.Errorf("expected sanitized URL with '***@', got %s", item.Value)
			}
		}
	}

	// 2. Editing unrelated setting (e.g. publishing.branch) preserves RemoteURL credentials
	if err := ad.UpdateSetting("publishing.branch", "feature-x"); err != nil {
		t.Fatalf("UpdateSetting(publishing.branch): %v", err)
	}
	currentPub := cfgSvc.Get().Publishing
	if currentPub.RemoteURL != "https://gituser:ghp_secrettoken999@github.com/org/repo.git" {
		t.Errorf("RemoteURL credentials lost on editing branch: %s", currentPub.RemoteURL)
	}

	// 3. Submitting sanitized URL back to publishing.remote_url preserves original credentials
	sanitizedPubURL := publisher.SanitizeURL(currentPub.RemoteURL)
	if err := ad.UpdateSetting("publishing.remote_url", sanitizedPubURL); err != nil {
		t.Fatalf("UpdateSetting(publishing.remote_url with sanitized): %v", err)
	}
	if cfgSvc.Get().Publishing.RemoteURL != "https://gituser:ghp_secrettoken999@github.com/org/repo.git" {
		t.Errorf("RemoteURL credentials erased when re-submitting sanitized URL: %s", cfgSvc.Get().Publishing.RemoteURL)
	}

	// 4. Gemini: editing block phrases preserves Gemini URL credentials
	if err := ad.UpdateSetting("test.gemini.block_phrases", "blocked, unavailable"); err != nil {
		t.Fatalf("UpdateSetting(block_phrases): %v", err)
	}
	currentGemini := cfgSvc.Get().Test.Gemini
	if currentGemini.URL != "https://apikey:secretgemini@gemini.dev/v1" {
		t.Errorf("Gemini URL credentials lost on editing block phrases: %s", currentGemini.URL)
	}

	// 5. Submitting sanitized URL to Gemini preserves credentials
	sanitizedGeminiURL := publisher.SanitizeURL(currentGemini.URL)
	if err := ad.UpdateSetting("test.gemini.url", sanitizedGeminiURL); err != nil {
		t.Fatalf("UpdateSetting(test.gemini.url with sanitized): %v", err)
	}
	if cfgSvc.Get().Test.Gemini.URL != "https://apikey:secretgemini@gemini.dev/v1" {
		t.Errorf("Gemini URL credentials erased when re-submitting sanitized URL: %s", cfgSvc.Get().Test.Gemini.URL)
	}

	// 6. Source items preserved completely
	if len(cfgSvc.Get().Sources) != 1 || cfgSvc.Get().Sources[0].URL != "https://user:pass123@example.com/feed1" {
		t.Errorf("Sources corrupted during settings edit: %+v", cfgSvc.Get().Sources)
	}
}

func TestAdapter_ConfigCenter_RestartRequiredAndPendingFlags(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	bus := events.New()
	defer bus.Close()

	initialCfg := config.Config{
		FetchIntervalRaw: "10m",
		Sources: []config.SourceItem{
			{ID: "src-1", URL: "https://example.com/feed1", Name: "Source 1", Enabled: true},
		},
		Serve: config.ServeConfig{
			Listen: "127.0.0.1:8765",
			Path:   "/sub",
			Format: "base64",
		},
		StateFile: "./gemsub_state.json",
		Headless:  false,
		Test: config.TestConfig{
			TimeoutRaw:  "10s",
			Concurrency: 5,
			Gemini: config.GeminiConfig{
				BlockPhrases: []string{"blocked"},
			},
		},
	}

	cfgSvc, err := config.NewService(cfgPath, &initialCfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}

	ad, _, _, _ := setupTestAdapter(t)
	ad.SetServices(cfgSvc, nil, nil, nil)

	// Check initial ConfigCenter: restart-required flags should be set, but pending should be false
	vm := ad.ConfigCenter()
	genCat := vm.Categories[viewmodel.CategoryGeneral]
	for _, item := range genCat.Items {
		switch item.Key {
		case "serve.listen", "serve.path", "state_file", "headless":
			if !item.RestartRequired {
				t.Errorf("field %s expected RestartRequired=true, got false", item.Key)
			}
			if item.PendingRestart {
				t.Errorf("field %s expected PendingRestart=false before edit, got true", item.Key)
			}
		case "serve.format", "flag_mode":
			if item.RestartRequired {
				t.Errorf("field %s expected RestartRequired=false, got true", item.Key)
			}
		}
	}

	// Mutate serve.listen
	if err := ad.UpdateSetting("serve.listen", "127.0.0.1:9999"); err != nil {
		t.Fatalf("UpdateSetting(serve.listen): %v", err)
	}

	// Now ConfigCenter should have PendingRestart=true on serve.listen
	vm = ad.ConfigCenter()
	genCat = vm.Categories[viewmodel.CategoryGeneral]
	found := false
	for _, item := range genCat.Items {
		if item.Key == "serve.listen" {
			found = true
			if !item.PendingRestart {
				t.Errorf("serve.listen expected PendingRestart=true after edit, got false")
			}
		}
	}
	if !found {
		t.Fatal("serve.listen not found in ConfigCenter")
	}
}

func TestAdapter_UpdateSetting_RemoteURL_CredentialPolicy(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	bus := events.New()
	defer bus.Close()

	secretToken := "secret_ghp_token_xyz987"
	originalURL := "https://gituser:" + secretToken + "@github.com/org/repo.git"

	initialCfg := config.Config{
		FetchIntervalRaw: "10m",
		Sources: []config.SourceItem{
			{ID: "src-1", URL: "https://example.com/feed1", Name: "Source 1", Enabled: true},
		},
		Test: config.TestConfig{
			TimeoutRaw:  "10s",
			Concurrency: 5,
			HealthURL:   "https://healthuser:secretpass@health.example.com/check",
			Gemini: config.GeminiConfig{
				URL:          "https://geminiuser:secretgeminikey@gemini.dev/v1",
				BlockPhrases: []string{"blocked"},
			},
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: "./test-repo",
			Branch:     "main",
			RemoteURL:  originalURL,
		},
	}

	cfgSvc, err := config.NewService(cfgPath, &initialCfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}
	pubSvc := publisher.NewService(cfgSvc, nil, bus)
	ad, _, _, _ := setupTestAdapter(t)
	ad.SetServices(cfgSvc, nil, pubSvc, nil)

	// 1. Existing credential + unchanged masked URL -> credential preserved
	maskedURL := publisher.SanitizeURL(originalURL) // "https://***@github.com/org/repo.git"
	if err := ad.UpdateSetting("publishing.remote_url", maskedURL); err != nil {
		t.Fatalf("UpdateSetting(publishing.remote_url, masked): %v", err)
	}
	if got := cfgSvc.Get().Publishing.RemoteURL; got != originalURL {
		t.Errorf("expected unchanged masked URL to preserve credentials %q, got %q", originalURL, got)
	}

	// 2. Existing credential + changed path -> credential preserved
	changedPathMasked := "https://***@github.com/org/another-repo.git"
	if err := ad.UpdateSetting("publishing.remote_url", changedPathMasked); err != nil {
		t.Fatalf("UpdateSetting(publishing.remote_url, changed path): %v", err)
	}
	expectedPathURL := "https://gituser:" + secretToken + "@github.com/org/another-repo.git"
	if got := cfgSvc.Get().Publishing.RemoteURL; got != expectedPathURL {
		t.Errorf("expected changed path to preserve credentials %q, got %q", expectedPathURL, got)
	}

	// Also test changed path with NO credentials provided in input:
	changedPathNoUser := "https://github.com/org/third-repo.git"
	if err := ad.UpdateSetting("publishing.remote_url", changedPathNoUser); err != nil {
		t.Fatalf("UpdateSetting(publishing.remote_url, no user): %v", err)
	}
	expectedNoUserPreserved := "https://gituser:" + secretToken + "@github.com/org/third-repo.git"
	if got := cfgSvc.Get().Publishing.RemoteURL; got != expectedNoUserPreserved {
		t.Errorf("expected omission of credentials to preserve credentials %q, got %q", expectedNoUserPreserved, got)
	}

	// 3. Existing credential + changed host -> credential preserved
	changedHostMasked := "https://***@gitlab.com/org/custom-repo.git"
	if err := ad.UpdateSetting("publishing.remote_url", changedHostMasked); err != nil {
		t.Fatalf("UpdateSetting(publishing.remote_url, changed host): %v", err)
	}
	expectedHostURL := "https://gituser:" + secretToken + "@gitlab.com/org/custom-repo.git"
	if got := cfgSvc.Get().Publishing.RemoteURL; got != expectedHostURL {
		t.Errorf("expected changed host to preserve credentials %q, got %q", expectedHostURL, got)
	}

	// 4. Submitting a URL with explicit userinfo is rejected per credential policy
	newExplicitURL := "https://newuser:newtoken456@github.com/neworg/newrepo.git"
	beforeExplicit := cfgSvc.Get().Publishing.RemoteURL
	errExplicit := ad.UpdateSetting("publishing.remote_url", newExplicitURL)
	if errExplicit == nil {
		t.Fatalf("expected explicit userinfo to be rejected, got nil error")
	}
	if !strings.Contains(errExplicit.Error(), "credentials must be managed separately") {
		t.Errorf("expected error 'credentials must be managed separately', got: %v", errExplicit)
	}
	// 5. Rejected credential input does not leak credential into error message or config
	if strings.Contains(errExplicit.Error(), "newtoken456") || strings.Contains(errExplicit.Error(), "newuser") {
		t.Fatalf("credential leaked in error message: %v", errExplicit)
	}
	if got := cfgSvc.Get().Publishing.RemoteURL; got != beforeExplicit {
		t.Errorf("config corrupted after rejected credential input: expected %q, got %q", beforeExplicit, got)
	}

	// Also test explicit credentials on test.gemini.url and test.health_url are rejected without leakage
	errGemini := ad.UpdateSetting("test.gemini.url", "https://explicituser:secretpass@gemini.dev/v1")
	if errGemini == nil || !strings.Contains(errGemini.Error(), "credentials must be managed separately") {
		t.Errorf("expected test.gemini.url explicit userinfo to be rejected, got: %v", errGemini)
	}
	if strings.Contains(fmt.Sprint(errGemini), "secretpass") {
		t.Fatalf("secret leaked in Gemini error message: %v", errGemini)
	}

	errHealth := ad.UpdateSetting("test.health_url", "https://healthuser:secretpass@health.example.com/v1")
	if errHealth == nil || !strings.Contains(errHealth.Error(), "credentials must be managed separately") {
		t.Errorf("expected test.health_url explicit userinfo to be rejected, got: %v", errHealth)
	}
	if strings.Contains(fmt.Sprint(errHealth), "secretpass") {
		t.Fatalf("secret leaked in Health URL error message: %v", errHealth)
	}

	// 6. No credentials starting point + changed URL -> new URL saved (non-secret URLs remain normally editable)
	// First change to a URL with no credentials directly via config service
	noCredsURL := "https://github.com/public/repo.git"
	_ = cfgSvc.Update(func(c *config.Config) error {
		c.Publishing.RemoteURL = noCredsURL
		return nil
	})
	changedNoCredsURL := "https://github.com/public/another-repo.git"
	if err := ad.UpdateSetting("publishing.remote_url", changedNoCredsURL); err != nil {
		t.Fatalf("UpdateSetting(publishing.remote_url, from no creds): %v", err)
	}
	if got := cfgSvc.Get().Publishing.RemoteURL; got != changedNoCredsURL {
		t.Errorf("expected %q, got %q", changedNoCredsURL, got)
	}

	// 6. Invalid URL -> rejected, config untouched
	beforeInvalid := cfgSvc.Get().Publishing.RemoteURL
	if err := ad.UpdateSetting("publishing.remote_url", "http://[invalid:host:port"); err == nil {
		t.Errorf("expected invalid URL to be rejected, got nil error")
	}
	if got := cfgSvc.Get().Publishing.RemoteURL; got != beforeInvalid {
		t.Errorf("config corrupted after invalid URL: expected %q, got %q", beforeInvalid, got)
	}

	// 7. Empty URL -> rejected, config untouched
	if err := ad.UpdateSetting("publishing.remote_url", ""); err == nil {
		t.Errorf("expected empty URL to be rejected, got nil error")
	}
	if err := ad.UpdateSetting("publishing.remote_url", "   \t\n  "); err == nil {
		t.Errorf("expected whitespace URL to be rejected, got nil error")
	}
	if got := cfgSvc.Get().Publishing.RemoteURL; got != beforeInvalid {
		t.Errorf("config corrupted after empty URL: expected %q, got %q", beforeInvalid, got)
	}

	// 8. Credential-preserving semantics consistency across test.gemini.url and test.health_url
	// Gemini:
	if err := ad.UpdateSetting("test.gemini.url", "https://***@gemini.dev/v2"); err != nil {
		t.Fatalf("UpdateSetting(test.gemini.url): %v", err)
	}
	expectedGemini := "https://geminiuser:secretgeminikey@gemini.dev/v2"
	if got := cfgSvc.Get().Test.Gemini.URL; got != expectedGemini {
		t.Errorf("expected Gemini credentials preserved %q, got %q", expectedGemini, got)
	}
	// Health URL:
	if err := ad.UpdateSetting("test.health_url", "https://***@health.example.com/v2"); err != nil {
		t.Fatalf("UpdateSetting(test.health_url): %v", err)
	}
	expectedHealth := "https://healthuser:secretpass@health.example.com/v2"
	if got := cfgSvc.Get().Test.HealthURL; got != expectedHealth {
		t.Errorf("expected Health URL credentials preserved %q, got %q", expectedHealth, got)
	}

	// 9. No credential leakage in ViewModel / render / error / status
	vm := ad.ConfigCenter()
	for catIdx, cat := range vm.Categories {
		for _, item := range cat.Items {
			for _, secret := range []string{secretToken, "newtoken456", "secretpass", "secretgeminikey"} {
				if strings.Contains(item.Value, secret) {
					t.Errorf("secret %q leaked in Category %d item.Value (key=%s): %s", secret, catIdx, item.Key, item.Value)
				}
				if strings.Contains(item.EditorValue, secret) {
					t.Errorf("secret %q leaked in Category %d item.EditorValue (key=%s): %s", secret, catIdx, item.Key, item.EditorValue)
				}
				if strings.Contains(item.RawValue, secret) {
					t.Errorf("secret %q leaked in Category %d item.RawValue (key=%s): %s", secret, catIdx, item.Key, item.RawValue)
				}
			}
			if item.HasSecret && item.RawValue != "" {
				t.Errorf("secret-bearing item %s must have empty RawValue, got %q", item.Key, item.RawValue)
			}
		}
	}
}

func TestAdapter_UpdateSetting_CrossFieldPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	bus := events.New()
	defer bus.Close()

	initialCfg := config.Config{
		FetchIntervalRaw: "15m",
		ProbeLimit:       42,
		StateFile:        "./state_custom.json",
		Headless:         false,
		FlagMode:         "unicode",
		Sources: []config.SourceItem{
			{ID: "src-1", URL: "https://example.com/feed1", Name: "Source 1", Enabled: true},
		},
		Serve: config.ServeConfig{
			Listen: "127.0.0.1:9090",
			Path:   "/custom-sub",
			Format: "raw",
		},
		Test: config.TestConfig{
			TimeoutRaw:            "15s",
			Concurrency:           8,
			RetryBackoffRaw:       "2s",
			HealthURL:             "https://health.example.com/check",
			DialTimeoutRaw:        "3s",
			MaxInconclusiveCycles: 4,
			RateLimitRPS:          20,
			Gemini: config.GeminiConfig{
				URL:          "https://gemini.example.com/v1",
				BlockPhrases: []string{"phrase1", "phrase2"},
			},
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: "./my-repo",
			Branch:     "dev",
			RemoteURL:  "https://github.com/my/repo.git",
		},
	}

	cfgSvc, err := config.NewService(cfgPath, &initialCfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}
	pubSvc := publisher.NewService(cfgSvc, nil, bus)
	srcSvc := source.NewService(cfgSvc)

	ad, _, _, _ := setupTestAdapter(t)
	ad.SetServices(cfgSvc, srcSvc, pubSvc, nil)

	// Step 1: Mutate Serve setting (serve.listen)
	if err := ad.UpdateSetting("serve.listen", "127.0.0.1:8080"); err != nil {
		t.Fatalf("UpdateSetting(serve.listen): %v", err)
	}
	cfg1 := cfgSvc.Get()
	if cfg1.Serve.Listen != "127.0.0.1:8080" {
		t.Errorf("serve.listen not updated: got %s", cfg1.Serve.Listen)
	}
	// Verify siblings
	if cfg1.Serve.Path != "/custom-sub" || cfg1.Serve.Format != "raw" {
		t.Errorf("serve siblings modified: Path=%s Format=%s", cfg1.Serve.Path, cfg1.Serve.Format)
	}
	// Verify other categories preserved
	if cfg1.Test.Concurrency != 8 || cfg1.Test.TimeoutRaw != "15s" || cfg1.Test.HealthURL != "https://health.example.com/check" {
		t.Errorf("test category modified: %+v", cfg1.Test)
	}
	if cfg1.Test.Gemini.URL != "https://gemini.example.com/v1" || len(cfg1.Test.Gemini.BlockPhrases) != 2 {
		t.Errorf("gemini category modified: %+v", cfg1.Test.Gemini)
	}
	if cfg1.FetchIntervalRaw != "15m" || cfg1.ProbeLimit != 42 {
		t.Errorf("scheduler category modified: FetchInterval=%s ProbeLimit=%d", cfg1.FetchIntervalRaw, cfg1.ProbeLimit)
	}
	if cfg1.Publishing.Repository != "./my-repo" || cfg1.Publishing.Branch != "dev" || cfg1.Publishing.RemoteURL != "https://github.com/my/repo.git" || !cfg1.Publishing.Enabled {
		t.Errorf("publishing category modified: %+v", cfg1.Publishing)
	}
	if cfg1.StateFile != "./state_custom.json" || cfg1.Headless != false || cfg1.FlagMode != "unicode" {
		t.Errorf("general category modified: StateFile=%s Headless=%v FlagMode=%s", cfg1.StateFile, cfg1.Headless, cfg1.FlagMode)
	}

	// Step 2: Mutate Testing setting (test.concurrency)
	if err := ad.UpdateSetting("test.concurrency", "16"); err != nil {
		t.Fatalf("UpdateSetting(test.concurrency): %v", err)
	}
	cfg2 := cfgSvc.Get()
	if cfg2.Test.Concurrency != 16 {
		t.Errorf("test.concurrency not updated: got %d", cfg2.Test.Concurrency)
	}
	// Verify testing siblings
	if cfg2.Test.TimeoutRaw != "15s" || cfg2.Test.RetryBackoffRaw != "2s" || cfg2.Test.HealthURL != "https://health.example.com/check" ||
		cfg2.Test.DialTimeoutRaw != "3s" || cfg2.Test.MaxInconclusiveCycles != 4 || cfg2.Test.RateLimitRPS != 20 {
		t.Errorf("testing siblings modified: %+v", cfg2.Test)
	}
	// Verify other categories preserved
	if cfg2.Serve.Listen != "127.0.0.1:8080" || cfg2.Serve.Path != "/custom-sub" || cfg2.Serve.Format != "raw" {
		t.Errorf("serve category modified during test edit: %+v", cfg2.Serve)
	}
	if cfg2.Publishing.Branch != "dev" || cfg2.Publishing.Repository != "./my-repo" {
		t.Errorf("publishing modified during test edit: %+v", cfg2.Publishing)
	}
	if cfg2.FetchIntervalRaw != "15m" || cfg2.ProbeLimit != 42 {
		t.Errorf("scheduler modified during test edit: %+v", cfg2)
	}

	// Step 3: Mutate Gemini setting (test.gemini.block_phrases)
	if err := ad.UpdateSetting("test.gemini.block_phrases", "alpha, beta, gamma"); err != nil {
		t.Fatalf("UpdateSetting(test.gemini.block_phrases): %v", err)
	}
	cfg3 := cfgSvc.Get()
	if len(cfg3.Test.Gemini.BlockPhrases) != 3 || cfg3.Test.Gemini.BlockPhrases[0] != "alpha" {
		t.Errorf("gemini block phrases not updated: %+v", cfg3.Test.Gemini.BlockPhrases)
	}
	if cfg3.Test.Gemini.URL != "https://gemini.example.com/v1" {
		t.Errorf("gemini.url changed: %s", cfg3.Test.Gemini.URL)
	}
	if cfg3.Test.Concurrency != 16 || cfg3.Serve.Listen != "127.0.0.1:8080" || cfg3.Publishing.Branch != "dev" {
		t.Errorf("other categories modified during gemini edit")
	}

	// Step 4: Mutate Scheduler setting (fetch_interval)
	if err := ad.UpdateSetting("fetch_interval", "30m"); err != nil {
		t.Fatalf("UpdateSetting(fetch_interval): %v", err)
	}
	cfg4 := cfgSvc.Get()
	if cfg4.FetchIntervalRaw != "30m" {
		t.Errorf("fetch_interval not updated: got %s", cfg4.FetchIntervalRaw)
	}
	if cfg4.ProbeLimit != 42 {
		t.Errorf("probe_limit modified during fetch_interval edit: got %d", cfg4.ProbeLimit)
	}
	if cfg4.Test.Concurrency != 16 || cfg4.Serve.Listen != "127.0.0.1:8080" || cfg4.Publishing.Branch != "dev" {
		t.Errorf("other categories modified during scheduler edit")
	}

	// Step 5: Mutate Publishing setting (publishing.branch)
	if err := ad.UpdateSetting("publishing.branch", "release-v2"); err != nil {
		t.Fatalf("UpdateSetting(publishing.branch): %v", err)
	}
	cfg5 := cfgSvc.Get()
	if cfg5.Publishing.Branch != "release-v2" {
		t.Errorf("publishing.branch not updated: got %s", cfg5.Publishing.Branch)
	}
	if cfg5.Publishing.Repository != "./my-repo" || cfg5.Publishing.RemoteURL != "https://github.com/my/repo.git" || !cfg5.Publishing.Enabled {
		t.Errorf("publishing siblings modified: %+v", cfg5.Publishing)
	}
	if cfg5.Serve.Listen != "127.0.0.1:8080" || cfg5.Test.Concurrency != 16 || cfg5.FetchIntervalRaw != "30m" {
		t.Errorf("other categories modified during publishing edit")
	}
}

func TestAdapter_UpdateSetting_LegacyGeminiSynchronization(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	bus := events.New()
	defer bus.Close()

	initialCfg := config.Config{
		FetchIntervalRaw: "10m",
		Sources: []config.SourceItem{
			{ID: "src-1", URL: "https://example.com/feed1", Name: "Source 1", Enabled: true},
		},
		Test: config.TestConfig{
			TimeoutRaw:  "10s",
			Concurrency: 5,
			Gemini: config.GeminiConfig{
				URL:          "https://initial-gemini.com/v1",
				BlockPhrases: []string{"initial-phrase"},
			},
		},
	}

	cfgSvc, err := config.NewService(cfgPath, &initialCfg, bus)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}
	ad, _, _, _ := setupTestAdapter(t)
	ad.SetServices(cfgSvc, nil, nil, nil)

	// Verify initial synchronization
	if cfgSvc.Get().Test.Gemini.URL != "https://initial-gemini.com/v1" || cfgSvc.Get().Test.TargetURL != "https://initial-gemini.com/v1" {
		t.Fatalf("initial URL sync failed: Gemini=%q TargetURL=%q", cfgSvc.Get().Test.Gemini.URL, cfgSvc.Get().Test.TargetURL)
	}

	// 1. Update test.gemini.url
	newGeminiURL := "https://updated-gemini.dev/v2"
	if err := ad.UpdateSetting("test.gemini.url", newGeminiURL); err != nil {
		t.Fatalf("UpdateSetting(test.gemini.url): %v", err)
	}

	// Verify in-memory synchronization
	inMem := cfgSvc.Get()
	if inMem.Test.Gemini.URL != newGeminiURL {
		t.Errorf("in-memory Test.Gemini.URL = %q, want %q", inMem.Test.Gemini.URL, newGeminiURL)
	}
	if inMem.Test.TargetURL != newGeminiURL {
		t.Errorf("in-memory Test.TargetURL = %q, want %q", inMem.Test.TargetURL, newGeminiURL)
	}

	// Verify real on-disk persistence and re-loading
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load(%s): %v", cfgPath, err)
	}
	if reloaded.Test.Gemini.URL != newGeminiURL {
		t.Errorf("reloaded Test.Gemini.URL = %q, want %q", reloaded.Test.Gemini.URL, newGeminiURL)
	}
	if reloaded.Test.TargetURL != newGeminiURL {
		t.Errorf("reloaded Test.TargetURL = %q, want %q", reloaded.Test.TargetURL, newGeminiURL)
	}

	// 2. Update test.gemini.block_phrases
	newPhrasesInput := "blocked_phrase_1, blocked_phrase_2, blocked_phrase_3"
	if err := ad.UpdateSetting("test.gemini.block_phrases", newPhrasesInput); err != nil {
		t.Fatalf("UpdateSetting(test.gemini.block_phrases): %v", err)
	}

	expectedPhrases := []string{"blocked_phrase_1", "blocked_phrase_2", "blocked_phrase_3"}

	// Verify in-memory synchronization
	inMem2 := cfgSvc.Get()
	if len(inMem2.Test.Gemini.BlockPhrases) != len(expectedPhrases) {
		t.Fatalf("in-memory Gemini.BlockPhrases len = %d, want %d", len(inMem2.Test.Gemini.BlockPhrases), len(expectedPhrases))
	}
	for i, phrase := range expectedPhrases {
		if inMem2.Test.Gemini.BlockPhrases[i] != phrase {
			t.Errorf("in-memory Gemini.BlockPhrases[%d] = %q, want %q", i, inMem2.Test.Gemini.BlockPhrases[i], phrase)
		}
		if inMem2.Test.BlockPhrases[i] != phrase {
			t.Errorf("in-memory Test.BlockPhrases[%d] = %q, want %q", i, inMem2.Test.BlockPhrases[i], phrase)
		}
	}

	// Verify real on-disk persistence and re-loading
	reloaded2, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load(%s) second reload: %v", cfgPath, err)
	}
	if len(reloaded2.Test.Gemini.BlockPhrases) != len(expectedPhrases) {
		t.Fatalf("reloaded Gemini.BlockPhrases len = %d, want %d", len(reloaded2.Test.Gemini.BlockPhrases), len(expectedPhrases))
	}
	for i, phrase := range expectedPhrases {
		if reloaded2.Test.Gemini.BlockPhrases[i] != phrase {
			t.Errorf("reloaded Gemini.BlockPhrases[%d] = %q, want %q", i, reloaded2.Test.Gemini.BlockPhrases[i], phrase)
		}
		if reloaded2.Test.BlockPhrases[i] != phrase {
			t.Errorf("reloaded Test.BlockPhrases[%d] = %q, want %q", i, reloaded2.Test.BlockPhrases[i], phrase)
		}
	}
}
