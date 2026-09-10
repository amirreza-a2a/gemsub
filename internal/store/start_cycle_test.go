package store_test

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gemsub/internal/store"
)

// generateTestLinks creates N distinct raw links for testing.
func generateTestLinks(n int) map[string]struct{} {
	links := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		raw := fmt.Sprintf("VLESS://User-%06d@Host-%d.Example.COM:443?type=tcp&security=tls&sni=Host-%d.example.com#Remark-%d",
			i, i%500, i%500, i)
		links[raw] = struct{}{}
	}
	return links
}

// TestStore_StartCycle_Semantics_EmptyAndNil verifies StartCycle behavior with empty and nil inputs.
func TestStore_StartCycle_Semantics_EmptyAndNil(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "empty_nil.json"), 2)

	// Populate 3 existing candidates
	links := []string{
		"vless://cand1@1.1.1.1:443#c1",
		"vless://cand2@1.1.1.1:443#c2",
		"vless://cand3@1.1.1.1:443#c3",
	}
	for _, l := range links {
		st.PutWithTransition(store.Result{
			Link:        l,
			Status:      store.StatusPassed,
			Latency:     50 * time.Millisecond,
			TransportOK: true,
			TestedAt:    time.Now(),
		})
	}
	revBefore := st.Revision()

	// 1. Empty map input
	st.StartCycle(map[string]struct{}{})
	if st.Revision() != revBefore+1 {
		t.Fatalf("expected revision increment to %d, got %d", revBefore+1, st.Revision())
	}
	if st.PendingPresentCountForTest() != 0 {
		t.Fatalf("expected 0 pendingPresent on empty input, got %d", st.PendingPresentCountForTest())
	}
	if st.PendingAbsentCountForTest() != 3 {
		t.Fatalf("expected 3 pendingAbsent on empty input, got %d", st.PendingAbsentCountForTest())
	}

	// 2. Nil map input
	revBefore = st.Revision()
	st.StartCycle(nil)
	if st.Revision() != revBefore+1 {
		t.Fatalf("expected revision increment to %d, got %d", revBefore+1, st.Revision())
	}
	if st.PendingPresentCountForTest() != 0 {
		t.Fatalf("expected 0 pendingPresent on nil input, got %d", st.PendingPresentCountForTest())
	}
	if st.PendingAbsentCountForTest() != 3 {
		t.Fatalf("expected 3 pendingAbsent on nil input, got %d", st.PendingAbsentCountForTest())
	}
}

// TestStore_StartCycle_Semantics_DuplicatesAndNormalization verifies that raw links
// sharing a canonical identity collapse into a single canonical entry in pendingPresent.
func TestStore_StartCycle_Semantics_DuplicatesAndNormalization(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "duplicates.json"), 2)

	// Two raw links differing only in query parameter ordering and scheme/host case
	raw1 := "vless://user@host.com:443?b=2&a=1#same-remark"
	raw2 := "VLESS://user@HOST.COM:443?a=1&b=2#same-remark"

	st.PutWithTransition(store.Result{
		Link:        raw1,
		Status:      store.StatusPassed,
		Latency:     50 * time.Millisecond,
		TransportOK: true,
		TestedAt:    time.Now(),
	})

	canonical := store.CanonicalizeLink(raw1)

	// Provide both raw duplicates in currentLinks
	input := map[string]struct{}{
		raw1: {},
		raw2: {},
	}

	st.StartCycle(input)

	if st.PendingPresentCountForTest() != 1 {
		t.Fatalf("expected exactly 1 pendingPresent for duplicate raw links, got %d", st.PendingPresentCountForTest())
	}
	if !st.IsPendingPresentForTest(canonical) {
		t.Fatalf("expected canonical link %s to be pendingPresent", canonical)
	}
	if st.PendingAbsentCountForTest() != 0 {
		t.Fatalf("expected 0 pendingAbsent, got %d", st.PendingAbsentCountForTest())
	}
}

