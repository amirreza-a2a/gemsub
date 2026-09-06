package store_test

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gemsub/internal/store"
)

// TestScoring_ObservationModelVerifiesResultPassedIsNotConflated checks that Result.Passed
// strictly represents Result.Status == StatusPassed and is never conflated with servability.
func TestScoring_ObservationModelVerifiesResultPassedIsNotConflated(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "test.json"), 2)
	link := "vless://user@host.com:443?type=tcp#Node"

	// 1. Passed probe: Passed is true, IsServable is true
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Category: store.ErrNone,
	})
	r, ok := st.Get(link)
	if !ok {
		t.Fatalf("link not found in store")
	}
	if !r.Passed || r.Status != store.StatusPassed {
		t.Errorf("expected Passed=true and StatusPassed, got passed=%v status=%s", r.Passed, r.Status)
	}
	if !st.IsServable(r) {
		t.Errorf("expected IsServable=true")
	}

	// 2. Inconclusive probe: Passed MUST BE FALSE, but IsServable is true
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
	})
	r, ok = st.Get(link)
	if !ok {
		t.Fatalf("link not found in store")
	}
	if r.Passed {
		t.Errorf("CRITICAL VIOLATION: Result.Passed must be false when Status is StatusInconclusive!")
	}
	if r.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive, got %s", r.Status)
	}
	if !st.IsServable(r) {
		t.Errorf("expected IsServable=true (score remains >= threshold)")
	}
	if len(st.Passing()) != 1 {
		t.Errorf("expected link in Passing() list")
	}

	// 3. Failed probe: Passed is false, IsServable is false
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusFailed,
		Category: store.ErrConnRefused,
	})
	r, ok = st.Get(link)
	if !ok {
		t.Fatalf("link not found in store")
	}
	if r.Passed || st.IsServable(r) {
		t.Errorf("expected Passed=false and IsServable=false for failed probe")
	}
}

// TestScoring_TargetPolicyOverride verifies that ErrRegionBlocked and ErrTargetDenied
// immediately disqualify a candidate regardless of high historical score.
func TestScoring_TargetPolicyOverride(t *testing.T) {
	cases := []struct {
		name     string
		category store.ErrorCategory
	}{
		{"ErrRegionBlocked", store.ErrRegionBlocked},
		{"ErrTargetDenied", store.ErrTargetDenied},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New(filepath.Join(t.TempDir(), "test.json"), 2)
			link := "vless://user@host.com:443?type=tcp#Node"

			// Populate 10 consecutive passes so the score is 1.0
			for i := 0; i < 10; i++ {
				st.PutWithTransition(store.Result{
					Link:     link,
					Status:   store.StatusPassed,
					Category: store.ErrNone,
				})
			}

			rec, ok := st.GetRecord(link)
			if !ok || rec.Score < 0.99 {
				t.Fatalf("expected perfect score, got %+v", rec)
			}
			if len(st.Passing()) != 1 {
				t.Fatalf("expected link to be passing before target restriction")
			}

			// Probing returns target policy rejection
			st.PutWithTransition(store.Result{
				Link:     link,
				Status:   store.StatusFailed,
				Category: tc.category,
			})

			rec, _ = st.GetRecord(link)
			// History score with lambda=0.75 and weight=0.0 on latest sample would still be:
			// ~0.75, which is > 0.65 threshold!
			if rec.Score < 0.65 {
				t.Logf("score dropped below 0.65: %f", rec.Score)
			}

			// BUT Gate 2 (Target Policy Override) MUST immediately disqualify it!
			if st.IsServableRecord(rec) {
				t.Fatalf("CRITICAL VIOLATION: candidate with %s must be disqualified by Target Policy Override regardless of score!", tc.category)
			}
			r, _ := st.Get(link)
			if st.IsServable(r) {
				t.Fatalf("expected IsServable(r) to be false")
			}
			if len(st.Passing()) != 0 {
				t.Fatalf("expected Passing() to be empty due to Target Policy Override")
			}
		})
	}
}

// TestScoring_ColdStartGate verifies that candidates are disqualified until
// MinObservationsForServing is satisfied.
func TestScoring_ColdStartGate(t *testing.T) {
	cfg := store.DefaultScoringConfig()
	cfg.MinObservationsForServing = 2 // require 2 observations

	st := store.NewWithConfig(filepath.Join(t.TempDir(), "test.json"), cfg)
	link := "vless://user@host.com:443#Node"

	// 1st observation: 1 pass
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Category: store.ErrNone,
	})

	rec, ok := st.GetRecord(link)
	if !ok {
		t.Fatalf("candidate not found")
	}
	// Score is 1.0, but only 1 observation is present
	if rec.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", rec.Score)
	}
	if st.IsServableRecord(rec) {
		t.Errorf("expected candidate to be blocked by Cold Start Gate (1 < 2 observations)")
	}
	if len(st.Passing()) != 0 {
		t.Errorf("expected Passing() to be empty before cold start threshold reached")
	}

	// 2nd observation: 2nd pass
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Category: store.ErrNone,
	})

	rec, _ = st.GetRecord(link)
	if !st.IsServableRecord(rec) {
		t.Errorf("expected candidate to become servable once MinObservationsForServing is met")
	}
	if len(st.Passing()) != 1 {
		t.Errorf("expected candidate in Passing() after cold start satisfied")
	}
}

