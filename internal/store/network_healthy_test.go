package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gemsub/internal/store"
)

func TestErrorCategory_Classification(t *testing.T) {
	// Target-specific error categories (transport succeeded, target application rejected)
	targetSpecific := []store.ErrorCategory{
		store.ErrRegionBlocked,
		store.ErrTargetDenied,
	}
	for _, cat := range targetSpecific {
		if !cat.IsTargetSpecific() {
			t.Errorf("expected %s to be target-specific", cat)
		}
	}

	// Transport/network failure categories
	networkFailures := []store.ErrorCategory{
		store.ErrConnRefused,
		store.ErrTimeout,
		store.ErrReset,
		store.ErrTLS,
		store.ErrReality,
		store.ErrProxyError,
		store.ErrConfig,
	}
	for _, cat := range networkFailures {
		if cat.IsTargetSpecific() {
			t.Errorf("expected %s NOT to be target-specific", cat)
		}
	}

	// ErrNone indicates full pass, not an error category
	if store.ErrNone.IsTargetSpecific() {
		t.Errorf("expected ErrNone NOT to be target-specific")
	}
}

func TestBoundedHistory_LastNetworkHealthyLatency(t *testing.T) {
	// 1. Empty history returns (0, false)
	h1 := store.NewBoundedHistory(5)
	if lat, ok := h1.LastNetworkHealthyLatency(); ok || lat != 0 {
		t.Fatalf("expected (0, false) for empty history, got (%v, %v)", lat, ok)
	}

	// 2. Only transport connection failures return (0, false)
	h1.Push(store.ProbeSample{
		Status:   store.StatusFailed,
		Category: store.ErrConnRefused,
		Latency:  50 * time.Millisecond,
	})
	if lat, ok := h1.LastNetworkHealthyLatency(); ok || lat != 0 {
		t.Fatalf("expected (0, false) for conn refused history, got (%v, %v)", lat, ok)
	}

	// 3. Target-specific failure with measured latency returns latency
	h2 := store.NewBoundedHistory(5)
	h2.Push(store.ProbeSample{
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  120 * time.Millisecond,
	})
	if lat, ok := h2.LastNetworkHealthyLatency(); !ok || lat != 120*time.Millisecond {
		t.Fatalf("expected (120ms, true), got (%v, %v)", lat, ok)
	}

	// 4. Trailing timeout does not erase prior healthy transport latency
	h2.Push(store.ProbeSample{
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
		Latency:  2000 * time.Millisecond,
	})
	if lat, ok := h2.LastNetworkHealthyLatency(); !ok || lat != 120*time.Millisecond {
		t.Fatalf("expected (120ms, true) after trailing timeout, got (%v, %v)", lat, ok)
	}

	// 5. Subsequent conclusive transport failure (ErrConnRefused) invalidates prior healthy latency
	h3 := store.NewBoundedHistory(5)
	h3.Push(store.ProbeSample{
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  120 * time.Millisecond,
	})
	h3.Push(store.ProbeSample{
		Status:   store.StatusFailed,
		Category: store.ErrConnRefused,
		Latency:  15 * time.Millisecond,
	})
	if lat, ok := h3.LastNetworkHealthyLatency(); ok || lat != 0 {
		t.Fatalf("expected (0, false) when ErrRegionBlocked is followed by ErrConnRefused, got (%v, %v)", lat, ok)
	}

	// 6. Subsequent conclusive transport failure (ErrTLS) invalidates prior healthy latency
	h4 := store.NewBoundedHistory(5)
	h4.Push(store.ProbeSample{
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  120 * time.Millisecond,
	})
	h4.Push(store.ProbeSample{
		Status:   store.StatusFailed,
		Category: store.ErrTLS,
		Latency:  25 * time.Millisecond,
	})
	if lat, ok := h4.LastNetworkHealthyLatency(); ok || lat != 0 {
		t.Fatalf("expected (0, false) when ErrRegionBlocked is followed by ErrTLS, got (%v, %v)", lat, ok)
	}

	// 7. Subsequent conclusive transport failure invalidates prior StatusPassed latency
	h5 := store.NewBoundedHistory(5)
	h5.Push(store.ProbeSample{
		Status:   store.StatusPassed,
		Category: store.ErrNone,
		Latency:  80 * time.Millisecond,
	})
	h5.Push(store.ProbeSample{
		Status:   store.StatusFailed,
		Category: store.ErrConnRefused,
		Latency:  10 * time.Millisecond,
	})
	if lat, ok := h5.LastNetworkHealthyLatency(); ok || lat != 0 {
		t.Fatalf("expected (0, false) when StatusPassed is followed by ErrConnRefused, got (%v, %v)", lat, ok)
	}

	// 8. Multiple target-specific samples return the latest applicable latency
	h6 := store.NewBoundedHistory(5)
	h6.Push(store.ProbeSample{
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  150 * time.Millisecond,
	})
	h6.Push(store.ProbeSample{
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  80 * time.Millisecond,
	})
	if lat, ok := h6.LastNetworkHealthyLatency(); !ok || lat != 80*time.Millisecond {
		t.Fatalf("expected latest latency (80ms, true), got (%v, %v)", lat, ok)
	}
}