// TestStore_StartCycle_Semantics_ExistingAndNewCandidates verifies presence/absence
// partitioning between existing store records and newly scraped links.
func TestStore_StartCycle_Semantics_ExistingAndNewCandidates(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "existing_new.json"), 2)

	// Pre-populate 2 candidates
	st.PutWithTransition(store.Result{Link: "vless://kept@1.1.1.1:443", Status: store.StatusPassed, TestedAt: time.Now()})
	st.PutWithTransition(store.Result{Link: "vless://dropped@1.1.1.1:443", Status: store.StatusPassed, TestedAt: time.Now()})

	keptCanon := store.CanonicalizeLink("vless://kept@1.1.1.1:443")
	droppedCanon := store.CanonicalizeLink("vless://dropped@1.1.1.1:443")
	newCanon := store.CanonicalizeLink("vless://brand-new@1.1.1.1:443")

	// Cycle input contains "kept" and a "brand-new" candidate not yet in store.records
	input := map[string]struct{}{
		"vless://kept@1.1.1.1:443":      {},
		"vless://brand-new@1.1.1.1:443": {},
	}

	st.StartCycle(input)

	// Invariant: s.records only contains kept and dropped.
	// "kept" is in input -> pendingPresent
	// "dropped" is not in input -> pendingAbsent
	// "brand-new" is in input -> pendingPresent, but not yet probed into s.records
	if !st.IsPendingPresentForTest(keptCanon) {
		t.Fatalf("expected kept candidate to be pendingPresent")
	}
	if !st.IsPendingAbsentForTest(droppedCanon) {
		t.Fatalf("expected dropped candidate to be pendingAbsent")
	}
	if st.IsPendingAbsentForTest(newCanon) {
		t.Fatalf("brand-new candidate must not be marked pendingAbsent")
	}
	if _, exists := st.GetRecord(newCanon); exists {
		t.Fatalf("brand-new candidate must not exist in store records prior to probing")
	}
}

// TestStore_StartCycle_Semantics_AbsentAndEvictionLifecycle verifies that absent candidates
// retain state until FinishCycle commits their absence increments, and are evicted after MaxAbsentCycles.
func TestStore_StartCycle_Semantics_AbsentAndEvictionLifecycle(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "eviction.json"), 2) // MaxAbsentCycles = 2

	linkPresent := "vless://stay@1.1.1.1:443"
	linkAbsent := "vless://leave@1.1.1.1:443"

	st.PutWithTransition(store.Result{Link: linkPresent, Status: store.StatusPassed, TestedAt: time.Now()})
	st.PutWithTransition(store.Result{Link: linkAbsent, Status: store.StatusPassed, TestedAt: time.Now()})

	leaveCanon := store.CanonicalizeLink(linkAbsent)
	stayCanon := store.CanonicalizeLink(linkPresent)

	// Cycle 1: leave is absent
	st.StartCycle(map[string]struct{}{linkPresent: {}})
	rec, _ := st.GetRecord(leaveCanon)
	if rec.AbsentCycles != 0 {
		t.Fatalf("StartCycle must not prematurely mutate AbsentCycles: got %d, want 0", rec.AbsentCycles)
	}
	st.FinishCycle()
	rec, _ = st.GetRecord(leaveCanon)
	if rec.AbsentCycles != 1 {
		t.Fatalf("after FinishCycle 1, expected AbsentCycles 1, got %d", rec.AbsentCycles)
	}

	// Cycle 2: leave is absent again
	st.StartCycle(map[string]struct{}{linkPresent: {}})
	st.FinishCycle()
	rec, _ = st.GetRecord(leaveCanon)
	if rec.AbsentCycles != 2 {
		t.Fatalf("after FinishCycle 2, expected AbsentCycles 2, got %d", rec.AbsentCycles)
	}

	// Cycle 3: leave is absent again -> exceeds MaxAbsentCycles (2), should be evicted on FinishCycle
	st.StartCycle(map[string]struct{}{linkPresent: {}})
	st.FinishCycle()

	if _, exists := st.GetRecord(leaveCanon); exists {
		t.Fatalf("candidate %s should have been evicted after exceeding MaxAbsentCycles", leaveCanon)
	}
	if _, exists := st.GetRecord(stayCanon); !exists {
		t.Fatalf("candidate %s should remain in store", stayCanon)
	}
}