// TestScoring_AbsenceLifecycleAndReappearance verifies that candidates disappearing from
// upstream retain their history during the grace period and resume upon reappearance.
func TestScoring_AbsenceLifecycleAndReappearance(t *testing.T) {
	cfg := store.DefaultScoringConfig()
	cfg.MaxAbsentCycles = 2

	st := store.NewWithConfig(filepath.Join(t.TempDir(), "test.json"), cfg)
	linkA := "vless://user@host-a.com:443#NodeA"
	linkB := "vless://user@host-b.com:443#NodeB"

	st.PutWithTransition(store.Result{Link: linkA, Status: store.StatusPassed})
	st.PutWithTransition(store.Result{Link: linkB, Status: store.StatusPassed})

	// Cycle 1: B is absent
	st.StartCycle(map[string]struct{}{linkA: {}})
	if len(st.Passing()) != 1 || st.Passing()[0] != linkA {
		t.Fatalf("expected only linkA in Passing() during cycle 1")
	}
	st.FinishCycle()

	recB, ok := st.GetRecord(linkB)
	if !ok || recB.AbsentCycles != 1 {
		t.Fatalf("expected linkB retained with AbsentCycles=1, got %+v", recB)
	}

	// Cycle 2: B reappears in upstream!
	st.StartCycle(map[string]struct{}{linkA: {}, linkB: {}})
	recB, ok = st.GetRecord(linkB)
	if !ok || recB.AbsentCycles != 0 {
		t.Fatalf("expected linkB AbsentCycles reset to 0 upon reappearance, got %+v", recB)
	}

	// Because B reappeared and its history was preserved, it is immediately servable!
	if len(st.Passing()) != 2 {
		t.Fatalf("expected both linkA and linkB in Passing() after reappearance, got %v", st.Passing())
	}
	st.FinishCycle()
}

// TestScoring_CycleAbortAndSkippedProbes verifies that aborted cycles do not commit absence,
// and skipped probes (due to ProbeLimit) do not count as absent or failed.
func TestScoring_CycleAbortAndSkippedProbes(t *testing.T) {
	cfg := store.DefaultScoringConfig()
	cfg.MaxAbsentCycles = 2

	st := store.NewWithConfig(filepath.Join(t.TempDir(), "test.json"), cfg)
	link1 := "vless://user@host1.com:443#Node1"
	link2 := "vless://user@host2.com:443#Node2"

	st.PutWithTransition(store.Result{Link: link1, Status: store.StatusPassed})
	st.PutWithTransition(store.Result{Link: link2, Status: store.StatusPassed})
	st.FinishCycle()

	// 1. Cycle Start with link2 missing, BUT cycle is cancelled/aborted before FinishCycle()
	st.StartCycle(map[string]struct{}{link1: {}})
	// Abort happens! FinishCycle is NEVER called.
	// Now next cycle starts with both links:
	st.StartCycle(map[string]struct{}{link1: {}, link2: {}})
	st.FinishCycle()

	rec2, ok := st.GetRecord(link2)
	if !ok || rec2.AbsentCycles != 0 {
		t.Fatalf("expected AbsentCycles=0 because aborted cycle never committed increments, got %+v", rec2)
	}

	// 2. Skipped probes: link2 was present in upstream linkSet, but not probed (e.g. ProbeLimit).
	// StartCycle has both link1 and link2:
	st.StartCycle(map[string]struct{}{link1: {}, link2: {}})
	// Only link1 is probed:
	st.PutWithTransition(store.Result{Link: link1, Status: store.StatusPassed})
	// link2 is NOT probed (skipped != failed, skipped != absent)
	st.FinishCycle()

	rec2, _ = st.GetRecord(link2)
	if rec2.AbsentCycles != 0 {
		t.Fatalf("skipped probe must not increment AbsentCycles, got %d", rec2.AbsentCycles)
	}
	if rec2.History.Count != 1 {
		t.Fatalf("skipped probe must not add failure samples to history, sample count=%d", rec2.History.Count)
	}
	// link2 remains servable
	if len(st.Passing()) != 2 {
		t.Fatalf("skipped candidate with good history must remain in Passing(), got %v", st.Passing())
	}
}

