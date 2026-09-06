package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gemsub/internal/store"
)

func TestStore_StateTransitionsAndLastKnownGood(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "test_state.json")

	st := store.New(stateFile, 2)

	link := "vless://example@1.2.3.4:443"

	// 1. Initial PASS -> StatusPassed, Passed=true
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Reason:   "ok",
		TestedAt: time.Now(),
	})

	passing := st.Passing()
	if len(passing) != 1 || passing[0] != link {
		t.Fatalf("expected link to be passing, got %v", passing)
	}
	stats := st.Stats()
	if stats.Passed != 1 || stats.Servable != 1 {
		t.Fatalf("expected 1 passed, 1 servable; got %+v", stats)
	}

	// 2. Cycle 1 Inconclusive -> Retain servability via scoring, but Passed represents latest probe outcome (false)
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusInconclusive,
		Reason:   "proxy tunnel CDN returned HTTP 429",
		TestedAt: time.Now(),
	})

	results := st.All()
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != store.StatusInconclusive {
		t.Errorf("expected canonical StatusInconclusive, got %s", r.Status)
	}
	// Result.Passed MUST represent probe observation outcome, NOT servability
	if r.Passed {
		t.Errorf("expected Passed=false (latest probe was inconclusive, not passed)")
	}
	if !st.IsServable(r) {
		t.Errorf("expected IsServable=true (score remains >= threshold)")
	}
	if r.ConsecutiveInconclusive != 1 {
		t.Errorf("expected ConsecutiveInconclusive=1, got %d", r.ConsecutiveInconclusive)
	}
	if !r.PreviouslyPassed {
		t.Errorf("expected PreviouslyPassed=true")
	}
	if len(st.Passing()) != 1 {
		t.Errorf("expected link to remain in Passing() list")
	}

	// 3. Establish 7 passes to test the verified numerical timeout decay sequence
	for i := 0; i < 7; i++ {
		st.PutWithTransition(store.Result{
			Link:     link,
			Status:   store.StatusPassed,
			Reason:   "ok",
			TestedAt: time.Now(),
		})
	}
	if len(st.Passing()) != 1 {
		t.Fatalf("expected 1 passing after 7 passes")
	}

	// Timeout 1: score ~0.841 >= 0.65 -> servable
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
		Reason:   "timeout",
		TestedAt: time.Now(),
	})
	results = st.All()
	r = results[0]
	if r.Passed || !st.IsServable(r) || len(st.Passing()) != 1 {
		t.Errorf("expected timeout 1 to be servable: passed=%v, servable=%v, passing=%d", r.Passed, st.IsServable(r), len(st.Passing()))
	}

	// Timeout 2: score ~0.722 >= 0.65 -> servable
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
		Reason:   "timeout",
		TestedAt: time.Now(),
	})
	results = st.All()
	r = results[0]
	if r.Passed || !st.IsServable(r) || len(st.Passing()) != 1 {
		t.Errorf("expected timeout 2 to be servable: passed=%v, servable=%v, passing=%d", r.Passed, st.IsServable(r), len(st.Passing()))
	}

	// Timeout 3: score ~0.632 < 0.65 -> drops below servable threshold
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
		Reason:   "timeout",
		TestedAt: time.Now(),
	})
	results = st.All()
	r = results[0]
	if r.Passed || st.IsServable(r) || len(st.Passing()) != 0 {
		t.Errorf("expected timeout 3 to fall below threshold: passed=%v, servable=%v, passing=%d", r.Passed, st.IsServable(r), len(st.Passing()))
	}

	// 4. Restore: PASS -> StatusPassed, Passed=true, consecutive reset
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Reason:   "ok",
		TestedAt: time.Now(),
	})
	results = st.All()
	r = results[0]
	if r.Status != store.StatusPassed || !r.Passed || r.ConsecutiveInconclusive != 0 || !st.IsServable(r) {
		t.Errorf("expected restored PASS, got %+v", r)
	}
	if len(st.Passing()) != 1 {
		t.Errorf("expected link in Passing() after restore")
	}

	// 5. Confirmed Failure -> PASS to FAILED with ErrRegionBlocked: Immediate policy eviction
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Reason:   "target returned regional restriction message",
		TestedAt: time.Now(),
	})
	results = st.All()
	r = results[0]
	if r.Status != store.StatusFailed || r.Passed || st.IsServable(r) {
		t.Errorf("expected immediate failure eviction, got %+v", r)
	}
	if len(st.Passing()) != 0 {
		t.Errorf("expected Passing() to be empty after confirmed failure")
	}
}

