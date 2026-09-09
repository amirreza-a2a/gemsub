package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gemsub/internal/store"
)

// TestTransportEvidence_ExplicitSuccess verifies that explicit transport health
// (TransportEvidenceKnown=true, TransportOK=true) and TransportLatency are faithfully
// recorded in store.Result and ProbeSample, projecting the candidate into NetworkPassing().
func TestTransportEvidence_ExplicitSuccess(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	link := "vless://explicit-success-node#us"
	st.StartCycle(map[string]struct{}{link: {}})

	res := store.Result{
		Link:                   link,
		Status:                 store.StatusPassed,
		Latency:                120 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       45 * time.Millisecond,
	}
	st.PutWithTransition(res)
	st.FinishCycle()

	rec, ok := st.GetRecord(link)
	if !ok {
		t.Fatalf("expected record for %s", link)
	}

	// Verify Latest Result has explicit transport evidence
	if !rec.Latest.TransportEvidenceKnown {
		t.Errorf("expected Latest.TransportEvidenceKnown == true")
	}
	if !rec.Latest.TransportOK {
		t.Errorf("expected Latest.TransportOK == true")
	}
	if rec.Latest.TransportLatency != 45*time.Millisecond {
		t.Errorf("expected Latest.TransportLatency == 45ms, got %v", rec.Latest.TransportLatency)
	}

	// Verify ProbeSample in BoundedHistory carries explicit transport evidence
	sample, ok := rec.History.Last()
	if !ok {
		t.Fatalf("expected sample in history")
	}
	if !sample.TransportEvidenceKnown {
		t.Errorf("expected sample.TransportEvidenceKnown == true")
	}
	if !sample.TransportOK {
		t.Errorf("expected sample.TransportOK == true")
	}
	if sample.TransportLatency != 45*time.Millisecond {
		t.Errorf("expected sample.TransportLatency == 45ms, got %v", sample.TransportLatency)
	}

	// Candidate must be in NetworkPassing
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != link {
		t.Errorf("expected %s in NetworkPassing, got %v", link, netPassing)
	}
}

// TestTransportEvidence_ExplicitFailure verifies that explicit transport failure
// (TransportEvidenceKnown=true, TransportOK=false) is distinguished from unknown evidence
// and prevents network-healthy projection even if category is target-specific.
func TestTransportEvidence_ExplicitFailure(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	linkConnRefused := "vless://explicit-conn-refused#us"
	linkBlockedExplicitFail := "vless://explicit-fail-blocked#ir"
	st.StartCycle(map[string]struct{}{
		linkConnRefused:         {},
		linkBlockedExplicitFail: {},
	})

	// Case 1: Standard transport failure
	st.PutWithTransition(store.Result{
		Link:                   linkConnRefused,
		Status:                 store.StatusFailed,
		Category:               store.ErrConnRefused,
		Latency:                30 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TransportLatency:       30 * time.Millisecond,
	})

	// Case 2: Explicit transport failure marked even if category was ErrRegionBlocked.
	// Explicit failure must NOT fall through to Tier 3 historical compatibility bridge.
	st.PutWithTransition(store.Result{
		Link:                   linkBlockedExplicitFail,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		Latency:                40 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TransportLatency:       40 * time.Millisecond,
	})
	st.FinishCycle()

	for _, link := range []string{linkConnRefused, linkBlockedExplicitFail} {
		rec, ok := st.GetRecord(link)
		if !ok {
			t.Fatalf("expected record for %s", link)
		}
		if !rec.Latest.TransportEvidenceKnown {
			t.Errorf("[%s] expected Latest.TransportEvidenceKnown == true", link)
		}
		if rec.Latest.TransportOK {
			t.Errorf("[%s] expected Latest.TransportOK == false", link)
		}
		sample, ok := rec.History.Last()
		if !ok {
			t.Fatalf("[%s] expected sample in history", link)
		}
		if !sample.TransportEvidenceKnown {
			t.Errorf("[%s] expected sample.TransportEvidenceKnown == true", link)
		}
		if sample.TransportOK {
			t.Errorf("[%s] expected sample.TransportOK == false", link)
		}
	}

	// Neither candidate may be in NetworkPassing or Passing
	if len(st.NetworkPassing()) != 0 {
		t.Errorf("expected 0 candidates in NetworkPassing, got %v", st.NetworkPassing())
	}
	if len(st.Passing()) != 0 {
		t.Errorf("expected 0 candidates in Passing, got %v", st.Passing())
	}
}