// TestStore_StartCycle_ConcurrentPutWithTransition verifies that concurrent probe writes
// during a large StartCycle transition complete safely without race conditions or data loss.
func TestStore_StartCycle_ConcurrentPutWithTransition(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "concurrent.json"), 2)

	// Pre-populate 10,000 candidates
	initialLinks := generateTestLinks(10000)
	now := time.Now()
	for raw := range initialLinks {
		st.PutWithTransition(store.Result{
			Link:        raw,
			Status:      store.StatusPassed,
			Latency:     50 * time.Millisecond,
			TransportOK: true,
			TestedAt:    now,
		})
	}

	startBarrier := make(chan struct{})
	var wg sync.WaitGroup

	// Concurrently invoke PutWithTransition from 4 workers
	numWorkers := 4
	probesPerWorker := 250
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			<-startBarrier
			for p := 0; p < probesPerWorker; p++ {
				link := fmt.Sprintf("vless://user-%d-%d@concurrent.example.com:443", workerID, p)
				st.PutWithTransition(store.Result{
					Link:        link,
					Status:      store.StatusPassed,
					Latency:     time.Duration(40+p%50) * time.Millisecond,
					TransportOK: true,
					TestedAt:    time.Now(),
				})
			}
		}(w)
	}

	// Concurrently invoke StartCycle
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startBarrier
		st.StartCycle(initialLinks)
	}()

	// Release all goroutines simultaneously
	close(startBarrier)
	wg.Wait()

	// Verify all concurrent records exist and Store invariants are preserved
	for w := 0; w < numWorkers; w++ {
		for p := 0; p < probesPerWorker; p++ {
			link := fmt.Sprintf("vless://user-%d-%d@concurrent.example.com:443", w, p)
			canon := store.CanonicalizeLink(link)
			if rec, ok := st.GetRecord(canon); !ok || rec == nil {
				t.Fatalf("missing record for concurrent probe %s", canon)
			}
		}
	}

	if count := st.Count(); count != 10000+(numWorkers*probesPerWorker) {
		t.Fatalf("expected total count %d, got %d", 10000+(numWorkers*probesPerWorker), count)
	}
}

// TestStore_StartCycle_Equivalence verifies that StartCycle produces identical canonical
// and pending states as the baseline definition across multi-cycle state evolutions.
func TestStore_StartCycle_Equivalence(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "equivalence.json"), 2)

	allLinks := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		allLinks[i] = fmt.Sprintf("VLESS://User-%04d@Host-%d.Example.COM:443?sni=host-%d.com#remark-%d",
			i, i%50, i%50, i)
		st.PutWithTransition(store.Result{
			Link:        allLinks[i],
			Status:      store.StatusPassed,
			Latency:     50 * time.Millisecond,
			TransportOK: true,
			TestedAt:    time.Now(),
		})
	}

	// Subset 1: candidates 0..799 present (800 present, 200 absent)
	subset1 := make(map[string]struct{}, 800)
	for i := 0; i < 800; i++ {
		subset1[allLinks[i]] = struct{}{}
	}

	st.StartCycle(subset1)

	if st.PendingPresentCountForTest() != 800 {
		t.Fatalf("expected 800 pendingPresent, got %d", st.PendingPresentCountForTest())
	}
	if st.PendingAbsentCountForTest() != 200 {
		t.Fatalf("expected 200 pendingAbsent, got %d", st.PendingAbsentCountForTest())
	}

	for i := 0; i < 800; i++ {
		if !st.IsPendingPresentForTest(store.CanonicalizeLink(allLinks[i])) {
			t.Fatalf("expected candidate %d to be pendingPresent", i)
		}
	}
	for i := 800; i < 1000; i++ {
		if !st.IsPendingAbsentForTest(store.CanonicalizeLink(allLinks[i])) {
			t.Fatalf("expected candidate %d to be pendingAbsent", i)
		}
	}
}

