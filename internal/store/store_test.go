package store_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestStore_Stats_GenericServableMatchesNetworkPassing(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	// 1. Initially empty
	stats := st.Stats()
	if stats.GenericServable != 0 || stats.Servable != 0 || len(st.NetworkPassing()) != 0 {
		t.Fatalf("expected 0 generic/servable on empty store, got %+v", stats)
	}

	// 2. Candidate 1: Transport OK + Gemini PASS -> both Generic and Gemini servable
	st.PutWithTransition(store.Result{
		Link:                   "vless://pass@1.1.1.1:443",
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})

	// 3. Candidate 2: Transport OK + Gemini RegionBlocked -> Generic servable, NOT Gemini servable
	st.PutWithTransition(store.Result{
		Link:                   "vmess://blocked@2.2.2.2:443",
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})

	// 4. Candidate 3: Transport Failure -> NEITHER Generic nor Gemini servable
	st.PutWithTransition(store.Result{
		Link:                   "trojan://dead@3.3.3.3:443",
		Status:                 store.StatusFailed,
		Category:               store.ErrTimeout,
		TransportEvidenceKnown: true,
		TransportOK:            false,
	})

	st.FinishCycle()

	stats = st.Stats()
	netPassing := st.NetworkPassing()
	gemPassing := st.Passing()

	if stats.GenericServable != len(netPassing) {
		t.Errorf("GenericServable (%d) != len(NetworkPassing()) (%d)", stats.GenericServable, len(netPassing))
	}
	if stats.GenericServable != 2 {
		t.Errorf("expected GenericServable == 2, got %d", stats.GenericServable)
	}
	if stats.Servable != len(gemPassing) {
		t.Errorf("Servable (%d) != len(Passing()) (%d)", stats.Servable, len(gemPassing))
	}
	if stats.Servable != 1 {
		t.Errorf("expected Servable == 1, got %d", stats.Servable)
	}
}

func TestPersistence_PrimaryPathAndLegacyFallback(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "state.json")
	primaryPath := basePath + ".gz"

	st := store.New(basePath, 2)
	link := "vless://primary-test@1.1.1.1:443#Primary"
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Latency:  50 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st.FinishCycle()

	// 1. Save must write only the primary compressed file (<path>.gz)
	if err := st.Save(); err != nil {
		t.Fatalf("st.Save() failed: %v", err)
	}

	if _, err := os.Stat(primaryPath); err != nil {
		t.Fatalf("expected primary file %s to exist: %v", primaryPath, err)
	}
	if _, err := os.Stat(basePath); !os.IsNotExist(err) {
		t.Fatalf("legacy file %s must NOT be created on compressed save", basePath)
	}

	// 2. Load finds <path>.gz
	st2 := store.New(basePath, 2)
	if err := st2.Load(); err != nil {
		t.Fatalf("st2.Load() failed: %v", err)
	}
	if len(st2.Passing()) != 1 || st2.Passing()[0] != link {
		t.Fatalf("reloaded store passing mismatch: %v", st2.Passing())
	}

	// 3. Fallback: delete primary .gz and write a legacy raw JSON at basePath
	if err := os.Remove(primaryPath); err != nil {
		t.Fatalf("failed to remove primary .gz: %v", err)
	}

	legacyContent := fmt.Sprintf(`{
		"version": 2,
		"cycle_count": 5,
		"last_cycle": "2026-09-01T12:00:00Z",
		"records": [
			{
				"canonical_link": "%s",
				"active_link": "%s",
				"absent_cycles": 0,
				"history": {
					"capacity": 10,
					"count": 1,
					"samples": [
						{"cycle_id": 5, "tested_at": "2026-09-01T12:00:00Z", "status": "passed", "latency": 60000000, "attempts": 1}
					]
				},
				"latest": {
					"link": "%s",
					"status": "passed",
					"latency": 60000000,
					"tested_at": "2026-09-01T12:00:00Z",
					"attempts": 1
				}
			}
		]
	}`, link, link, link)
	if err := os.WriteFile(basePath, []byte(legacyContent), 0o644); err != nil {
		t.Fatalf("failed to write legacy state: %v", err)
	}

	stFallback := store.New(basePath, 2)
	if err := stFallback.Load(); err != nil {
		t.Fatalf("fallback load failed: %v", err)
	}
	if len(stFallback.Passing()) != 1 || stFallback.Passing()[0] != link {
		t.Fatalf("fallback passing mismatch: %v", stFallback.Passing())
	}

	// 4. Save should now create primary .gz while leaving legacy basePath untouched
	if err := stFallback.Save(); err != nil {
		t.Fatalf("save after fallback failed: %v", err)
	}
	if _, err := os.Stat(primaryPath); err != nil {
		t.Fatalf("expected primary .gz created: %v", err)
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("legacy file must NOT be deleted automatically: %v", err)
	}
}