func TestStore_NetworkPassing_PreservesTargetIncompatibleCandidates(t *testing.T) {
	st := store.NewWithConfig("", store.DefaultScoringConfig())

	linkPassed := "vless://gemini-pass"
	linkBlocked := "vless://gemini-region-blocked"
	linkDenied := "vless://gemini-target-denied"
	linkRefused := "vless://transport-conn-refused"
	linkTimeout := "vless://transport-timeout"

	st.PutWithTransition(store.Result{
		Link:    linkPassed,
		Status:  store.StatusPassed,
		Latency: 100 * time.Millisecond,
	})
	st.PutWithTransition(store.Result{
		Link:     linkBlocked,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Reason:   "Country IR not supported",
		Latency:  150 * time.Millisecond,
	})
	st.PutWithTransition(store.Result{
		Link:     linkDenied,
		Status:   store.StatusFailed,
		Category: store.ErrTargetDenied,
		Reason:   "HTTP 403 Forbidden",
		Latency:  120 * time.Millisecond,
	})
	st.PutWithTransition(store.Result{
		Link:     linkRefused,
		Status:   store.StatusFailed,
		Category: store.ErrConnRefused,
		Reason:   "connection refused",
		Latency:  10 * time.Millisecond,
	})
	st.PutWithTransition(store.Result{
		Link:     linkTimeout,
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
		Reason:   "i/o timeout",
		Latency:  2000 * time.Millisecond,
	})

	// 1. Verify Gemini-servable Passing() remains strictly Gemini-specific
	passing := st.Passing()
	if len(passing) != 1 || passing[0] != linkPassed {
		t.Fatalf("expected only %s in Passing(), got %v", linkPassed, passing)
	}

	// 2. Verify NetworkPassing() includes candidates with proven transport
	networkPassing := st.NetworkPassing()
	if len(networkPassing) != 3 {
		t.Fatalf("expected 3 candidates in NetworkPassing(), got %d: %v", len(networkPassing), networkPassing)
	}
	expected := []string{linkPassed, linkBlocked, linkDenied}
	for i, want := range expected {
		if networkPassing[i] != want {
			t.Errorf("networkPassing[%d] = %s, want %s", i, networkPassing[i], want)
		}
	}

	// 3. Verify individual record helpers
	recBlocked, _ := st.GetRecord(linkBlocked)
	if !st.IsNetworkHealthyRecord(recBlocked) {
		t.Errorf("expected linkBlocked to be network healthy")
	}
	if st.IsServableRecord(recBlocked) {
		t.Errorf("expected linkBlocked NOT to be Gemini servable")
	}

	recRefused, _ := st.GetRecord(linkRefused)
	if st.IsNetworkHealthyRecord(recRefused) {
		t.Errorf("expected linkRefused NOT to be network healthy")
	}
}