// TestTransportEvidence_IndependentApplicationFailure verifies that TransportOK=true remains
// distinguishable from application-layer failure (e.g. Gemini 500 ErrTargetError).
// The candidate must be projected as NetworkPassing() without being Gemini-servable.
func TestTransportEvidence_IndependentApplicationFailure(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	linkTarget500 := "vless://gemini-500-node#us"
	linkTargetBlocked := "vless://gemini-blocked-node#ir"
	st.StartCycle(map[string]struct{}{
		linkTarget500:     {},
		linkTargetBlocked: {},
	})

	// Case A: Gemini returned 500 (ErrTargetError - not normally a target-specific category like ErrRegionBlocked).
	// With explicit TransportEvidenceKnown=true, TransportOK=true, it is recognized as network-healthy!
	st.PutWithTransition(store.Result{
		Link:                   linkTarget500,
		Status:                 store.StatusFailed,
		Category:               store.ErrTargetError,
		StatusCode:             500,
		Reason:                 "gemini internal server error",
		Latency:                250 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       50 * time.Millisecond,
	})

	// Case B: Gemini returned region block, also with explicit transport success
	st.PutWithTransition(store.Result{
		Link:                   linkTargetBlocked,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		StatusCode:             200,
		Reason:                 "regional block",
		Latency:                180 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       60 * time.Millisecond,
	})

	st.FinishCycle()

	// 1. Both candidates failed Gemini application check -> Passing() must be empty
	passing := st.Passing()
	if len(passing) != 0 {
		t.Errorf("expected Passing() to be empty, got %v", passing)
	}

	// 2. Both candidates had TransportOK=true -> NetworkPassing() must include both
	netPassing := st.NetworkPassing()
	if len(netPassing) != 2 {
		t.Fatalf("expected 2 candidates in NetworkPassing(), got %d: %v", len(netPassing), netPassing)
	}

	// 3. Verify rank ordering in NetworkPassingRanked() uses transport/network latency
	netRanked := st.NetworkPassingRanked()
	if len(netRanked) != 2 {
		t.Fatalf("expected 2 candidates in NetworkPassingRanked(), got %d", len(netRanked))
	}
	// 50ms transport latency < 60ms transport latency -> linkTarget500 should rank first
	if netRanked[0] != linkTarget500 || netRanked[1] != linkTargetBlocked {
		t.Errorf("unexpected ranking order: got %v, want [%s, %s]", netRanked, linkTarget500, linkTargetBlocked)
	}
}