func TestPersistence_MagicBytesDetection(t *testing.T) {
	tmpDir := t.TempDir()

	// Case 1: Gzip data stored in a file WITHOUT .gz extension
	noExtPath := filepath.Join(tmpDir, "state_without_ext")
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	jsonPayload := `{
		"version": 2,
		"cycle_count": 1,
		"last_cycle": "2026-09-01T12:00:00Z",
		"records": [
			{
				"canonical_link": "vless://magic-test",
				"active_link": "vless://magic-test",
				"history": {"capacity": 10, "count": 1, "samples": [{"cycle_id": 1, "status": "passed"}]},
				"latest": {"status": "passed"}
			}
		]
	}`
	gw.Write([]byte(jsonPayload))
	gw.Close()

	if err := os.WriteFile(noExtPath, gzBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("write gzip without ext: %v", err)
	}

	st := store.New(noExtPath, 2)
	if err := st.Load(); err != nil {
		t.Fatalf("load gzip without ext failed: %v", err)
	}
	if len(st.Passing()) != 1 || st.Passing()[0] != "vless://magic-test" {
		t.Fatalf("unexpected passing: %v", st.Passing())
	}

	// Case 2: Raw JSON stored in a file WITH .gz extension (misnamed)
	misnamedPath := filepath.Join(tmpDir, "state_misnamed.gz")
	if err := os.WriteFile(misnamedPath, []byte(jsonPayload), 0o644); err != nil {
		t.Fatalf("write raw JSON in .gz: %v", err)
	}

	stMisnamed := store.New(misnamedPath, 2)
	if err := stMisnamed.Load(); err != nil {
		t.Fatalf("load raw JSON in .gz failed: %v", err)
	}
	if len(stMisnamed.Passing()) != 1 || stMisnamed.Passing()[0] != "vless://magic-test" {
		t.Fatalf("unexpected passing: %v", stMisnamed.Passing())
	}
}

func TestPersistence_ColdStartAndCorruptGzipHandling(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Missing files -> clean cold start (return nil)
	missingPath := filepath.Join(tmpDir, "nonexistent.json")
	stCold := store.New(missingPath, 2)
	if err := stCold.Load(); err != nil {
		t.Fatalf("cold start should succeed, got %v", err)
	}

	// 2. Corrupt .gz file must NOT silently fall back to legacy
	primaryPath := filepath.Join(tmpDir, "corrupt.json.gz")
	legacyPath := filepath.Join(tmpDir, "corrupt.json")

	// Create legacy valid file
	legacyValid := `{"version": 2, "cycle_count": 1, "records": [{"canonical_link": "vless://legacy", "active_link": "vless://legacy"}]}`
	if err := os.WriteFile(legacyPath, []byte(legacyValid), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	// Create truncated / corrupt gzip primary file (magic bytes present but truncated body)
	corruptGz := []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0x01, 0x02}
	if err := os.WriteFile(primaryPath, corruptGz, 0o644); err != nil {
		t.Fatalf("write corrupt gz: %v", err)
	}

	stCorrupt := store.New(legacyPath, 2)
	err := stCorrupt.Load()
	if err == nil {
		t.Fatalf("expected error when primary .gz is corrupt, but got nil (silent fallback must not occur)")
	}

	// 3. Completely invalid header and content
	invalidHeader := []byte("not-gzip-and-not-valid-json")
	if err := os.WriteFile(primaryPath, invalidHeader, 0o644); err != nil {
		t.Fatalf("write invalid header: %v", err)
	}
	if err := stCorrupt.Load(); err == nil {
		t.Fatalf("expected error on invalid header and malformed JSON, got nil")
	}
}

func TestPersistence_LinkDeduplication(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "dedup_state.json")

	linkSame := "vless://same-link@1.1.1.1:443#Same"
	linkDiffActive := "vless://diff-active@2.2.2.2:443#Active"
	linkDiffLatest := "vless://diff-latest@2.2.2.2:443#Latest"

	// Seed state file with two records: one where Latest.Link == ActiveLink, one where Latest.Link != ActiveLink
	seedJSON := fmt.Sprintf(`{
		"version": 2,
		"records": [
			{
				"canonical_link": "%s",
				"active_link": "%s",
				"history": {"capacity": 10, "count": 1, "samples": [{"status": "passed"}]},
				"latest": {"link": "%s", "status": "passed"}
			},
			{
				"canonical_link": "%s",
				"active_link": "%s",
				"history": {"capacity": 10, "count": 1, "samples": [{"status": "passed"}]},
				"latest": {"link": "%s", "status": "passed"}
			}
		]
	}`,
		store.CanonicalizeLink(linkSame), linkSame, linkSame,
		store.CanonicalizeLink(linkDiffActive), linkDiffActive, linkDiffLatest,
	)

	if err := os.WriteFile(stateFile, []byte(seedJSON), 0o644); err != nil {
		t.Fatalf("write seed JSON: %v", err)
	}

	st := store.New(stateFile, 2)
	if err := st.Load(); err != nil {
		t.Fatalf("load seed: %v", err)
	}

	if err := st.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Inspect the raw decompressed JSON to verify deduplication
	f, err := os.Open(st.PrimaryPath())
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	decompressed, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read decompressed: %v", err)
	}

	var rawDoc struct {
		Records []struct {
			ActiveLink string `json:"active_link"`
			Latest     struct {
				Link string `json:"link"`
			} `json:"latest"`
		} `json:"records"`
	}
	if err := json.Unmarshal(decompressed, &rawDoc); err != nil {
		t.Fatalf("unmarshal rawDoc: %v", err)
	}

	foundSame := false
	foundDiff := false
	for _, r := range rawDoc.Records {
		if r.ActiveLink == linkSame {
			foundSame = true
			if r.Latest.Link != "" {
				t.Errorf("expected Latest.Link to be omitted (empty in JSON) when equal to ActiveLink, got %q", r.Latest.Link)
			}
		}
		if r.ActiveLink == linkDiffActive {
			foundDiff = true
			if r.Latest.Link != linkDiffLatest {
				t.Errorf("expected Latest.Link %q preserved when differing from ActiveLink, got %q", linkDiffLatest, r.Latest.Link)
			}
		}
	}
	if !foundSame || !foundDiff {
		t.Fatalf("did not find both test records in rawDoc: foundSame=%v foundDiff=%v", foundSame, foundDiff)
	}

	// Reload into a fresh Store and confirm Latest.Link is properly restored
	stReloaded := store.New(stateFile, 2)
	if err := stReloaded.Load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	recSameReloaded, ok := stReloaded.GetRecord(linkSame)
	if !ok {
		t.Fatalf("same record not found after reload")
	}
	if recSameReloaded.Latest.Link != linkSame {
		t.Errorf("expected Latest.Link restored to %q, got %q", linkSame, recSameReloaded.Latest.Link)
	}

	recDiffReloaded, ok := stReloaded.GetRecord(linkDiffActive)
	if !ok {
		t.Fatalf("diff record not found after reload")
	}
	if recDiffReloaded.Latest.Link != linkDiffLatest {
		t.Errorf("expected Latest.Link preserved as %q, got %q", linkDiffLatest, recDiffReloaded.Latest.Link)
	}
}