func TestStore_NetworkPassingRanked_RankingPrecedenceAndTier2ScoreIsolation(t *testing.T) {
	st := store.NewWithConfig("", store.DefaultScoringConfig())

	linkPassA := "vless://pass-a-high-score"
	linkPassB := "vless://pass-b-lower-score"
	linkBlockedClean := "vless://blocked-clean"
	linkBlockedFlaky := "vless://blocked-flaky-higher-gemini-score"
	linkBlockedFast := "vless://blocked-fast"

	// Tier 1: Gemini-servable candidates
	// Candidate Pass A: 2 passes -> score 1.0, latency 100ms
	st.PutWithTransition(store.Result{Link: linkPassA, Status: store.StatusPassed, Latency: 100 * time.Millisecond})
	st.PutWithTransition(store.Result{Link: linkPassA, Status: store.StatusPassed, Latency: 100 * time.Millisecond})

	// Candidate Pass B: 1 pass, 1 inconclusive -> score ~0.657, latency 80ms
	st.PutWithTransition(store.Result{Link: linkPassB, Status: store.StatusPassed, Latency: 80 * time.Millisecond})
	st.PutWithTransition(store.Result{Link: linkPassB, Status: store.StatusInconclusive, Latency: 200 * time.Millisecond})

	// Tier 2: Target-incompatible candidates
	// Candidate Clean: 1 probe with ErrRegionBlocked (150ms). Gemini rec.Score = 0.0
	st.PutWithTransition(store.Result{
		Link:     linkBlockedClean,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  150 * time.Millisecond,
	})

	// Candidate Flaky: 1 timeout, then 1 ErrRegionBlocked (250ms).
	// Because ErrTimeout weight is 0.4 while ErrRegionBlocked is 0.0,
	// its Gemini rec.Score is > 0.0 (~0.173)!
	st.PutWithTransition(store.Result{
		Link:     linkBlockedFlaky,
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
		Latency:  2000 * time.Millisecond,
	})
	st.PutWithTransition(store.Result{
		Link:     linkBlockedFlaky,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  250 * time.Millisecond,
	})

	// Candidate Fast: ErrTargetDenied with 50ms latency. Gemini rec.Score = 0.0
	st.PutWithTransition(store.Result{
		Link:     linkBlockedFast,
		Status:   store.StatusFailed,
		Category: store.ErrTargetDenied,
		Latency:  50 * time.Millisecond,
	})

	// Verify PassingRanked only includes Gemini-servable candidates
	geminiRanked := st.PassingRanked()
	if len(geminiRanked) != 2 || geminiRanked[0] != linkPassA || geminiRanked[1] != linkPassB {
		t.Fatalf("expected [%s, %s] in PassingRanked(), got %v", linkPassA, linkPassB, geminiRanked)
	}

	// Verify NetworkPassingRanked ranking order:
	// Tier 1: Gemini-servable candidates (Pass A [score 1.0], then Pass B [score 0.657])
	// Tier 2: Target-incompatible candidates ranked strictly by network latency ascending:
	//         1st: Blocked Fast (50ms)
	//         2nd: Blocked Clean (150ms)
	//         3rd: Blocked Flaky (250ms) -- MUST NOT outrank Clean despite having higher Gemini score!
	netRanked := st.NetworkPassingRanked()
	if len(netRanked) != 5 {
		t.Fatalf("expected 5 candidates in NetworkPassingRanked(), got %d: %v", len(netRanked), netRanked)
	}

	expectedOrder := []string{
		linkPassA,        // Tier 1: Servable, higher score
		linkPassB,        // Tier 1: Servable, lower score
		linkBlockedFast,  // Tier 2: Non-servable, latency 50ms
		linkBlockedClean, // Tier 2: Non-servable, latency 150ms
		linkBlockedFlaky, // Tier 2: Non-servable, latency 250ms (cleanly defeated by 150ms)
	}

	for i, want := range expectedOrder {
		if netRanked[i] != want {
			t.Errorf("netRanked[%d] = %s, want %s", i, netRanked[i], want)
		}
	}
}