// TestTransportEvidence_MissingOrLegacyPersistence verifies that historical state files
// without explicit transport_evidence_known or transport_ok fields load as Known=false, TransportOK=false.
// They must NOT be silently reinterpreted as explicit failure or explicit success,
// and Ticket 16's backward-compatibility bridge must continue to project legacy target-specific samples.
func TestTransportEvidence_MissingOrLegacyPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "legacy_state.json")

	// Construct a synthetic V2 snapshot with legacy samples lacking transport fields
	legacyV2JSON := `{
  "version": 2,
  "cycle_count": 5,
  "last_cycle": "2026-09-09T00:00:00Z",
  "records": [
    {
      "canonical_link": "vless://legacy-blocked#ir",
      "active_link": "vless://legacy-blocked#ir",
      "score": 0.5,
      "latest": {
        "link": "vless://legacy-blocked#ir",
        "status": "failed",
        "category": "region_blocked",
        "latency": 150000000
      },
      "history": {
        "capacity": 10,
        "count": 1,
        "start": 0,
        "samples": [
          {
            "cycle_id": 5,
            "status": "failed",
            "category": "region_blocked",
            "latency": 150000000,
            "attempts": 1
          }
        ]
      }
    },
    {
      "canonical_link": "vless://legacy-conn-refused#us",
      "active_link": "vless://legacy-conn-refused#us",
      "score": 0.1,
      "latest": {
        "link": "vless://legacy-conn-refused#us",
        "status": "failed",
        "category": "conn_refused",
        "latency": 20000000
      },
      "history": {
        "capacity": 10,
        "count": 1,
        "start": 0,
        "samples": [
          {
            "cycle_id": 5,
            "status": "failed",
            "category": "conn_refused",
            "latency": 20000000,
            "attempts": 1
          }
        ]
      }
    }
  ]
}`

	if err := os.WriteFile(statePath, []byte(legacyV2JSON), 0o644); err != nil {
		t.Fatalf("failed to write legacy state: %v", err)
	}

	st := store.New(statePath, 2)
	if err := st.Load(); err != nil {
		t.Fatalf("failed to load legacy state: %v", err)
	}

	// 1. Verify loaded records load as Known=false and TransportOK=false
	recBlocked, ok := st.GetRecord("vless://legacy-blocked#ir")
	if !ok {
		t.Fatalf("missing legacy-blocked record")
	}
	if recBlocked.Latest.TransportEvidenceKnown {
		t.Errorf("legacy record must NOT have TransportEvidenceKnown == true")
	}
	if recBlocked.Latest.TransportOK {
		t.Errorf("legacy record must NOT have TransportOK == true")
	}
	sampleBlocked, _ := recBlocked.History.Last()
	if sampleBlocked.TransportEvidenceKnown {
		t.Errorf("legacy sample must NOT have TransportEvidenceKnown == true")
	}
	if sampleBlocked.TransportOK {
		t.Errorf("legacy sample must NOT have TransportOK == true")
	}

	recRefused, ok := st.GetRecord("vless://legacy-conn-refused#us")
	if !ok {
		t.Fatalf("missing legacy-conn-refused record")
	}
	if recRefused.Latest.TransportEvidenceKnown || recRefused.Latest.TransportOK {
		t.Errorf("legacy conn-refused record must have Known=false, TransportOK=false")
	}

	// 2. In an active cycle, verify Ticket 16's backward-compatibility bridge:
	// legacy-blocked is network-healthy via Tier 3 (Known=false, ErrRegionBlocked),
	// legacy-conn-refused is NOT network-healthy.
	st.StartCycle(map[string]struct{}{
		"vless://legacy-blocked#ir":      {},
		"vless://legacy-conn-refused#us": {},
	})

	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != "vless://legacy-blocked#ir" {
		t.Errorf("expected [vless://legacy-blocked#ir] in NetworkPassing() via compatibility bridge; got %v", netPassing)
	}
}