func TestPersistence_DerivedFieldsOmissionAndReconstruction(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "derived_state.json")

	st := store.New(stateFile, 2)
	link := "vless://derived-test@1.1.1.1:443#Node"
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusPassed,
		Latency:  75 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st.FinishCycle()

	recOrig, _ := st.GetRecord(link)
	if recOrig.Score <= 0 || !recOrig.HasPassed || recOrig.LastPassedLatency != 75*time.Millisecond {
		t.Fatalf("invalid initial derived state: score=%f hasPassed=%v latency=%v", recOrig.Score, recOrig.HasPassed, recOrig.LastPassedLatency)
	}

	if err := st.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Inspect raw decompressed JSON: score, has_passed, last_passed_latency, passed should be omitted
	f, err := os.Open(st.PrimaryPath())
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	rawJSON, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read rawJSON: %v", err)
	}

	var rawDoc struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(rawJSON, &rawDoc); err != nil {
		t.Fatalf("unmarshal rawDoc: %v", err)
	}
	if len(rawDoc.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(rawDoc.Records))
	}
	recMap := rawDoc.Records[0]
	if _, exists := recMap["score"]; exists {
		t.Errorf("expected 'score' to be omitted from persistent payload, but found in JSON")
	}
	if _, exists := recMap["has_passed"]; exists {
		t.Errorf("expected 'has_passed' to be omitted from persistent payload, but found in JSON")
	}
	if _, exists := recMap["last_passed_latency"]; exists {
		t.Errorf("expected 'last_passed_latency' to be omitted from persistent payload, but found in JSON")
	}

	// Reload into a fresh store: derived fields MUST be authoritatively reconstructed
	st2 := store.New(stateFile, 2)
	if err := st2.Load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	rec2, ok := st2.GetRecord(link)
	if !ok {
		t.Fatalf("record not found after reload")
	}
	if rec2.Score != recOrig.Score {
		t.Errorf("reconstructed score %f != original %f", rec2.Score, recOrig.Score)
	}
	if !rec2.HasPassed {
		t.Errorf("expected HasPassed=true reconstructed from history")
	}
	if rec2.LastPassedLatency != 75*time.Millisecond {
		t.Errorf("expected LastPassedLatency=75ms, got %v", rec2.LastPassedLatency)
	}
	if !rec2.Latest.Passed {
		t.Errorf("expected Latest.Passed=true reconstructed from StatusPassed")
	}
}

func TestPersistence_AtomicCrashSafetyAndTempCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "atomic_state.json")

	st := store.New(stateFile, 2)
	link := "vless://atomic-test@1.1.1.1:443"
	st.PutWithTransition(store.Result{Link: link, Status: store.StatusPassed, TestedAt: time.Now()})
	st.FinishCycle()

	if err := st.Save(); err != nil {
		t.Fatalf("initial save failed: %v", err)
	}

	primaryPath := st.PrimaryPath()
	tmpPath := primaryPath + ".tmp"

	// Confirm primary exists and no lingering temp file remains
	if _, err := os.Stat(primaryPath); err != nil {
		t.Fatalf("primary missing: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("lingering temp file must not exist: %v", tmpPath)
	}

	// Capture modification time and content of initial primary
	origInfo, _ := os.Stat(primaryPath)
	origContent, _ := os.ReadFile(primaryPath)

	// Simulate failure: create a directory with tmpPath name to cause file create error
	if err := os.Mkdir(tmpPath, 0o755); err != nil {
		t.Fatalf("mkdir tmpPath: %v", err)
	}

	// Attempting Save will fail at os.OpenFile(tmp, ...) because tmp is a directory
	err := st.Save()
	if err == nil {
		t.Fatalf("expected Save to fail when tmp is an existing directory")
	}

	// Cleanup directory collision
	os.Remove(tmpPath)

	// Verify original primary state was NOT destroyed or truncated
	currentInfo, err := os.Stat(primaryPath)
	if err != nil {
		t.Fatalf("primary file missing after failed save: %v", err)
	}
	if currentInfo.Size() != origInfo.Size() {
		t.Errorf("primary file size changed after failed save: %d != %d", currentInfo.Size(), origInfo.Size())
	}
	currentContent, _ := os.ReadFile(primaryPath)
	if !bytes.Equal(currentContent, origContent) {
		t.Errorf("primary content altered after failed save")
	}
}

