package store_test

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gemsub/internal/store"
)

func TestStore_ServiceProjections_Independence(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	now := time.Now()

	// Candidate 1: Passes Gemini, but RegionBlocked on Claude
	candGeminiOnly := "vless://cand-gemini-only@1.2.3.4:443#GeminiOnly"
	st.PutWithTransition(store.Result{
		Link:                   candGeminiOnly,
		Status:                 store.StatusPassed,
		Category:               store.ErrNone,
		Latency:                50 * time.Millisecond,
		TestedAt:               now,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       20 * time.Millisecond,
		Services: map[string]store.TargetResult{
			"gemini": {
				Status:   store.StatusPassed,
				Category: store.ErrNone,
				Latency:  50 * time.Millisecond,
			},
			"claude": {
				Status:   store.StatusFailed,
				Category: store.ErrRegionBlocked,
				Latency:  60 * time.Millisecond,
			},
		},
	})

	// Candidate 2: RegionBlocked on Gemini, but Passes Claude
	candClaudeOnly := "vless://cand-claude-only@2.3.4.5:443#ClaudeOnly"
	st.PutWithTransition(store.Result{
		Link:                   candClaudeOnly,
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		Latency:                55 * time.Millisecond,
		TestedAt:               now,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       22 * time.Millisecond,
		Services: map[string]store.TargetResult{
			"gemini": {
				Status:   store.StatusFailed,
				Category: store.ErrRegionBlocked,
				Latency:  55 * time.Millisecond,
			},
			"claude": {
				Status:   store.StatusPassed,
				Category: store.ErrNone,
				Latency:  45 * time.Millisecond,
			},
		},
	})

	// Candidate 3: Both Pass
	candBoth := "vless://cand-both@3.4.5.6:443#Both"
	st.PutWithTransition(store.Result{
		Link:                   candBoth,
		Status:                 store.StatusPassed,
		Category:               store.ErrNone,
		Latency:                40 * time.Millisecond,
		TestedAt:               now,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       15 * time.Millisecond,
		Services: map[string]store.TargetResult{
			"gemini": {
				Status:   store.StatusPassed,
				Category: store.ErrNone,
				Latency:  40 * time.Millisecond,
			},
			"claude": {
				Status:   store.StatusPassed,
				Category: store.ErrNone,
				Latency:  35 * time.Millisecond,
			},
		},
	})

	// Candidate 4: Transport failed
	candTransportFailed := "vless://cand-broken@4.5.6.7:443#Broken"
	st.PutWithTransition(store.Result{
		Link:                   candTransportFailed,
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		Latency:                100 * time.Millisecond,
		TestedAt:               now,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TransportLatency:       100 * time.Millisecond,
	})

	st.FinishCycle()

	// Projections verification
	geminiPassing := st.PassingFor("gemini")
	legacyPassing := st.Passing()
	claudePassing := st.PassingFor("claude")
	networkPassing := st.NetworkPassing()

	// 1. PassingFor("gemini") must be identical to legacy Passing()
	if !reflect.DeepEqual(geminiPassing, legacyPassing) {
		t.Fatalf("PassingFor(\"gemini\") != Passing(): got %v vs %v", geminiPassing, legacyPassing)
	}

	// 2. Gemini projection should contain GeminiOnly and Both, but NOT ClaudeOnly or Broken
	contains := func(slice []string, item string) bool {
		for _, s := range slice {
			if s == item {
				return true
			}
		}
		return false
	}

	if !contains(geminiPassing, candGeminiOnly) {
		t.Errorf("expected GeminiOnly in gemini projection")
	}
	if !contains(geminiPassing, candBoth) {
		t.Errorf("expected Both in gemini projection")
	}
	if contains(geminiPassing, candClaudeOnly) {
		t.Errorf("unexpected ClaudeOnly in gemini projection")
	}
	if contains(geminiPassing, candTransportFailed) {
		t.Errorf("unexpected Broken in gemini projection")
	}

	// 3. Claude projection should contain ClaudeOnly and Both, but NOT GeminiOnly or Broken
	if !contains(claudePassing, candClaudeOnly) {
		t.Errorf("expected ClaudeOnly in claude projection, got %v", claudePassing)
	}
	if !contains(claudePassing, candBoth) {
		t.Errorf("expected Both in claude projection, got %v", claudePassing)
	}
	if contains(claudePassing, candGeminiOnly) {
		t.Errorf("unexpected GeminiOnly in claude projection")
	}
	if contains(claudePassing, candTransportFailed) {
		t.Errorf("unexpected Broken in claude projection")
	}

	// 4. Transport health should contain all three transport-healthy candidates (GeminiOnly, ClaudeOnly, Both)
	if !contains(networkPassing, candGeminiOnly) || !contains(networkPassing, candClaudeOnly) || !contains(networkPassing, candBoth) {
		t.Errorf("expected all 3 transport-healthy candidates in NetworkPassing, got %v", networkPassing)
	}
	if contains(networkPassing, candTransportFailed) {
		t.Errorf("unexpected Broken in NetworkPassing")
	}
}