func TestStore_StartCycleAbsenceAndEviction(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "prune_state.json")
	st := store.New(stateFile, 2) // MaxAbsentCycles = 2

	st.PutWithTransition(store.Result{Link: "link1", Status: store.StatusPassed})
	st.PutWithTransition(store.Result{Link: "link2", Status: store.StatusPassed})

	if len(st.Passing()) != 2 {
		t.Fatalf("expected 2 passing initially")
	}

	currentLinks := map[string]struct{}{
		"link1": {},
	}

	// Cycle 1: link2 missing from upstream
	st.StartCycle(currentLinks)

	// Source Presence Gate: link2 is pending absent, immediately disqualified from serving
	if len(st.Passing()) != 1 || st.Passing()[0] != "link1" {
		t.Fatalf("expected only link1 in Passing() during active cycle, got %v", st.Passing())
	}
	// But link2 is NOT yet evicted from store history (grace period)
	if len(st.All()) != 2 {
		t.Fatalf("expected 2 candidates in All() during grace period, got %d", len(st.All()))
	}

	st.FinishCycle()
	// Cycle 1 finished: AbsentCycles committed to 1 <= 2. Not evicted yet.
	rec2, ok := st.GetRecord("link2")
	if !ok || rec2.AbsentCycles != 1 {
		t.Fatalf("expected link2 with AbsentCycles=1, got %+v", rec2)
	}
	if len(st.All()) != 2 {
		t.Fatalf("expected 2 candidates in All() after cycle 1, got %d", len(st.All()))
	}

	// Cycle 2: link2 still missing
	st.StartCycle(currentLinks)
	st.FinishCycle()
	// Cycle 2 finished: AbsentCycles committed to 2 <= 2. Not evicted yet.
	rec2, ok = st.GetRecord("link2")
	if !ok || rec2.AbsentCycles != 2 {
		t.Fatalf("expected link2 with AbsentCycles=2, got %+v", rec2)
	}
	if len(st.All()) != 2 {
		t.Fatalf("expected 2 candidates in All() after cycle 2, got %d", len(st.All()))
	}

	// Cycle 3: link2 still missing
	st.StartCycle(currentLinks)
	st.FinishCycle()
	// Cycle 3 finished: AbsentCycles committed to 3 > 2 -> Evicted!
	if len(st.All()) != 1 || st.All()[0].Link != "link1" {
		t.Fatalf("expected only link1 to remain after grace period exceeded, got %+v", st.All())
	}
}

func TestStore_PersistenceAndMigration(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "migrate_state.json")

	// Write old-format JSON (only passed: true/false, no status or category)
	oldJSON := `{
  "results": [
    {
      "link": "link-old-pass",
      "passed": true,
      "reason": "ok",
      "latency": 1000000000,
      "tested_at": "2026-09-02T18:00:00Z"
    },
    {
      "link": "link-old-fail",
      "passed": false,
      "reason": "request failed: unexpected HTTP response status: 429",
      "latency": 0,
      "tested_at": "2026-09-02T18:00:00Z"
    }
  ],
  "last_cycle": "2026-09-02T18:00:00Z",
  "cycle_count": 1
}`
	if err := os.WriteFile(stateFile, []byte(oldJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.New(stateFile, 2)
	if err := st.Load(); err != nil {
		t.Fatalf("failed to load old state: %v", err)
	}

	all := st.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 results, got %d", len(all))
	}

	var passResult, failResult store.Result
	for _, r := range all {
		if r.Link == "link-old-pass" {
			passResult = r
		} else if r.Link == "link-old-fail" {
			failResult = r
		}
	}

	if passResult.Status != store.StatusPassed || !passResult.Passed {
		t.Errorf("expected passResult to have StatusPassed and Passed=true, got %+v", passResult)
	}
	if failResult.Status != store.StatusFailed || failResult.Passed {
		t.Errorf("expected failResult to have StatusFailed and Passed=false, got %+v", failResult)
	}
	if failResult.Category != store.ErrProxyRateLimited {
		t.Errorf("expected derived category ErrProxyRateLimited, got %s", failResult.Category)
	}

	// Save and reload
	if err := st.Save(); err != nil {
		t.Fatalf("failed to save migrated state: %v", err)
	}

	st2 := store.New(stateFile, 2)
	if err := st2.Load(); err != nil {
		t.Fatalf("failed to reload saved state: %v", err)
	}
	if len(st2.Passing()) != 1 || st2.Passing()[0] != "link-old-pass" {
		t.Fatalf("expected link-old-pass in passing after reload, got %v", st2.Passing())
	}
}