// TestTransportEvidence_SerializationRoundTrip verifies that explicit true, explicit false,
// and missing legacy transport evidence survive full save-to-disk and load-from-disk round-trips.
func TestTransportEvidence_SerializationRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "roundtrip_state.json")

	st1 := store.New(statePath, 2)
	linkExplicitTrue := "vless://explicit-true#us"
	linkExplicitFalse := "vless://explicit-false#de"
	linkLegacyUnknown := "vless://legacy-unknown#jp"

	st1.StartCycle(map[string]struct{}{
		linkExplicitTrue:  {},
		linkExplicitFalse: {},
		linkLegacyUnknown: {},
	})

	// 1. Explicit true
	st1.PutWithTransition(store.Result{
		Link:                   linkExplicitTrue,
		Status:                 store.StatusPassed,
		Latency:                100 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       35 * time.Millisecond,
	})

	// 2. Explicit false
	st1.PutWithTransition(store.Result{
		Link:                   linkExplicitFalse,
		Status:                 store.StatusFailed,
		Category:               store.ErrConnRefused,
		Latency:                20 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TransportLatency:       20 * time.Millisecond,
	})

	// 3. Legacy / unknown evidence
	st1.PutWithTransition(store.Result{
		Link:                   linkLegacyUnknown,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		Latency:                150 * time.Millisecond,
		TransportEvidenceKnown: false,
		TransportOK:            false,
	})
	st1.FinishCycle()

	if err := st1.Save(); err != nil {
		t.Fatalf("failed to save snapshot: %v", err)
	}

	// Verify disk file is valid JSON
	content, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read state file: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatalf("corrupt JSON output: %v", err)
	}
	if v, ok := raw["version"].(float64); !ok || int(v) != 2 {
		t.Errorf("expected version 2, got %v", raw["version"])
	}

	// Reload in a fresh Store instance
	st2 := store.New(statePath, 2)
	if err := st2.Load(); err != nil {
		t.Fatalf("failed to load state: %v", err)
	}

	// Verify Explicit True survived
	recTrue, ok := st2.GetRecord(linkExplicitTrue)
	if !ok || !recTrue.Latest.TransportEvidenceKnown || !recTrue.Latest.TransportOK || recTrue.Latest.TransportLatency != 35*time.Millisecond {
		t.Errorf("explicit true reload mismatch: %+v", recTrue)
	}
	sampleTrue, _ := recTrue.History.Last()
	if !sampleTrue.TransportEvidenceKnown || !sampleTrue.TransportOK || sampleTrue.TransportLatency != 35*time.Millisecond {
		t.Errorf("explicit true sample reload mismatch: %+v", sampleTrue)
	}

	// Verify Explicit False survived
	recFalse, ok := st2.GetRecord(linkExplicitFalse)
	if !ok || !recFalse.Latest.TransportEvidenceKnown || recFalse.Latest.TransportOK || recFalse.Latest.TransportLatency != 20*time.Millisecond {
		t.Errorf("explicit false reload mismatch: %+v", recFalse)
	}
	sampleFalse, _ := recFalse.History.Last()
	if !sampleFalse.TransportEvidenceKnown || sampleFalse.TransportOK || sampleFalse.TransportLatency != 20*time.Millisecond {
		t.Errorf("explicit false sample reload mismatch: %+v", sampleFalse)
	}

	// Verify Legacy Unknown survived
	recUnknown, ok := st2.GetRecord(linkLegacyUnknown)
	if !ok || recUnknown.Latest.TransportEvidenceKnown || recUnknown.Latest.TransportOK {
		t.Errorf("legacy unknown reload mismatch: %+v", recUnknown)
	}
	sampleUnknown, _ := recUnknown.History.Last()
	if sampleUnknown.TransportEvidenceKnown || sampleUnknown.TransportOK {
		t.Errorf("legacy unknown sample reload mismatch: %+v", sampleUnknown)
	}
}

// TestTransportEvidence_SnapshotProjection verifies that CandidateSnapshot projects
// explicit transport evidence (TransportEvidenceKnown, TransportOK, TransportLatency) without collapsing states.
func TestTransportEvidence_SnapshotProjection(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	linkPass := "vless://pass#us"
	linkTargetFail := "vless://target-fail#us"
	linkNetFail := "vless://net-fail#us"
	linkLegacy := "vless://legacy#ir"

	st.StartCycle(map[string]struct{}{
		linkPass:       {},
		linkTargetFail: {},
		linkNetFail:    {},
		linkLegacy:     {},
	})

	// 1. Pass: Known=true, TransportOK=true, Gemini Passed
	st.PutWithTransition(store.Result{
		Link:                   linkPass,
		Status:                 store.StatusPassed,
		Latency:                100 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       30 * time.Millisecond,
	})

	// 2. Target Fail: Known=true, TransportOK=true, Gemini Failed (500)
	st.PutWithTransition(store.Result{
		Link:                   linkTargetFail,
		Status:                 store.StatusFailed,
		Category:               store.ErrTargetError,
		Latency:                250 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       40 * time.Millisecond,
	})

	// 3. Net Fail: Known=true, TransportOK=false, Gemini not run / failed
	st.PutWithTransition(store.Result{
		Link:                   linkNetFail,
		Status:                 store.StatusFailed,
		Category:               store.ErrConnRefused,
		Latency:                10 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TransportLatency:       10 * time.Millisecond,
	})

	// 4. Legacy: Known=false, TransportOK=false, Gemini ErrRegionBlocked
	st.PutWithTransition(store.Result{
		Link:                   linkLegacy,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		Latency:                120 * time.Millisecond,
		TransportEvidenceKnown: false,
		TransportOK:            false,
	})

	st.FinishCycle()

	snapshots := st.Snapshots()
	if len(snapshots) != 4 {
		t.Fatalf("expected 4 snapshots, got %d", len(snapshots))
	}

	snapMap := make(map[string]store.CandidateSnapshot)
	for _, snap := range snapshots {
		snapMap[snap.Record.ActiveLink] = snap
	}

	// Verify Pass node: Servable=true, NetworkHealthy=true, Known=true, TransportOK=true
	sPass := snapMap[linkPass]
	if !sPass.Servable || !sPass.NetworkHealthy || !sPass.TransportEvidenceKnown || !sPass.TransportOK || sPass.TransportLatency != 30*time.Millisecond {
		t.Errorf("pass node snapshot mismatch: %+v", sPass)
	}

	// Verify TargetFail node: Servable=false, NetworkHealthy=true, Known=true, TransportOK=true
	sTargetFail := snapMap[linkTargetFail]
	if sTargetFail.Servable || !sTargetFail.NetworkHealthy || !sTargetFail.TransportEvidenceKnown || !sTargetFail.TransportOK || sTargetFail.TransportLatency != 40*time.Millisecond {
		t.Errorf("target-fail node snapshot mismatch: %+v", sTargetFail)
	}

	// Verify NetFail node: Servable=false, NetworkHealthy=false, Known=true, TransportOK=false
	sNetFail := snapMap[linkNetFail]
	if sNetFail.Servable || sNetFail.NetworkHealthy || !sNetFail.TransportEvidenceKnown || sNetFail.TransportOK {
		t.Errorf("net-fail node snapshot mismatch: %+v", sNetFail)
	}

	// Verify Legacy node: Servable=false, NetworkHealthy=true (via Tier 3), Known=false, TransportOK=false
	sLegacy := snapMap[linkLegacy]
	if sLegacy.Servable || !sLegacy.NetworkHealthy || sLegacy.TransportEvidenceKnown || sLegacy.TransportOK {
		t.Errorf("legacy node snapshot mismatch: %+v", sLegacy)
	}
}

