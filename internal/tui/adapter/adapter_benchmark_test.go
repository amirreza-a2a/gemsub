package adapter_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gemsub/internal/events"
	"gemsub/internal/logging"
	"gemsub/internal/store"
	"gemsub/internal/tui/adapter"
	"gemsub/internal/tui/viewmodel"
)

func setup50kAdapter(b *testing.B) (*adapter.Adapter, *store.Store) {
	b.Helper()
	tmpDir := b.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)

	now := time.Now()
	for i := 0; i < 50000; i++ {
		link := fmt.Sprintf("vless://user-%d@1.1.1.1:443#Node-%d", i, i)
		st.PutWithTransition(store.Result{
			Link:                   link,
			Status:                 store.StatusPassed,
			Latency:                time.Duration(50+i%300) * time.Millisecond,
			TransportOK:            true,
			TransportEvidenceKnown: true,
			TransportLatency:       time.Duration(20+i%100) * time.Millisecond,
			TestedAt:               now,
		})
	}

	b.Cleanup(func() {
		ad.Close()
		bus.Close()
	})

	return ad, st
}

// BenchmarkCandidateRowsWindow_50k measures the latency and allocations of materializing
// a visible viewport window (25 rows) from a 50,000 candidate dataset.
func BenchmarkCandidateRowsWindow_50k(b *testing.B) {
	ad, _ := setup50kAdapter(b)

	// Prime the index cache
	_ = ad.Snapshot(viewmodel.FilterAll)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Viewport offset in the middle of the list
		rows := ad.CandidateRowsWindow(viewmodel.FilterAll, 25000, 25)
		if len(rows) != 25 {
			b.Fatalf("expected 25 rows, got %d", len(rows))
		}
	}
}

// BenchmarkPollSnapshot_50k_Throttled measures the normal 10 Hz polling loop tick
// when Store revision increments between ticks (e.g. ongoing probe results),
// benefiting from the 1 Hz candidate index throttle and bounded viewport row materialization.
func BenchmarkPollSnapshot_50k_Throttled(b *testing.B) {
	ad, st := setup50kAdapter(b)

	// Prime initial snapshot
	_ = ad.Snapshot(viewmodel.FilterAll)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Simulate a probe result incrementing store revision
		st.PutWithTransition(store.Result{
			Link:     "vless://user-0@1.1.1.1:443#Node-0",
			Status:   store.StatusPassed,
			Latency:  100 * time.Millisecond,
			TestedAt: time.Now(),
		})

		snap, updated := ad.PollSnapshot(viewmodel.FilterAll)
		if !updated || len(snap.Rows) > 50 {
			b.Fatalf("unexpected snapshot state: updated=%v rows=%d", updated, len(snap.Rows))
		}
	}
}

// BenchmarkPollSnapshot_50k_WarmTick measures a 10 Hz poll tick when an event marks adapter dirty
// while candidate ordering index is already cached (steady state during cycle).
func BenchmarkPollSnapshot_50k_WarmTick(b *testing.B) {
	ad, _ := setup50kAdapter(b)

	// Prime initial snapshot
	_ = ad.Snapshot(viewmodel.FilterAll)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ad.MarkDirty()
		snap, updated := ad.PollSnapshot(viewmodel.FilterAll)
		if !updated || len(snap.Rows) > 50 {
			b.Fatalf("unexpected snapshot state: updated=%v rows=%d", updated, len(snap.Rows))
		}
	}
}

// BenchmarkCandidateIndex_50k measures the lightweight Store.CandidateIndex presentation seam
// across 50,000 candidates, verifying it avoids deep-copying BoundedHistory.
func BenchmarkCandidateIndex_50k(b *testing.B) {
	_, st := setup50kAdapter(b)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		idx := st.CandidateIndex()
		if len(idx) != 50000 {
			b.Fatalf("expected 50000 entries, got %d", len(idx))
		}
	}
}

// BenchmarkCheckAndResetDirty_50k measures the steady-state 10 Hz dirty check
// across a 50,000 candidate dataset when no updates occurred, confirming O(1) performance.
func BenchmarkCheckAndResetDirty_50k(b *testing.B) {
	ad, _ := setup50kAdapter(b)

	// Prime initial snapshot and cached index
	_ = ad.Snapshot(viewmodel.FilterAll)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = ad.CheckAndResetDirty()
	}
}