func TestPersistence_FixtureInspection(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "fixture_state.json")

	st := store.New(stateFile, 2)
	link := "vless://fixture-test@1.1.1.1:443#Fixture"
	now := time.Now().Truncate(time.Millisecond)

	st.PutWithTransition(store.Result{
		Link:                   link,
		Status:                 store.StatusPassed,
		Latency:                42 * time.Millisecond,
		TestedAt:               now,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       25 * time.Millisecond,
	})
	st.FinishCycle()

	if err := st.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Decompress and inspect JSON payload directly
	f, err := os.Open(st.PrimaryPath())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()

	payload, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	rawString := string(payload)

	// 1. Compact JSON (no multi-space indentation or newline padding)
	if strings.Contains(rawString, "  \"version\"") || strings.Contains(rawString, "    \"canonical_link\"") {
		t.Errorf("payload contains indent formatting whitespace: %s", rawString)
	}

	// 2. No dummy sample zero-timestamps
	if strings.Contains(rawString, "0001-01-01T00:00:00Z") {
		t.Errorf("payload contains zero-value dummy sample timestamp: %s", rawString)
	}

	// 3. Confirm 1 sample serialized instead of 10
	var snap store.Snapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(snap.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(snap.Records))
	}
	rec := snap.Records[0]
	if len(rec.History.Samples) != 1 {
		t.Fatalf("expected exactly 1 sample in serialized payload, got %d", len(rec.History.Samples))
	}
	if rec.History.Count != 1 {
		t.Fatalf("expected count 1, got %d", rec.History.Count)
	}

	// 4. Verify transport evidence is intact
	sample := rec.History.Samples[0]
	if !sample.TransportEvidenceKnown || !sample.TransportOK || sample.TransportLatency != 25*time.Millisecond {
		t.Errorf("transport evidence mismatch in sample: %+v", sample)
	}
}

