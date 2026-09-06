package store_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
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

// TestScoringConfig_DefensiveCopyBothDirections verifies that mutating the input ScoringConfig
// or the returned Config() map does not affect the store's internal state.
func TestScoringConfig_DefensiveCopyBothDirections(t *testing.T) {
	cfg := store.DefaultScoringConfig()
	cfg.CategoryWeights[store.ErrTimeout] = 0.50

	st := store.NewWithConfig(filepath.Join(t.TempDir(), "test.json"), cfg)

	// Mutate original cfg map after store initialization
	cfg.CategoryWeights[store.ErrTimeout] = 0.99

	// Verify store's internal config was not mutated
	retrieved := st.Config()
	if retrieved.CategoryWeights[store.ErrTimeout] != 0.50 {
		t.Fatalf("store config was mutated via external cfg map; expected 0.50, got %f", retrieved.CategoryWeights[store.ErrTimeout])
	}

	// Mutate the returned config map
	retrieved.CategoryWeights[store.ErrTimeout] = 0.10

	// Retrieve again and verify store's internal config was not mutated
	retrieved2 := st.Config()
	if retrieved2.CategoryWeights[store.ErrTimeout] != 0.50 {
		t.Fatalf("store config was mutated via returned Config() map; expected 0.50, got %f", retrieved2.CategoryWeights[store.ErrTimeout])
	}
}

// TestScoring_LatencyRankingPreservesLastValidPassed verifies that latency used for ranking
// is taken exclusively from StatusPassed observations and non-passing probe durations do not overwrite it.
func TestScoring_LatencyRankingPreservesLastValidPassed(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "latency.json"), 2)

	linkA := "vless://user@host-a.com:443#CandidateA"
	linkB := "vless://user@host-b.com:443#CandidateB"

	// linkA: Pass with 100ms
	st.PutWithTransition(store.Result{
		Link:     linkA,
		Status:   store.StatusPassed,
		Latency:  100 * time.Millisecond,
		Category: store.ErrNone,
	})
	// linkB: Pass with 200ms
	st.PutWithTransition(store.Result{
		Link:     linkB,
		Status:   store.StatusPassed,
		Latency:  200 * time.Millisecond,
		Category: store.ErrNone,
	})

	// Now linkA experiences an inconclusive probe with high timeout latency (4000ms)
	st.PutWithTransition(store.Result{
		Link:     linkA,
		Status:   store.StatusInconclusive,
		Latency:  4000 * time.Millisecond,
		Category: store.ErrTimeout,
	})

	// And linkB experiences an inconclusive probe with high timeout latency (4000ms)
	st.PutWithTransition(store.Result{
		Link:     linkB,
		Status:   store.StatusInconclusive,
		Latency:  4000 * time.Millisecond,
		Category: store.ErrTimeout,
	})

	// Both A and B have equal scores.
	// linkA's ranking latency must remain 100ms (its last valid StatusPassed latency).
	// linkB's ranking latency must remain 200ms.
	// Therefore, linkA MUST rank before linkB!
	ranked := st.PassingRanked()
	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked links, got %d", len(ranked))
	}
	if ranked[0] != linkA {
		t.Errorf("expected linkA (100ms last passed latency) to rank ahead of linkB (200ms), got %s", ranked[0])
	}

	// Candidates that have never passed must rank behind candidates with at least one pass.
	cfg := store.DefaultScoringConfig()
	cfg.MinObservationsForServing = 1
	cfg.MinServableScore = 0.30 // Allow inconclusive candidate with score 0.40 to be servable
	st2 := store.NewWithConfig(filepath.Join(t.TempDir(), "latency2.json"), cfg)

	linkProven := "vless://proven@host.com:443#Proven"
	linkUnproven := "vless://unproven@host.com:443#Unproven"

	// Proven: Pass at 500ms
	st2.PutWithTransition(store.Result{
		Link:     linkProven,
		Status:   store.StatusPassed,
		Latency:  500 * time.Millisecond,
		Category: store.ErrNone,
	})

	// Unproven: Inconclusive at 10ms (never passed)
	st2.PutWithTransition(store.Result{
		Link:     linkUnproven,
		Status:   store.StatusInconclusive,
		Latency:  10 * time.Millisecond,
		Category: store.ErrTimeout,
	})

	ranked2 := st2.PassingRanked()
	if len(ranked2) != 2 {
		t.Fatalf("expected 2 ranked links, got %d", len(ranked2))
	}
	if ranked2[0] != linkProven {
		t.Errorf("expected proven link to rank ahead of unproven link regardless of latency, got 1st: %s, 2nd: %s", ranked2[0], ranked2[1])
	}
}