func TestStore_NetworkPassing_LaterConclusiveFailureInvalidatesHealth(t *testing.T) {
	cfg := store.DefaultScoringConfig()
	st := store.NewWithConfig("", cfg)
	link := "vless://node-invalidation"

	// 1. Initial test: ErrRegionBlocked -> candidate is network-healthy
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  100 * time.Millisecond,
	})
	if len(st.NetworkPassing()) != 1 {
		t.Fatalf("expected 1 candidate in NetworkPassing initially")
	}

	// 2. Trailing inconclusive timeouts do NOT erase the latest conclusive health
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
		Latency:  2000 * time.Millisecond,
	})
	if len(st.NetworkPassing()) != 1 {
		t.Fatalf("expected candidate to remain network-healthy after inconclusive timeout")
	}

	// 3. Conclusive transport failure (ErrConnRefused) invalidates network health
	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusFailed,
		Category: store.ErrConnRefused,
		Latency:  15 * time.Millisecond,
	})
	if len(st.NetworkPassing()) != 0 {
		t.Fatalf("expected 0 in NetworkPassing after subsequent ErrConnRefused, got %v", st.NetworkPassing())
	}
	if len(st.NetworkPassingRanked()) != 0 {
		t.Fatalf("expected 0 in NetworkPassingRanked after subsequent ErrConnRefused, got %v", st.NetworkPassingRanked())
	}

	// 4. Conclusive transport failure (ErrTLS) also invalidates
	st2 := store.NewWithConfig("", cfg)
	st2.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  100 * time.Millisecond,
	})
	st2.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusFailed,
		Category: store.ErrTLS,
		Latency:  20 * time.Millisecond,
	})
	if len(st2.NetworkPassing()) != 0 {
		t.Fatalf("expected 0 in NetworkPassing after subsequent ErrTLS, got %v", st2.NetworkPassing())
	}
}

func TestStore_NetworkPassing_PresenceGates(t *testing.T) {
	cfg := store.DefaultScoringConfig()
	st := store.NewWithConfig("", cfg)
	link := "vless://node-presence"

	st.PutWithTransition(store.Result{
		Link:     link,
		Status:   store.StatusFailed,
		Category: store.ErrRegionBlocked,
		Latency:  100 * time.Millisecond,
	})
	if len(st.NetworkPassing()) != 1 {
		t.Fatalf("expected 1 in NetworkPassing initially")
	}

	// Upstream absence in active cycle (pendingAbsent) disqualifies candidate from NetworkPassing
	st.StartCycle(map[string]struct{}{"vless://other-node": {}})
	if len(st.NetworkPassing()) != 0 {
		t.Fatalf("expected 0 in NetworkPassing when candidate is marked pending-absent in active cycle")
	}

	// Finish cycle commits absence
	st.FinishCycle()
	if len(st.NetworkPassing()) != 0 {
		t.Fatalf("expected 0 in NetworkPassing when candidate has AbsentCycles > 0 and not in active cycle")
	}

	// Reappearance restores candidate
	st.StartCycle(map[string]struct{}{link: {}})
	if len(st.NetworkPassing()) != 1 {
		t.Fatalf("expected candidate to regain network-healthy status upon reappearance in source set")
	}
}