func computeDurationStats(durations []time.Duration) (min, median, p95, max time.Duration) {
	if len(durations) == 0 {
		return 0, 0, 0, 0
	}
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	min = sorted[0]
	max = sorted[len(sorted)-1]
	median = sorted[len(sorted)/2]
	p95Idx := int(float64(len(sorted)-1) * 0.95)
	p95 = sorted[p95Idx]
	return min, median, p95, max
}

// TestStore_StartCycle_LockHoldDuration_50k verifies that on 50,000 candidates,
// across repeated cycles, the exclusive write-lock hold duration remains strictly under 25ms.
func TestStore_StartCycle_LockHoldDuration_50k(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "start_cycle_50k.json"), 2)

	links := generateTestLinks(50000)
	now := time.Now()
	for raw := range links {
		st.PutWithTransition(store.Result{
			Link:        raw,
			Status:      store.StatusPassed,
			Latency:     50 * time.Millisecond,
			TransportOK: true,
			TestedAt:    now,
		})
	}

	iterations := 5
	preDurations := make([]time.Duration, iterations)
	lockDurations := make([]time.Duration, iterations)

	for i := 0; i < iterations; i++ {
		preDurations[i], lockDurations[i] = st.StartCycleWithTimingForTest(links)
	}

	minPre, medPre, p95Pre, maxPre := computeDurationStats(preDurations)
	minLock, medLock, p95Lock, maxLock := computeDurationStats(lockDurations)

	t.Logf("50k candidates (n=%d reps):", iterations)
	t.Logf("  Preprocessing (unlocked): min=%v med=%v p95=%v max=%v", minPre, medPre, p95Pre, maxPre)
	t.Logf("  Write-Lock Hold (locked): min=%v med=%v p95=%v max=%v", minLock, medLock, p95Lock, maxLock)

	limit := 25 * time.Millisecond
	if raceDetectorActive {
		// ThreadSanitizer instruments every memory access and mutex operation,
		// introducing ~2-5x overhead on high-volume CPU/memory loops.
		limit = 80 * time.Millisecond
	}

	if medLock >= limit {
		t.Fatalf("median write-lock hold time (%v) exceeded %v limit on 50k candidates", medLock, limit)
	}
	if p95Lock >= limit && !raceDetectorActive {
		t.Fatalf("p95 write-lock hold time (%v) exceeded %v limit on 50k candidates", p95Lock, limit)
	}
}

// TestStore_StartCycle_LockHoldDuration_62k verifies that on 62,000 candidates,
// across repeated cycles, the exclusive write-lock hold duration remains strictly under 25ms.
func TestStore_StartCycle_LockHoldDuration_62k(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "start_cycle_62k.json"), 2)

	links := generateTestLinks(62000)
	now := time.Now()
	for raw := range links {
		st.PutWithTransition(store.Result{
			Link:        raw,
			Status:      store.StatusPassed,
			Latency:     50 * time.Millisecond,
			TransportOK: true,
			TestedAt:    now,
		})
	}

	iterations := 5
	preDurations := make([]time.Duration, iterations)
	lockDurations := make([]time.Duration, iterations)

	for i := 0; i < iterations; i++ {
		preDurations[i], lockDurations[i] = st.StartCycleWithTimingForTest(links)
	}

	minPre, medPre, p95Pre, maxPre := computeDurationStats(preDurations)
	minLock, medLock, p95Lock, maxLock := computeDurationStats(lockDurations)

	t.Logf("62k candidates (n=%d reps):", iterations)
	t.Logf("  Preprocessing (unlocked): min=%v med=%v p95=%v max=%v", minPre, medPre, p95Pre, maxPre)
	t.Logf("  Write-Lock Hold (locked): min=%v med=%v p95=%v max=%v", minLock, medLock, p95Lock, maxLock)

	limit := 25 * time.Millisecond
	if raceDetectorActive {
		limit = 80 * time.Millisecond
	}

	if medLock >= limit {
		t.Fatalf("median write-lock hold time (%v) exceeded %v limit on 62k candidates", medLock, limit)
	}
	if p95Lock >= limit && !raceDetectorActive {
		t.Fatalf("p95 write-lock hold time (%v) exceeded %v limit on 62k candidates", p95Lock, limit)
	}
}