// TestScoring_TargetPolicyOverride_ValidAndInvalidCombinations verifies that only StatusFailed
// with target policy errors triggers Gate 2, whereas invalid combinations do not.
func TestScoring_TargetPolicyOverride_ValidAndInvalidCombinations(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "target_policy.json"), 2)

	// Valid trigger: StatusFailed + ErrRegionBlocked -> Disqualified
	linkBlocked := "vless://user@blocked.com:443#Blocked"
	st.PutWithTransition(store.Result{
		Link:     linkBlocked,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
	})
	rBlocked, _ := st.Get(linkBlocked)
	if st.IsServable(rBlocked) {
		t.Errorf("expected StatusFailed + ErrRegionBlocked to be disqualified by target policy override")
	}

	// Valid trigger: StatusFailed + ErrTargetDenied -> Disqualified
	linkDenied := "vless://user@denied.com:443#Denied"
	st.PutWithTransition(store.Result{
		Link:     linkDenied,
		Status:   store.StatusFailed,
		Category: store.ErrTargetDenied,
	})
	rDenied, _ := st.Get(linkDenied)
	if st.IsServable(rDenied) {
		t.Errorf("expected StatusFailed + ErrTargetDenied to be disqualified by target policy override")
	}

	// Invalid combinations:
	// A probe with StatusPassed but anomalous ErrRegionBlocked category (e.g. synthetic or malformed)
	// must NOT be disqualified by Gate 2 (only StatusFailed with target errors triggers Gate 2).
	linkPassWithAnomalousCat := "vless://user@anomalous.com:443#PassAnomalous"
	for i := 0; i < 5; i++ {
		st.PutWithTransition(store.Result{
			Link:     linkPassWithAnomalousCat,
			Status:   store.StatusPassed,
			Category: store.ErrRegionBlocked,
		})
	}
	rPass, _ := st.Get(linkPassWithAnomalousCat)
	if !st.IsServable(rPass) {
		t.Errorf("expected StatusPassed not to trigger target policy override even if Category was anomalous")
	}

	// An inconclusive probe with ErrRegionBlocked must NOT trigger target policy override.
	linkInconclusive := "vless://user@inconclusive.com:443#Inconclusive"
	for i := 0; i < 10; i++ {
		st.PutWithTransition(store.Result{
			Link:     linkInconclusive,
			Status:   store.StatusPassed,
			Category: store.ErrNone,
		})
	}
	st.PutWithTransition(store.Result{
		Link:     linkInconclusive,
		Status:   store.StatusInconclusive,
		Category: store.ErrRegionBlocked,
	})
	rInconclusive, _ := st.Get(linkInconclusive)
	if !st.IsServable(rInconclusive) {
		t.Errorf("expected StatusInconclusive not to trigger target policy override (requires StatusFailed)")
	}
}

