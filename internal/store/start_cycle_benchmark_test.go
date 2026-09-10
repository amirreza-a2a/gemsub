package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"gemsub/internal/store"
)

func setupBenchmarkStore(b *testing.B, count int) (*store.Store, map[string]struct{}) {
	b.Helper()
	st := store.New(filepath.Join(b.TempDir(), "bench.json"), 2)
	links := generateTestLinks(count)
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
	return st, links
}

// BenchmarkStartCycle_50k measures the end-to-end StartCycle call on 50,000 candidates.
func BenchmarkStartCycle_50k(b *testing.B) {
	st, links := setupBenchmarkStore(b, 50000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.StartCycle(links)
	}
}

// BenchmarkStartCycle_Preprocessing_50k measures link canonicalization outside the lock on 50,000 candidates.
func BenchmarkStartCycle_Preprocessing_50k(b *testing.B) {
	_, links := setupBenchmarkStore(b, 50000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.CanonicalizeLinks(links)
	}
}

// BenchmarkStartCycle_WriteLock_50k measures the isolated write-lock hold duration on 50,000 candidates.
func BenchmarkStartCycle_WriteLock_50k(b *testing.B) {
	st, links := setupBenchmarkStore(b, 50000)
	canonical := store.CanonicalizeLinks(links)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.StartCycleLockedForTest(canonical)
	}
}

// BenchmarkStartCycle_62k measures the end-to-end StartCycle call on 62,000 candidates.
func BenchmarkStartCycle_62k(b *testing.B) {
	st, links := setupBenchmarkStore(b, 62000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.StartCycle(links)
	}
}

// BenchmarkStartCycle_Preprocessing_62k measures link canonicalization outside the lock on 62,000 candidates.
func BenchmarkStartCycle_Preprocessing_62k(b *testing.B) {
	_, links := setupBenchmarkStore(b, 62000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.CanonicalizeLinks(links)
	}
}

// BenchmarkStartCycle_WriteLock_62k measures the isolated write-lock hold duration on 62,000 candidates.
func BenchmarkStartCycle_WriteLock_62k(b *testing.B) {
	st, links := setupBenchmarkStore(b, 62000)
	canonical := store.CanonicalizeLinks(links)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.StartCycleLockedForTest(canonical)
	}
}
