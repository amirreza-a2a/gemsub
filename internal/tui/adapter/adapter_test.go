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
	snap, updated := ad.PollSnapshot(false)
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
	snap, updated := ad.PollSnapshot(false)
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

	rowsASCII := ad.CandidateRows(false)
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

	rowsUnicode := ad.CandidateRows(false)
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