func TestPersistence_MalformedJSON_Matrix(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{
			name: "trailing_comma_array",
			json: `{"version":2,"records":[{}],}`,
		},
		{
			name: "trailing_garbage",
			json: `{"version":2,"records":[{}]}garbage`,
		},
		{
			name: "truncated_array",
			json: `{"version":2,"records":[`,
		},
		{
			name: "truncated_object",
			json: `{"version":2,"records":[{`,
		},
		{
			name: "missing_comma_between_fields",
			json: `{"version":2 "records":[]}`,
		},
		{
			name: "missing_colon",
			json: `{"version":2,"records" []}`,
		},
		{
			name: "invalid_string_escape",
			json: "{\"version\":2,\"records\":[{\"canonical_link\":\"x\\q\"}]}",
		},
		{
			name: "unterminated_string",
			json: "{\"version\":2,\"records\":[{\"canonical_link\":\"unterminated}]}",
		},
		{
			name: "unescaped_control_character",
			json: "{\"version\":2,\"records\":[{\"canonical_link\":\"line1\nline2\"}]}",
		},
		{
			name: "trailing_comma_inside_records",
			json: `{"version":2,"records":[{"canonical_link":"x"},]}`,
		},
		{
			name: "missing_comma_inside_records",
			json: `{"version":2,"records":[{"canonical_link":"x"}{"canonical_link":"y"}]}`,
		},
		{
			name: "trailing_comma_inside_object",
			json: `{"version":2,"records":[{"canonical_link":"x",}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"_raw", func(t *testing.T) {
			dir := t.TempDir()
			rawPath := filepath.Join(dir, "state.json")
			if err := os.WriteFile(rawPath, []byte(tc.json), 0o644); err != nil {
				t.Fatalf("write raw: %v", err)
			}
			st := store.NewWithConfig(rawPath, store.DefaultScoringConfig())
			err := st.Load()
			if err == nil {
				t.Fatalf("expected error loading malformed JSON, got nil (payload: %s)", tc.json)
			}
		})

		t.Run(tc.name+"_gzip", func(t *testing.T) {
			dir := t.TempDir()
			rawPath := filepath.Join(dir, "state.json")
			gzPath := rawPath + ".gz"
			var buf bytes.Buffer
			gw := gzip.NewWriter(&buf)
			if _, err := gw.Write([]byte(tc.json)); err != nil {
				t.Fatalf("gzip write: %v", err)
			}
			if err := gw.Close(); err != nil {
				t.Fatalf("gzip close: %v", err)
			}
			if err := os.WriteFile(gzPath, buf.Bytes(), 0o644); err != nil {
				t.Fatalf("write gz: %v", err)
			}
			st := store.NewWithConfig(rawPath, store.DefaultScoringConfig())
			err := st.Load()
			if err == nil {
				t.Fatalf("expected error loading malformed gzip JSON, got nil (payload: %s)", tc.json)
			}
		})
	}
}

func TestPersistence_StringCompatibility_Matrix(t *testing.T) {
	// JSON string containing:
	// - ASCII string
	// - Persian text: "کانفیگ سرور ایران"
	// - Emoji surrogate pair: "\uD83D\uDE00" (which is 😀)
	// - Escaped characters: "quotes \" backslash \\ slash \/ newline \n"
	jsonPayload := `{
		"version": 2,
		"last_cycle": "2026-09-09T12:00:00Z",
		"cycle_count": 42,
		"records": [
			{
				"canonical_link": "vless://ascii@1.1.1.1:443#Persian_کانفیگ_سرور_ایران",
				"active_link": "vless://ascii@1.1.1.1:443#Emoji_\uD83D\uDE00",
				"latest": {
					"link": "vless://ascii@1.1.1.1:443#Emoji_\uD83D\uDE00",
					"status": "passed",
					"reason": "quotes \" backslash \\ slash \/ newline \n and \t tab",
					"warnings": ["warning 1: \uD83D\uDE00", "هشدار آزمایشی"]
				},
				"history": {
					"capacity": 10,
					"count": 1,
					"samples": [
						{
							"cycle_id": 42,
							"tested_at": "2026-09-09T12:00:00Z",
							"status": "passed",
							"category": "none",
							"attempts": 1
						}
					]
				}
			}
		]
	}`

	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	gzPath := path + ".gz"

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(jsonPayload)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(gzPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write gz: %v", err)
	}

	st := store.NewWithConfig(path, store.DefaultScoringConfig())
	if err := st.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	snaps := st.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 record, got %d", len(snaps))
	}
	rec := snaps[0].Record

	// Verify Persian text in CanonicalLink
	expectedCanonical := "vless://ascii@1.1.1.1:443#Persian_کانفیگ_سرور_ایران"
	if rec.CanonicalLink != expectedCanonical {
		t.Errorf("canonical link mismatch:\nexpected: %q\ngot:      %q", expectedCanonical, rec.CanonicalLink)
	}

	// Verify Emoji decoded from surrogate pair \uD83D\uDE00 -> 😀
	expectedActive := "vless://ascii@1.1.1.1:443#Emoji_😀"
	if rec.ActiveLink != expectedActive {
		t.Errorf("active link mismatch:\nexpected: %q\ngot:      %q", expectedActive, rec.ActiveLink)
	}

	// Verify escapes in reason
	expectedReason := "quotes \" backslash \\ slash / newline \n and \t tab"
	if rec.Latest.Reason != expectedReason {
		t.Errorf("reason mismatch:\nexpected: %q\ngot:      %q", expectedReason, rec.Latest.Reason)
	}

	// Verify warnings
	if len(rec.Latest.Warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(rec.Latest.Warnings))
	}
	if rec.Latest.Warnings[0] != "warning 1: 😀" {
		t.Errorf("warning 0 mismatch: %q", rec.Latest.Warnings[0])
	}
	if rec.Latest.Warnings[1] != "هشدار آزمایشی" {
		t.Errorf("warning 1 mismatch: %q", rec.Latest.Warnings[1])
	}
}

func TestPersistence_UnknownFields_Nesting(t *testing.T) {
	// Valid JSON with unknown scalar, string, array, and deeply nested objects
	validJSON := `{
		"version": 2,
		"unknown_root_scalar": 123.456e+2,
		"unknown_root_string": "hello world \"with quotes\"",
		"unknown_root_bool": true,
		"unknown_root_null": null,
		"unknown_root_array": [1, "two", {"three": [4, 5.5]}, false, null],
		"unknown_root_object": {
			"level1": {
				"level2": {
					"level3": {
						"val": "deep",
						"arr": [10, 20, 30]
					}
				}
			}
		},
		"last_cycle": "2026-09-09T12:00:00Z",
		"cycle_count": 1,
		"records": [
			{
				"canonical_link": "vless://unknown-fields@1.1.1.1:443",
				"active_link": "vless://unknown-fields@1.1.1.1:443",
				"unknown_record_field": {"foo": "bar", "num": -99.5},
				"latest": {
					"status": "passed",
					"latency": 15000000,
					"tested_at": "2026-09-09T12:00:00Z",
					"unknown_latest_meta": [true, false, null, "extra"]
				},
				"history": {
					"capacity": 10,
					"count": 1,
					"unknown_history_field": 42,
					"samples": [
						{
							"cycle_id": 1,
							"tested_at": "2026-09-09T12:00:00Z",
							"status": "passed",
							"unknown_sample_field": {"tag": 123}
						}
					]
				}
			}
		]
	}`

	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(validJSON), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	st := store.NewWithConfig(path, store.DefaultScoringConfig())
	if err := st.Load(); err != nil {
		t.Fatalf("failed to load valid JSON with unknown fields: %v", err)
	}

	snaps := st.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 record, got %d", len(snaps))
	}
	if snaps[0].Record.CanonicalLink != "vless://unknown-fields@1.1.1.1:443" {
		t.Errorf("record mismatch: %+v", snaps[0].Record)
	}

	// Malformed unknown nested object MUST fail!
	malformedJSON := `{
		"version": 2,
		"unknown_object": {
			"valid_key": "val",
			"broken_nested": {
				"missing_colon" "value"
			}
		},
		"records": []
	}`
	malformedPath := filepath.Join(dir, "malformed.json")
	if err := os.WriteFile(malformedPath, []byte(malformedJSON), 0o644); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	stMalformed := store.NewWithConfig(malformedPath, store.DefaultScoringConfig())
	if err := stMalformed.Load(); err == nil {
		t.Fatalf("expected error loading malformed unknown nested object, got nil")
	}
}

func TestPersistence_SerializerEquivalence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st := store.NewWithConfig(path, store.DefaultScoringConfig())

	baseTime := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	// Populate 100+ candidates with diverse characters, Unicode, errors, warnings, timestamps, latencies
	st.StartCycle(nil)
	for i := 0; i < 120; i++ {
		var status store.Status
		var cat store.ErrorCategory
		var reason string
		var warnings []string
		var transOK bool
		var transLat time.Duration

		switch i % 5 {
		case 0:
			status = store.StatusPassed
			cat = store.ErrNone
			transOK = true
			transLat = time.Duration(10+i) * time.Millisecond
		case 1:
			status = store.StatusFailed
			cat = store.ErrTimeout
			reason = "connection timed out after 5s: dial tcp 1.2.3.4:443"
			warnings = []string{"slow handshake", "retry 1 failed"}
			transOK = false
		case 2:
			status = store.StatusFailed
			cat = store.ErrRegionBlocked
			reason = "Google service regional block: 403 Forbidden (IR)"
			transOK = true
			transLat = 25 * time.Millisecond
		case 3:
			status = store.StatusInconclusive
			cat = store.ErrTargetRateLimited
			reason = "HTTP 429 Too Many Requests: quota exceeded"
			warnings = []string{"rate limit hit"}
			transOK = true
			transLat = 15 * time.Millisecond
		case 4:
			status = store.StatusFailed
			cat = store.ErrProxyError
			reason = fmt.Sprintf("quote: \"escaped\" and newline:\nand tab:\tand unicode: \u2713 and emoji: \U0001F600 (%d)", i)
			transOK = false
		}

		link := fmt.Sprintf("vless://user-%d@10.0.0.%d:443#کانفیگ_شماره_%d_😀", i, (i%250)+1, i)
		st.PutWithTransition(store.Result{
			Link:                   link,
			Status:                 status,
			Category:               cat,
			Reason:                 reason,
			Latency:                time.Duration(50+i) * time.Millisecond,
			TestedAt:               baseTime.Add(time.Duration(i) * time.Second),
			Attempts:               (i % 3) + 1,
			TransportEvidenceKnown: true,
			TransportOK:            transOK,
			TransportLatency:       transLat,
			Warnings:               warnings,
		})
	}
	st.FinishCycle()

	if err := st.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Read and decompress the custom-serialized payload
	f, err := os.Open(st.PrimaryPath())
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	customJSON, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read decompressed: %v", err)
	}

	// 1. Verify standard json.Unmarshal can parse custom JSON without error
	var snapFromCustom store.Snapshot
	if err := json.Unmarshal(customJSON, &snapFromCustom); err != nil {
		t.Fatalf("json.Unmarshal failed on custom JSON: %v", err)
	}

	// 2. Load into a fresh store instance (which uses fastDecodeSnapshot)
	st2 := store.NewWithConfig(path, store.DefaultScoringConfig())
	if err := st2.Load(); err != nil {
		t.Fatalf("st2.Load failed: %v", err)
	}

	// 3. Verify semantic equality between st and st2
	snapsOrig := st.Snapshots()
	snapsLoaded := st2.Snapshots()

	statsOrig := st.Stats()
	statsLoaded := st2.Stats()

	if statsOrig.Total != statsLoaded.Total {
		t.Errorf("total mismatch: %d vs %d", statsOrig.Total, statsLoaded.Total)
	}
	if len(snapsOrig) != len(snapsLoaded) {
		t.Fatalf("record count mismatch: %d vs %d", len(snapsOrig), len(snapsLoaded))
	}

	loadedMap := make(map[string]*store.CandidateRecord, len(snapsLoaded))
	for _, s := range snapsLoaded {
		loadedMap[s.Record.CanonicalLink] = s.Record
	}

	for i := range snapsOrig {
		orig := snapsOrig[i].Record
		loaded, ok := loadedMap[orig.CanonicalLink]
		if !ok {
			t.Fatalf("missing record %s in loaded store", orig.CanonicalLink)
		}

		if orig.ActiveLink != loaded.ActiveLink {
			t.Errorf("record %s ActiveLink mismatch: %q vs %q", orig.CanonicalLink, orig.ActiveLink, loaded.ActiveLink)
		}
		if orig.Latest.Status != loaded.Latest.Status {
			t.Errorf("record %s Status mismatch: %q vs %q", orig.CanonicalLink, orig.Latest.Status, loaded.Latest.Status)
		}
		if orig.Latest.Reason != loaded.Latest.Reason {
			t.Errorf("record %s Reason mismatch: %q vs %q", orig.CanonicalLink, orig.Latest.Reason, loaded.Latest.Reason)
		}
		if orig.Latest.Category != loaded.Latest.Category {
			t.Errorf("record %s Category mismatch: %q vs %q", orig.CanonicalLink, orig.Latest.Category, loaded.Latest.Category)
		}
		if orig.Latest.Latency != loaded.Latest.Latency {
			t.Errorf("record %s Latency mismatch: %v vs %v", orig.CanonicalLink, orig.Latest.Latency, loaded.Latest.Latency)
		}
		if orig.Latest.TransportOK != loaded.Latest.TransportOK {
			t.Errorf("record %s TransportOK mismatch: %v vs %v", orig.CanonicalLink, orig.Latest.TransportOK, loaded.Latest.TransportOK)
		}
		if orig.Latest.TransportLatency != loaded.Latest.TransportLatency {
			t.Errorf("record %s TransportLatency mismatch: %v vs %v", orig.CanonicalLink, orig.Latest.TransportLatency, loaded.Latest.TransportLatency)
		}
		if orig.Latest.TransportEvidenceKnown != loaded.Latest.TransportEvidenceKnown {
			t.Errorf("record %s TransportEvidenceKnown mismatch: %v vs %v", orig.CanonicalLink, orig.Latest.TransportEvidenceKnown, loaded.Latest.TransportEvidenceKnown)
		}
		if !slices.Equal(orig.Latest.Warnings, loaded.Latest.Warnings) {
			t.Errorf("record %s Warnings mismatch: %v vs %v", orig.CanonicalLink, orig.Latest.Warnings, loaded.Latest.Warnings)
		}
		if orig.History.Count != loaded.History.Count {
			t.Errorf("record %s History Count mismatch: %d vs %d", orig.CanonicalLink, orig.History.Count, loaded.History.Count)
		}
		if math.Abs(orig.Score-loaded.Score) > 1e-9 {
			t.Errorf("record %s Score mismatch: %f vs %f", orig.CanonicalLink, orig.Score, loaded.Score)
		}
	}
}

func TestPersistence_SaveSerializer_EdgeCases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st := store.NewWithConfig(path, store.DefaultScoringConfig())

	// Edge case 1: Persian remarks in links
	persianLink := "vless://user@1.2.3.4:443?security=reality#کانفیگ سرور ایران پرسرعت (تست ۱)"
	// Edge case 2: Emojis in remarks and active link
	emojiLink := "vmess://user@5.6.7.8:443#🚀_Fast_Node_⚡_IR_🇮🇷"
	// Edge case 3: Multiline error messages or warnings, quotes, slashes, backslashes, control chars
	multilineReason := "Line 1: dial failed\r\nLine 2: connection reset by peer \"quote\" and path /api/v1\\sub\twith tabs"
	multilineWarnings := []string{
		"Warning\nwith\nnewlines",
		"Unicode: \u26A0\uFE0F Warning with emoji",
		"Tabs\tand\tquotes: \"test\"",
	}

	now := time.Now().Truncate(time.Millisecond)

	st.StartCycle(nil)
	st.PutWithTransition(store.Result{
		Link:                   persianLink,
		Status:                 store.StatusPassed,
		Latency:                45 * time.Millisecond,
		TestedAt:               now,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       30 * time.Millisecond,
	})
	st.PutWithTransition(store.Result{
		Link:                   emojiLink,
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		Reason:                 multilineReason,
		Warnings:               multilineWarnings,
		Latency:                120 * time.Millisecond,
		TestedAt:               now.Add(time.Second),
		TransportEvidenceKnown: true,
		TransportOK:            false,
	})
	st.FinishCycle()

	if err := st.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reload in fresh store
	st2 := store.NewWithConfig(path, store.DefaultScoringConfig())
	if err := st2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	snaps := st2.Snapshots()
	if len(snaps) != 2 {
		t.Fatalf("expected 2 records, got %d", len(snaps))
	}

	// Find the records
	var recPersian, recEmoji *store.CandidateRecord
	for _, s := range snaps {
		r := s.Record
		if strings.Contains(r.CanonicalLink, "1.2.3.4") {
			recPersian = r
		} else if strings.Contains(r.CanonicalLink, "5.6.7.8") {
			recEmoji = r
		}
	}

	if recPersian == nil || recEmoji == nil {
		t.Fatalf("missing records in loaded state: %+v", snaps)
	}

	// Verify exact byte preservation of Persian remark
	if recPersian.ActiveLink != persianLink {
		t.Errorf("persian active link mismatch:\nexpected: %q\ngot:      %q", persianLink, recPersian.ActiveLink)
	}

	// Verify exact byte preservation of Emoji remark
	if recEmoji.ActiveLink != emojiLink {
		t.Errorf("emoji active link mismatch:\nexpected: %q\ngot:      %q", emojiLink, recEmoji.ActiveLink)
	}

	// Verify multiline reason
	if recEmoji.Latest.Reason != multilineReason {
		t.Errorf("multiline reason mismatch:\nexpected: %q\ngot:      %q", multilineReason, recEmoji.Latest.Reason)
	}

	// Verify multiline warnings
	if !slices.Equal(recEmoji.Latest.Warnings, multilineWarnings) {
		t.Errorf("multiline warnings mismatch:\nexpected: %v\ngot:      %v", multilineWarnings, recEmoji.Latest.Warnings)
	}
}

func TestStore_Count(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "count_state.json")
	st := store.New(stateFile, 2)

	// 1. Empty store
	if got := st.Count(); got != 0 {
		t.Fatalf("expected Count() on empty store to be 0, got %d", got)
	}
	if got, total := st.Count(), st.Stats().Total; got != total {
		t.Fatalf("Count() (%d) != Stats().Total (%d) on empty store", got, total)
	}

	// 2. Inserts
	st.PutWithTransition(store.Result{
		Link:     "vless://user1@1.1.1.1:443",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})
	st.PutWithTransition(store.Result{
		Link:     "vless://user2@2.2.2.2:443",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})

	if got := st.Count(); got != 2 {
		t.Fatalf("expected Count() to be 2 after 2 inserts, got %d", got)
	}
	if got, total := st.Count(), st.Stats().Total; got != total {
		t.Fatalf("Count() (%d) != Stats().Total (%d) after inserts", got, total)
	}
	if got, snaps := st.Count(), len(st.Snapshots()); got != snaps {
		t.Fatalf("Count() (%d) != len(Snapshots()) (%d)", got, snaps)
	}

	// 3. Update existing candidate does not change count
	st.PutWithTransition(store.Result{
		Link:     "vless://user1@1.1.1.1:443",
		Status:   store.StatusFailed,
		TestedAt: time.Now(),
	})
	if got := st.Count(); got != 2 {
		t.Fatalf("expected Count() to remain 2 after update, got %d", got)
	}

	// 4. Removals / Evictions via absent cycles
	activeLinks := map[string]struct{}{
		"vless://user1@1.1.1.1:443": {},
	}
	// Cycle 1: user2 missing
	st.StartCycle(activeLinks)
	st.FinishCycle()
	if got := st.Count(); got != 2 {
		t.Fatalf("expected Count() to remain 2 during absent grace period, got %d", got)
	}
	// Cycle 2: user2 missing
	st.StartCycle(activeLinks)
	st.FinishCycle()
	if got := st.Count(); got != 2 {
		t.Fatalf("expected Count() to remain 2 during absent grace period, got %d", got)
	}
	// Cycle 3: user2 missing (exceeds MaxAbsentCycles=2 -> evicted)
	st.StartCycle(activeLinks)
	st.FinishCycle()
	if got := st.Count(); got != 1 {
		t.Fatalf("expected Count() to be 1 after eviction, got %d", got)
	}
	if got, total := st.Count(), st.Stats().Total; got != total {
		t.Fatalf("Count() (%d) != Stats().Total (%d) after eviction", got, total)
	}
}

func TestStore_CycleCount(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "cycle_count_state.json")
	st := store.New(stateFile, 2)

	// 1. Initial state
	if got := st.CycleCount(); got != 0 {
		t.Fatalf("expected initial CycleCount 0, got %d", got)
	}
	if got, want := st.CycleCount(), st.Stats().CycleCount; got != want {
		t.Fatalf("CycleCount() (%d) != Stats().CycleCount (%d)", got, want)
	}

	// 2. Increment on FinishCycle
	st.StartCycle(map[string]struct{}{})
	st.FinishCycle()

	if got := st.CycleCount(); got != 1 {
		t.Fatalf("expected CycleCount 1 after FinishCycle, got %d", got)
	}
	if got, want := st.CycleCount(), st.Stats().CycleCount; got != want {
		t.Fatalf("CycleCount() (%d) != Stats().CycleCount (%d)", got, want)
	}

	// 3. Increment again
	st.StartCycle(map[string]struct{}{})
	st.FinishCycle()

	if got := st.CycleCount(); got != 2 {
		t.Fatalf("expected CycleCount 2 after second FinishCycle, got %d", got)
	}

	// 4. Persistence round-trip retains CycleCount
	if err := st.Save(); err != nil {
		t.Fatalf("failed to save store: %v", err)
	}

	stLoaded := store.New(stateFile, 2)
	if err := stLoaded.Load(); err != nil {
		t.Fatalf("failed to load store: %v", err)
	}
	if got := stLoaded.CycleCount(); got != 2 {
		t.Fatalf("expected loaded CycleCount 2, got %d", got)
	}
}

func TestStore_LastCycle(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "last_cycle_state.json")
	st := store.New(stateFile, 2)

	// 1. Initial state: zero time
	if got := st.LastCycle(); !got.IsZero() {
		t.Fatalf("expected initial LastCycle to be zero time, got %v", got)
	}
	if got, want := st.LastCycle(), st.Stats().LastCycle; !got.Equal(want) {
		t.Fatalf("LastCycle() (%v) != Stats().LastCycle (%v)", got, want)
	}

	// 2. Updated on FinishCycle
	before := time.Now().Add(-time.Second)
	st.StartCycle(map[string]struct{}{})
	st.FinishCycle()
	after := time.Now().Add(time.Second)

	got := st.LastCycle()
	if got.IsZero() {
		t.Fatal("expected LastCycle to be set after FinishCycle")
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("LastCycle (%v) outside expected range [%v, %v]", got, before, after)
	}
	if !got.Equal(st.Stats().LastCycle) {
		t.Fatalf("LastCycle() (%v) != Stats().LastCycle (%v)", got, st.Stats().LastCycle)
	}
}
