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

	st := store.New(stateFile, 2) // max 2 inconclusive cycles

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

	// 2. Cycle 1 Inconclusive -> Retain last-known-good (Passed=true)
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
	if !r.Passed {
		t.Errorf("expected Passed=true (last-known-good retained on cycle 1)")
	}
	if r.ConsecutiveInconclusive != 1 {
		t.Errorf("expected ConsecutiveInconclusive=1, got %d", r.ConsecutiveInconclusive)
	}
	if len(st.Passing()) != 1 {
		t.Errorf("expected link to remain in Passing() list")
	}

	// 3. Cycle 2 Inconclusive -> Retain last-known-good (Passed=true)
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusInconclusive,
		Reason:   "timeout",
		TestedAt: time.Now(),
	})
	results = st.All()
	r = results[0]
	if !r.Passed || r.ConsecutiveInconclusive != 2 {
		t.Errorf("expected Passed=true, ConsecutiveInconclusive=2; got passed=%v, count=%d", r.Passed, r.ConsecutiveInconclusive)
	}

	// 4. Cycle 3 Inconclusive -> Exceeds maxInconclusiveCycles (2), so Passed=false
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusInconclusive,
		Reason:   "timeout",
		TestedAt: time.Now(),
	})
	results = st.All()
	r = results[0]
	if r.Passed {
		t.Errorf("expected Passed=false after exceeding max inconclusive cycles")
	}
	if len(st.Passing()) != 0 {
		t.Errorf("expected link to be removed from Passing() list")
	}

	// 5. Restore: PASS -> StatusPassed, Passed=true, consecutive reset
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Reason:   "ok",
		TestedAt: time.Now(),
	})
	results = st.All()
	r = results[0]
	if r.Status != store.StatusPassed || !r.Passed || r.ConsecutiveInconclusive != 0 {
		t.Errorf("expected restored PASS, got %+v", r)
	}

	// 6. Confirmed Failure -> PASS to FAILED: Immediate eviction
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Reason:   "target returned regional restriction message",
		TestedAt: time.Now(),
	})
	results = st.All()
	r = results[0]
	if r.Status != store.StatusFailed || r.Passed {
		t.Errorf("expected immediate failure eviction, got %+v", r)
	}
	if len(st.Passing()) != 0 {
		t.Errorf("expected Passing() to be empty after confirmed failure")
	}
}

func TestStore_StartCyclePruning(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "prune_state.json")
	st := store.New(stateFile, 2)

	st.PutWithTransition(store.Result{Link: "link1", Status: store.StatusPassed})
	st.PutWithTransition(store.Result{Link: "link2", Status: store.StatusPassed})

	currentLinks := map[string]struct{}{
		"link1": {},
	}
	st.StartCycle(currentLinks)

	all := st.All()
	if len(all) != 1 || all[0].Link != "link1" {
		t.Fatalf("expected only link1 to remain after StartCycle, got %+v", all)
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
		t.Fatalf("expected link-old-pass in passing after reload")
	}
}