// TestBoundedHistory_ExplicitTransportEvidence verifies that LastNetworkHealthyLatency
// correctly respects TransportEvidenceKnown without confusing explicit failure and legacy samples.
func TestBoundedHistory_ExplicitTransportEvidence(t *testing.T) {
	// 1. Explicit TransportOK=true sample returns its transport latency
	h1 := store.NewBoundedHistory(5)
	h1.Push(store.ProbeSample{
		Status:                 store.StatusFailed,
		Category:               store.ErrTargetError,
		Latency:                300 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       45 * time.Millisecond,
	})
	if lat, ok := h1.LastNetworkHealthyLatency(); !ok || lat != 45*time.Millisecond {
		t.Errorf("expected (45ms, true), got (%v, %v)", lat, ok)
	}

	// 2. Trailing inconclusive sample retains prior explicit transport evidence
	h1.Push(store.ProbeSample{
		Status:   store.StatusInconclusive,
		Category: store.ErrTimeout,
		Latency:  2000 * time.Millisecond,
	})
	if lat, ok := h1.LastNetworkHealthyLatency(); !ok || lat != 45*time.Millisecond {
		t.Errorf("expected (45ms, true) after trailing inconclusive, got (%v, %v)", lat, ok)
	}

	// 3. Explicit transport failure (Known=true, TransportOK=false) conclusively invalidates prior transport evidence
	h1.Push(store.ProbeSample{
		Status:                 store.StatusFailed,
		Category:               store.ErrConnRefused,
		Latency:                15 * time.Millisecond,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TransportLatency:       15 * time.Millisecond,
	})
	if lat, ok := h1.LastNetworkHealthyLatency(); ok || lat != 0 {
		t.Errorf("expected (0, false) after explicit transport failure, got (%v, %v)", lat, ok)
	}

	// 4. Legacy sample without explicit evidence (Known=false, ErrRegionBlocked) returns legacy latency
	h2 := store.NewBoundedHistory(5)
	h2.Push(store.ProbeSample{
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		Latency:                120 * time.Millisecond,
		TransportEvidenceKnown: false,
		TransportOK:            false,
	})
	if lat, ok := h2.LastNetworkHealthyLatency(); !ok || lat != 120*time.Millisecond {
		t.Errorf("expected (120ms, true) for legacy ErrRegionBlocked, got (%v, %v)", lat, ok)
	}
}