// TestScoring_LegacyV1MigrationDoesNotFabricateObservations verifies that V1 migration
// does not synthesize historical passes, timestamps, or repeated inconclusive samples.
func TestScoring_LegacyV1MigrationDoesNotFabricateObservations(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "v1_legacy.json")
	v1Content := `{
  "results": [
    {
      "link": "vless://user@host.com:443#InconclusiveNode",
      "status": "inconclusive",
      "category": "timeout",
      "reason": "i/o timeout",
      "latency": 3000000000,
      "tested_at": "2026-09-01T12:00:00Z",
      "attempts": 2,
      "previously_passed": true,
      "consecutive_inconclusive": 2
    },
    {
      "link": "vless://user@host2.com:443#PassedNode",
      "status": "passed",
      "category": "none",
      "reason": "ok",
      "latency": 150000000,
      "tested_at": "2026-09-01T12:00:00Z",
      "attempts": 1,
      "previously_passed": true,
      "consecutive_inconclusive": 0
    }
  ],
  "last_cycle": "2026-09-01T12:00:00Z",
  "cycle_count": 5
}`
	if err := os.WriteFile(stateFile, []byte(v1Content), 0o644); err != nil {
		t.Fatalf("failed to write v1 state: %v", err)
	}

	st := store.New(stateFile, 2)
	if err := st.Load(); err != nil {
		t.Fatalf("failed to load legacy v1 state: %v", err)
	}

	link1 := "vless://user@host.com:443#InconclusiveNode"
	rec1, ok := st.GetRecord(link1)
	if !ok {
		t.Fatalf("expected record for %s", link1)
	}

	// Authoritative history invariant: strictly ONE authentic sample
	if rec1.History.Count != 1 {
		t.Fatalf("expected exactly 1 sample in history without fabricated passes/duplicates, got %d", rec1.History.Count)
	}
	sample1 := rec1.History.ChronologicalSamples()[0]
	if sample1.Status != store.StatusInconclusive {
		t.Errorf("expected sample status StatusInconclusive, got %s", sample1.Status)
	}
	if sample1.Category != store.ErrTimeout {
		t.Errorf("expected sample category ErrTimeout, got %s", sample1.Category)
	}
	expectedTime, _ := time.Parse(time.RFC3339, "2026-09-01T12:00:00Z")
	if !sample1.TestedAt.Equal(expectedTime) {
		t.Errorf("expected exact authentic TestedAt %v, got %v", expectedTime, sample1.TestedAt)
	}

	// Preserved LKG servability: should be servable because previously_passed=true and consecutive_inconclusive=2 <= maxAbsentCycles (2)
	if !st.IsServableRecord(rec1) {
		t.Errorf("expected legacy record with LKG to remain servable until next probe cycle")
	}

	// Verify candidate 2
	link2 := "vless://user@host2.com:443#PassedNode"
	rec2, ok := st.GetRecord(link2)
	if !ok {
		t.Fatalf("expected record for %s", link2)
	}
	if rec2.History.Count != 1 {
		t.Fatalf("expected exactly 1 sample in history for candidate 2, got %d", rec2.History.Count)
	}
	if rec2.LastPassedLatency != 150*time.Millisecond || !rec2.HasPassed {
		t.Errorf("expected LastPassedLatency=150ms and HasPassed=true, got latency=%v hasPassed=%v", rec2.LastPassedLatency, rec2.HasPassed)
	}
}

// TestScoring_MalformedV2SnapshotRecovery verifies that corrupted or malformed history
// structs in a V2 snapshot are normalized without panicking on load or subsequent pushes.
func TestScoring_MalformedV2SnapshotRecovery(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "malformed_v2.json")
	link := "vless://user@host.com:443#Node"
	malformedJSON := `{
  "version": 2,
  "cycle_count": 3,
  "last_cycle": "2026-09-01T12:00:00Z",
  "records": [
    {
      "canonical_link": "vless://user@host.com:443#Node",
      "active_link": "vless://user@host.com:443#Node",
      "score": 0.0,
      "absent_cycles": 0,
      "history": {
        "capacity": 0,
        "samples": null,
        "count": -1,
        "start": 5
      }
    }
  ]
}`
	if err := os.WriteFile(stateFile, []byte(malformedJSON), 0o644); err != nil {
		t.Fatalf("failed to write malformed state: %v", err)
	}

	st := store.New(stateFile, 2)
	if err := st.Load(); err != nil {
		t.Fatalf("Load() failed on malformed history: %v", err)
	}

	rec, ok := st.GetRecord(link)
	if !ok {
		t.Fatalf("failed to get candidate record")
	}

	// Invariants should be restored
	if rec.History.Capacity != 10 {
		t.Errorf("expected Capacity restored to default 10, got %d", rec.History.Capacity)
	}
	if len(rec.History.Samples) != 10 {
		t.Errorf("expected Samples length 10, got %d", len(rec.History.Samples))
	}
	if rec.History.Count != 0 {
		t.Errorf("expected Count clamped to 0, got %d", rec.History.Count)
	}
	if rec.History.Start != 0 {
		t.Errorf("expected Start clamped to 0, got %d", rec.History.Start)
	}

	// Subsequent push must work cleanly without panic
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Category: store.ErrNone,
	})

	recAfter, _ := st.GetRecord(link)
	if recAfter.History.Count != 1 {
		t.Errorf("expected 1 sample after push, got %d", recAfter.History.Count)
	}
}