func TestStore_Snapshots(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "test_state.json")

	st := store.New(stateFile, 2)
	linkPass := "vless://pass@1.1.1.1:443"
	linkFail := "vless://fail@2.2.2.2:443"

	st.PutWithTransition(store.Result{
		Link:     linkPass,
		Status:   store.StatusPassed,
		Reason:   "ok",
		Latency:  100 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st.PutWithTransition(store.Result{
		Link:     linkFail,
		Status:   store.StatusFailed,
		Reason:   "target region blocked",
		Category: store.ErrRegionBlocked,
		Latency:  50 * time.Millisecond,
		TestedAt: time.Now(),
	})

	snaps := st.Snapshots()
	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}

	for _, snap := range snaps {
		if snap.Record == nil {
			t.Fatal("expected non-nil record in snapshot")
		}
		if snap.Record.ActiveLink == linkPass {
			if !snap.Servable {
				t.Errorf("expected pass link to be servable, got false (gate: %s)", snap.Gate)
			}
			if snap.Gate != "Servable" {
				t.Errorf("expected gate 'Servable', got %q", snap.Gate)
			}
		} else if snap.Record.ActiveLink == linkFail {
			if snap.Servable {
				t.Errorf("expected fail link to not be servable")
			}
			if snap.Gate == "" || snap.Gate == "Servable" {
				t.Errorf("expected non-servable gate explanation, got %q", snap.Gate)
			}
		}
	}
}

func TestStore_Revision(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "test_state.json")

	st := store.New(stateFile, 2)
	rev0 := st.Revision()

	// Put increment
	st.PutWithTransition(store.Result{
		Link:     "vless://r1@1.1.1.1:443",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})
	rev1 := st.Revision()
	if rev1 <= rev0 {
		t.Errorf("expected revision to increment after Put, rev0=%d rev1=%d", rev0, rev1)
	}

	// StartCycle increment
	st.StartCycle(map[string]struct{}{"vless://r1@1.1.1.1:443": {}})
	rev2 := st.Revision()
	if rev2 <= rev1 {
		t.Errorf("expected revision to increment after StartCycle, rev1=%d rev2=%d", rev1, rev2)
	}

	// FinishCycle increment
	st.FinishCycle()
	rev3 := st.Revision()
	if rev3 <= rev2 {
		t.Errorf("expected revision to increment after FinishCycle, rev2=%d rev3=%d", rev2, rev3)
	}

	// Put wrapper increment
	st.Put(store.Result{
		Link:     "vless://r2@2.2.2.2:443",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})
	rev4 := st.Revision()
	if rev4 <= rev3 {
		t.Errorf("expected revision to increment after Put wrapper, rev3=%d rev4=%d", rev3, rev4)
	}

	// Save and Load increment
	if err := st.Save(); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}
	if err := st.Load(); err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	rev5 := st.Revision()
	if rev5 <= rev4 {
		t.Errorf("expected revision to increment after Load, rev4=%d rev5=%d", rev4, rev5)
	}
}