func TestStore_CandidateSnapshot_NetworkHealthy(t *testing.T) {
	st := store.NewWithConfig("", store.DefaultScoringConfig())
	linkPass := "vless://pass"
	linkBlocked := "vless://blocked"
	linkFailed := "vless://failed"

	st.PutWithTransition(store.Result{Link: linkPass, Status: store.StatusPassed, Latency: 50 * time.Millisecond})
	st.PutWithTransition(store.Result{Link: linkBlocked, Status: store.StatusFailed, Category: store.ErrRegionBlocked, Latency: 60 * time.Millisecond})
	st.PutWithTransition(store.Result{Link: linkFailed, Status: store.StatusFailed, Category: store.ErrConnRefused, Latency: 10 * time.Millisecond})

	snaps := st.Snapshots()
	if len(snaps) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(snaps))
	}

	for _, snap := range snaps {
		switch snap.Record.ActiveLink {
		case linkPass:
			if !snap.Servable || !snap.NetworkHealthy {
				t.Errorf("linkPass: Servable=%v, NetworkHealthy=%v; want true, true", snap.Servable, snap.NetworkHealthy)
			}
		case linkBlocked:
			if snap.Servable || !snap.NetworkHealthy {
				t.Errorf("linkBlocked: Servable=%v, NetworkHealthy=%v; want false, true", snap.Servable, snap.NetworkHealthy)
			}
			if !strings.Contains(snap.Gate, "Target policy") {
				t.Errorf("linkBlocked gate expected Target policy, got %s", snap.Gate)
			}
		case linkFailed:
			if snap.Servable || snap.NetworkHealthy {
				t.Errorf("linkFailed: Servable=%v, NetworkHealthy=%v; want false, false", snap.Servable, snap.NetworkHealthy)
			}
		}
	}
}

func TestStore_PersistenceCompatibility_NetworkPassing(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "gemsub_state.json")

	st := store.NewWithConfig(statePath, store.DefaultScoringConfig())
	linkPass := "vless://pass"
	linkBlocked := "vless://blocked"

	st.PutWithTransition(store.Result{Link: linkPass, Status: store.StatusPassed, Latency: 50 * time.Millisecond})
	st.PutWithTransition(store.Result{Link: linkBlocked, Status: store.StatusFailed, Category: store.ErrRegionBlocked, Latency: 70 * time.Millisecond})

	if err := st.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Ensure saved file is valid
	if _, err := os.Stat(st.PrimaryPath()); err != nil {
		t.Fatalf("state file not created: %v", err)
	}

	// Reload in a fresh store instance
	stReloaded := store.NewWithConfig(statePath, store.DefaultScoringConfig())

	if err := stReloaded.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify reload preserves Gemini Passing() vs NetworkPassing()
	passing := stReloaded.Passing()
	if len(passing) != 1 || passing[0] != linkPass {
		t.Fatalf("reloaded Passing() = %v, want [%s]", passing, linkPass)
	}

	netPassing := stReloaded.NetworkPassing()
	if len(netPassing) != 2 {
		t.Fatalf("reloaded NetworkPassing() count = %d, want 2", len(netPassing))
	}

	netRanked := stReloaded.NetworkPassingRanked()
	if len(netRanked) != 2 || netRanked[0] != linkPass || netRanked[1] != linkBlocked {
		t.Fatalf("reloaded NetworkPassingRanked() = %v, want [%s, %s]", netRanked, linkPass, linkBlocked)
	}
}