func TestStore_PassingFor_Gemini_IdenticalOrder(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	now := time.Now()
	r := rand.New(rand.NewSource(42))

	const count = 300
	for i := 0; i < count; i++ {
		link := fmt.Sprintf("vless://user-%d@node-%d.example.com:443#Node-%d", i, i%10, i)
		passed := r.Float64() > 0.4
		status := store.StatusPassed
		cat := store.ErrNone
		if !passed {
			status = store.StatusFailed
			cat = store.ErrProxyError
		}
		lat := time.Duration(30+r.Intn(100)) * time.Millisecond

		st.PutWithTransition(store.Result{
			Link:                   link,
			Status:                 status,
			Category:               cat,
			Latency:                lat,
			TestedAt:               now,
			TransportEvidenceKnown: true,
			TransportOK:            passed,
			TransportLatency:       lat / 2,
			Services: map[string]store.TargetResult{
				"gemini": {
					Status:   status,
					Category: cat,
					Latency:  lat,
				},
				"claude": {
					Status:   status,
					Category: cat,
					Latency:  lat + 5*time.Millisecond,
				},
			},
		})
	}
	st.FinishCycle()

	passing := st.Passing()
	passingGemini := st.PassingFor("gemini")

	if len(passing) == 0 {
		t.Fatalf("expected non-empty passing candidates")
	}

	if !reflect.DeepEqual(passing, passingGemini) {
		t.Fatalf("Passing() and PassingFor(\"gemini\") differed in content or order:\ngot %d vs %d", len(passing), len(passingGemini))
	}

	passingRanked := st.PassingRanked()
	passingRankedGemini := st.PassingForRanked("gemini")

	if !reflect.DeepEqual(passingRanked, passingRankedGemini) {
		t.Fatalf("PassingRanked() and PassingForRanked(\"gemini\") differed in content or order")
	}
}

func TestStore_Persistence_ClaudeFields(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "claude_state.json")
	st := store.New(statePath, 2)
	now := time.Now().Truncate(time.Second)

	link := "vless://user-1@claude-node.example.com:443#ClaudeNode"
	st.PutWithTransition(store.Result{
		Link:                   link,
		Status:                 store.StatusPassed,
		Category:               store.ErrNone,
		Latency:                42 * time.Millisecond,
		TestedAt:               now,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       18 * time.Millisecond,
		Services: map[string]store.TargetResult{
			"gemini": {
				Status:   store.StatusPassed,
				Category: store.ErrNone,
				Latency:  42 * time.Millisecond,
			},
			"claude": {
				Status:   store.StatusPassed,
				Category: store.ErrNone,
				Latency:  38 * time.Millisecond,
			},
		},
	})
	st.FinishCycle()

	if err := st.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Reload in a fresh store instance
	reloaded := store.New(statePath, 2)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	claudePassing := reloaded.PassingFor("claude")
	if len(claudePassing) != 1 || claudePassing[0] != link {
		t.Fatalf("expected candidate in reloaded PassingFor(\"claude\"), got %v", claudePassing)
	}

	geminiPassing := reloaded.PassingFor("gemini")
	if len(geminiPassing) != 1 || geminiPassing[0] != link {
		t.Fatalf("expected candidate in reloaded PassingFor(\"gemini\"), got %v", geminiPassing)
	}
}

func TestStore_TransportFailure_EvictsClaudeFromProjections(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "transport_fail.json"), 2)
	now := time.Now()
	link := "vless://user@node.example.com:443#TestNode"

	// Cycle 1: passes Gemini and Claude
	st.PutWithTransition(store.Result{
		Link:                   link,
		Status:                 store.StatusPassed,
		Category:               store.ErrNone,
		Latency:                50 * time.Millisecond,
		TestedAt:               now,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TransportLatency:       20 * time.Millisecond,
		Services: map[string]store.TargetResult{
			"gemini": {
				Status:   store.StatusPassed,
				Category: store.ErrNone,
				Latency:  50 * time.Millisecond,
			},
			"claude": {
				Status:   store.StatusPassed,
				Category: store.ErrNone,
				Latency:  45 * time.Millisecond,
			},
		},
	})
	st.FinishCycle()

	if len(st.PassingFor("claude")) != 1 {
		t.Fatalf("expected candidate in PassingFor(\"claude\") after cycle 1")
	}

	// Cycle 2: transport failure (e.g. proxy CDN error)
	st.PutWithTransition(store.Result{
		Link:                   link,
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		Latency:                100 * time.Millisecond,
		TestedAt:               now.Add(10 * time.Second),
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TransportLatency:       100 * time.Millisecond,
		// Notice: Services is nil or empty, as occurs when Stage 1 fails early
	})
	st.FinishCycle()

	claudePassing := st.PassingFor("claude")
	if len(claudePassing) != 0 {
		t.Fatalf("expected candidate to be evicted from PassingFor(\"claude\") after transport failure, got %v", claudePassing)
	}

	claudePassingRanked := st.PassingForRanked("claude")
	if len(claudePassingRanked) != 0 {
		t.Fatalf("expected candidate to be evicted from PassingForRanked(\"claude\") after transport failure, got %v", claudePassingRanked)
	}

	geminiPassing := st.PassingFor("gemini")
	if len(geminiPassing) != 0 {
		t.Fatalf("expected candidate to be evicted from PassingFor(\"gemini\") after transport failure, got %v", geminiPassing)
	}

	networkPassing := st.NetworkPassing()
	if len(networkPassing) != 0 {
		t.Fatalf("expected candidate to be evicted from NetworkPassing() after transport failure, got %v", networkPassing)
	}
}