// TestScoring_PassingRanked verifies sorting by Score desc, Latency asc, ActiveLink asc.
func TestScoring_PassingRanked(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "test.json"), 2)

	linkHighLatency := "vless://user@host-high.com:443#HighLatency"
	linkLowLatency := "vless://user@host-low.com:443#LowLatency"
	linkLowerScore := "vless://user@host-lowerscore.com:443#LowerScore"

	// linkLowLatency: perfect score, 50ms
	for i := 0; i < 5; i++ {
		st.PutWithTransition(store.Result{
			Link:     linkLowLatency,
			Status:   store.StatusPassed,
			Latency:  50 * time.Millisecond,
			Category: store.ErrNone,
		})
	}

	// linkHighLatency: perfect score, 200ms
	for i := 0; i < 5; i++ {
		st.PutWithTransition(store.Result{
			Link:     linkHighLatency,
			Status:   store.StatusPassed,
			Latency:  200 * time.Millisecond,
			Category: store.ErrNone,
		})
	}

	// linkLowerScore: 3 passes then 1 timeout -> lower score, 30ms latency
	for i := 0; i < 3; i++ {
		st.PutWithTransition(store.Result{
			Link:     linkLowerScore,
			Status:   store.StatusPassed,
			Latency:  30 * time.Millisecond,
			Category: store.ErrNone,
		})
	}
	st.PutWithTransition(store.Result{
		Link:     linkLowerScore,
		Status:   store.StatusInconclusive,
		Latency:  30 * time.Millisecond,
		Category: store.ErrTimeout,
	})

	ranked := st.PassingRanked()
	if len(ranked) != 3 {
		t.Fatalf("expected 3 ranked passing links, got %d", len(ranked))
	}

	// 1st: linkLowLatency (highest score, lowest latency among equal score)
	if ranked[0] != linkLowLatency {
		t.Errorf("expected 1st ranked to be %s, got %s", linkLowLatency, ranked[0])
	}
	// 2nd: linkHighLatency (highest score, higher latency)
	if ranked[1] != linkHighLatency {
		t.Errorf("expected 2nd ranked to be %s, got %s", linkHighLatency, ranked[1])
	}
	// 3rd: linkLowerScore (lower score despite low latency)
	if ranked[2] != linkLowerScore {
		t.Errorf("expected 3rd ranked to be %s, got %s", linkLowerScore, ranked[2])
	}
}

// TestScoring_PersistenceV2AndScoreRecomputation verifies that Version 2 snapshots
// recompute the Score deterministically on load using the authoritative BoundedHistory.
func TestScoring_PersistenceV2AndScoreRecomputation(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "v2_state.json")
	st := store.New(stateFile, 2)

	link := "vless://user@host.com:443?b=2&a=1#Node"
	for i := 0; i < 5; i++ {
		st.PutWithTransition(store.Result{
			Link:     link,
			Status:   store.StatusPassed,
			Category: store.ErrNone,
		})
	}
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
	})

	rec, _ := st.GetRecord(link)
	origScore := rec.Score
	if origScore <= 0 {
		t.Fatalf("invalid score: %f", origScore)
	}

	if err := st.Save(); err != nil {
		t.Fatalf("failed to save V2 state: %v", err)
	}

	// Reload into a fresh store
	st2 := store.New(stateFile, 2)
	if err := st2.Load(); err != nil {
		t.Fatalf("failed to load state: %v", err)
	}

	rec2, ok := st2.GetRecord(link)
	if !ok {
		t.Fatalf("link not found in reloaded store")
	}
	if rec2.Score != origScore {
		t.Fatalf("expected recomputed score %f to match original %f", rec2.Score, origScore)
	}
	if rec2.History.Count != 6 {
		t.Fatalf("expected 6 samples in history, got %d", rec2.History.Count)
	}
	if len(st2.Passing()) != 1 {
		t.Fatalf("expected link in Passing() after reload")
	}
}

// TestScoring_Concurrency verifies concurrent safety under heavy read/write contention.
func TestScoring_Concurrency(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "race.json"), 2)

	links := make([]string, 20)
	for i := range links {
		links[i] = fmt.Sprintf("vless://user%d@host%d.com:443?type=tcp#Node%d", i, i, i)
	}

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Writers: PutWithTransition
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
			for {
				select {
				case <-ctx.Done():
					return
				default:
					idx := r.Intn(len(links))
					status := store.StatusPassed
					category := store.ErrNone
					if r.Float64() < 0.3 {
						status = store.StatusInconclusive
						category = store.ErrTimeout
					}
					st.PutWithTransition(store.Result{
						Link:     links[idx],
						Status:   status,
						Category: category,
						Latency:  time.Duration(r.Intn(200)) * time.Millisecond,
					})
				}
			}
		}(w)
	}

	// Readers: Passing, PassingRanked, Stats, All, Get
	for rID := 0; rID < 4; rID++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(readerID+100)))
			for {
				select {
				case <-ctx.Done():
					return
				default:
					_ = st.Passing()
					_ = st.PassingRanked()
					_ = st.Stats()
					_ = st.All()
					idx := r.Intn(len(links))
					_, _ = st.Get(links[idx])
					_, _ = st.GetRecord(links[idx])
				}
			}
		}(rID)
	}

	// Lifecycle manager: StartCycle / FinishCycle / Save
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				linkSet := make(map[string]struct{})
				for i := 0; i < 15; i++ {
					linkSet[links[i]] = struct{}{}
				}
				st.StartCycle(linkSet)
				time.Sleep(10 * time.Millisecond)
				st.FinishCycle()
				_ = st.Save()
			}
		}
	}()

	wg.Wait()
}