func TestStore_NetworkHealthyAndServabilityEquivalence_Matrix(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.NewWithConfig(filepath.Join(tmpDir, "state.json"), store.DefaultScoringConfig())

	// 1. Target blocked: transport OK, but target region blocked
	lBlocked := "vless://blocked@1.1.1.1:443#Blocked"
	st.PutWithTransition(store.Result{
		Link:                   lBlocked,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportOK:            true,
		TransportEvidenceKnown: true,
	})

	// 2. Target denied: transport OK, but target denied
	lDenied := "vless://denied@1.1.1.2:443#Denied"
	st.PutWithTransition(store.Result{
		Link:                   lDenied,
		Status:                 store.StatusFailed,
		Category:               store.ErrTargetDenied,
		TransportOK:            true,
		TransportEvidenceKnown: true,
	})

	// 3. Score below threshold: transport failed, score low
	lLowScore := "vless://lowscore@1.1.1.3:443#LowScore"
	st.PutWithTransition(store.Result{
		Link:                   lLowScore,
		Status:                 store.StatusFailed,
		Category:               store.ErrConnRefused,
		TransportOK:            false,
		TransportEvidenceKnown: true,
	})

	// 4. Score above threshold: fully passed Gemini & transport
	lPassed := "vless://passed@1.1.1.4:443#Passed"
	st.PutWithTransition(store.Result{
		Link:                   lPassed,
		Status:                 store.StatusPassed,
		TransportOK:            true,
		TransportEvidenceKnown: true,
	})

	// 5. Explicit transport failure with known evidence
	lExplicitFail := "vless://explicitfail@1.1.1.6:443#ExplicitFail"
	st.PutWithTransition(store.Result{
		Link:                   lExplicitFail,
		Status:                 store.StatusFailed,
		Category:               store.ErrTimeout,
		TransportOK:            false,
		TransportEvidenceKnown: true,
	})

	// 6. Legacy record with no explicit transport evidence (historical compatibility)
	lLegacyTarget := "vless://legacytarget@1.1.1.7:443#LegacyTarget"
	st.PutWithTransition(store.Result{
		Link:                   lLegacyTarget,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: false,
	})

	lLegacyNetFail := "vless://legacynetfail@1.1.1.8:443#LegacyNetFail"
	st.PutWithTransition(store.Result{
		Link:                   lLegacyNetFail,
		Status:                 store.StatusFailed,
		Category:               store.ErrConnRefused,
		TransportEvidenceKnown: false,
	})

	// Test all candidate states for 100% equivalence between:
	// - ServabilityGate
	// - IsNetworkHealthyRecord
	// - NetworkHealthyStateForTest
	// - CandidateIndex
	indexEntries := st.CandidateIndex()
	indexByLink := make(map[string]store.CandidateIndexEntry, len(indexEntries))
	for _, entry := range indexEntries {
		indexByLink[entry.CanonicalLink] = entry
	}

	testCases := []struct {
		link         string
		wantServable bool
		wantHealthy  bool
	}{
		{lBlocked, false, true},
		{lDenied, false, true},
		{lLowScore, false, false},
		{lPassed, true, true},
		{lExplicitFail, false, false},
		{lLegacyTarget, false, true},
		{lLegacyNetFail, false, false},
	}

	for _, tc := range testCases {
		canonical := store.CanonicalizeLink(tc.link)
		rec, ok := st.GetRecord(tc.link)
		if !ok {
			t.Fatalf("record not found for %s", tc.link)
		}

		servableGate, _ := st.ServabilityGate(rec)
		if servableGate != tc.wantServable {
			t.Errorf("[%s] ServabilityGate = %v, want %v", tc.link, servableGate, tc.wantServable)
		}

		isServable := st.IsServableRecord(rec)
		if isServable != tc.wantServable {
			t.Errorf("[%s] IsServableRecord = %v, want %v", tc.link, isServable, tc.wantServable)
		}

		netHealthy := st.IsNetworkHealthyRecord(rec)
		if netHealthy != tc.wantHealthy {
			t.Errorf("[%s] IsNetworkHealthyRecord = %v, want %v", tc.link, netHealthy, tc.wantHealthy)
		}

		healthyState, servableState := st.NetworkHealthyStateForTest(rec)
		if healthyState != tc.wantHealthy || servableState != tc.wantServable {
			t.Errorf("[%s] NetworkHealthyStateForTest = (%v, %v), want (%v, %v)",
				tc.link, healthyState, servableState, tc.wantHealthy, tc.wantServable)
		}

		idxEntry, hasIdx := indexByLink[canonical]
		if !hasIdx {
			t.Fatalf("[%s] not found in CandidateIndex", tc.link)
		}
		if idxEntry.Servable != tc.wantServable || idxEntry.NetworkHealthy != tc.wantHealthy {
			t.Errorf("[%s] CandidateIndexEntry = (Servable:%v, NetHealthy:%v), want (%v, %v)",
				tc.link, idxEntry.Servable, idxEntry.NetworkHealthy, tc.wantServable, tc.wantHealthy)
		}
	}

	// Grace period inconclusive: legacy record with prior pass and 1 inconclusive observation
	recGrace := &store.CandidateRecord{
		CanonicalLink: "vless://grace@1.1.1.5:443#Grace",
		Score:         0.2, // Below MinServableScore (0.75)
		History: func() store.BoundedHistory {
			h := store.NewBoundedHistory(10)
			h.Push(store.ProbeSample{
				Status:                 store.StatusInconclusive,
				TransportOK:            true,
				TransportEvidenceKnown: true,
			})
			return h
		}(),
		Latest: store.Result{
			Status:                  store.StatusInconclusive,
			PreviouslyPassed:        true,
			ConsecutiveInconclusive: 1,
			TransportOK:             true,
			TransportEvidenceKnown:  true,
		},
	}
	gateGrace, reasonGrace := st.ServabilityGate(recGrace)
	if !gateGrace || reasonGrace != "Servable (grace period)" {
		t.Errorf("expected grace period servability, got %v (%s)", gateGrace, reasonGrace)
	}
	hGrace, sGrace := st.NetworkHealthyStateForTest(recGrace)
	if !hGrace || !sGrace {
		t.Errorf("grace period candidate must have (healthy:true, servable:true), got (%v, %v)", hGrace, sGrace)
	}

	// 8. Cold-start record (rec.History.Count < MinObservationsForServing)
	recCold := &store.CandidateRecord{
		CanonicalLink: "vless://cold@1.1.1.9:443#Cold",
		History:       store.NewBoundedHistory(10), // Count is 0
	}
	hCold, sCold := st.NetworkHealthyStateForTest(recCold)
	if hCold || sCold {
		t.Errorf("cold start candidate must have (healthy:false, servable:false), got (%v, %v)", hCold, sCold)
	}

	// 9. Pending absent / Absent cycles > 0 / Present candidate reappearing
	// Start a cycle with a subset of links to populate pendingAbsent & pendingPresent
	activeCycleLinks := map[string]struct{}{
		lPassed:  {},
		lBlocked: {},
	}
	st.StartCycle(activeCycleLinks)

	// In active cycle:
	// lPassed is present
	// lDenied was absent in this cycle -> disqualified by Gate 1 (pendingAbsent)
	recDenied, _ := st.GetRecord(lDenied)
	hPending, sPending := st.NetworkHealthyStateForTest(recDenied)
	if hPending || sPending {
		t.Errorf("pending absent candidate must not be healthy or servable, got (%v, %v)", hPending, sPending)
	}
	gatePending, reasonPending := st.ServabilityGate(recDenied)
	if gatePending || reasonPending != "Absent in active cycle" {
		t.Errorf("expected pending absent rejection, got %v (%s)", gatePending, reasonPending)
	}

	// Finish cycle to transition absent candidate to AbsentCycles > 0
	st.FinishCycle()

	// lDenied now has AbsentCycles > 0
	recAbsent, _ := st.GetRecord(lDenied)
	if recAbsent.AbsentCycles == 0 {
		t.Fatalf("expected AbsentCycles > 0 after cycle completion")
	}

	// In the next cycle, start with lDenied present again (candidate reappearing from source)
	reappearingLinks := map[string]struct{}{
		lDenied: {},
		lPassed: {},
	}
	st.StartCycle(reappearingLinks)

	// lDenied is in pendingPresent -> source absence gate must NOT disqualify it!
	recReappearing, _ := st.GetRecord(lDenied)
	hReappear, sReappear := st.NetworkHealthyStateForTest(recReappearing)
	// For lDenied, target is denied so servable=false, but transport passed so network-healthy=true
	if !hReappear || sReappear {
		t.Errorf("reappearing candidate with transport OK should be network-healthy, got (%v, %v)", hReappear, sReappear)
	}
}
