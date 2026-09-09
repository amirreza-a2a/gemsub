package store_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gemsub/internal/store"
)

func buildReferenceStore(tb testing.TB, count int) (*store.Store, string) {
	tb.Helper()
	tmpDir := tb.TempDir()
	statePath := filepath.Join(tmpDir, "gemsub_state.json")

	st := store.New(statePath, 2)
	now := time.Now().Truncate(time.Second)

	for i := 0; i < count; i++ {
		link := fmt.Sprintf("vless://user-%d@node-%d.example.com:443?security=reality&sni=test%d.com#Node-%d", i, i%500, i%100, i)
		st.PutWithTransition(store.Result{
			Link:                   link,
			Status:                 store.StatusPassed,
			Category:               store.ErrNone,
			Latency:                time.Duration(40+(i%100)) * time.Millisecond,
			TestedAt:               now,
			Attempts:               1,
			TransportOK:            true,
			TransportLatency:       time.Duration(20+(i%50)) * time.Millisecond,
			TransportEvidenceKnown: true,
		})
	}
	st.FinishCycle()

	return st, statePath
}

func TestPersistence_EmpiricalGates_56k(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 56k persistence verification in short mode")
	}

	const datasetCount = 56799
	st, statePath := buildReferenceStore(t, datasetCount)

	// Measure Save
	startSave := time.Now()
	if err := st.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	saveDuration := time.Since(startSave)

	// Measure primary compressed file size
	primaryPath := st.PrimaryPath()
	info, err := os.Stat(primaryPath)
	if err != nil {
		t.Fatalf("failed to stat primary file: %v", err)
	}
	sizeMB := float64(info.Size()) / (1024 * 1024)

	// Measure Load
	stReload := store.New(statePath, 2)
	startLoad := time.Now()
	if err := stReload.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	loadDuration := time.Since(startLoad)

	t.Logf("=== TICKET 28 EMPIRICAL RESULTS (56,799 candidates) ===")
	t.Logf("Compressed Primary Size : %.2f MB", sizeMB)
	t.Logf("Save Duration           : %v", saveDuration)
	t.Logf("Load Duration           : %v", loadDuration)

	// Verification Gates from Ticket 28:
	// 1. Compressed state < 10 MB (Baseline was 137.65 MB, >90% reduction)
	if sizeMB >= 10.0 {
		t.Errorf("FAIL: primary size %.2f MB exceeds 10.0 MB gate", sizeMB)
	}

	// 2. Candidate count fidelity
	if len(stReload.Passing()) != datasetCount {
		t.Errorf("candidate count mismatch: got %d, want %d", len(stReload.Passing()), datasetCount)
	}
}

func BenchmarkPersistence_Save_56k(b *testing.B) {
	const datasetCount = 56799
	st, _ := buildReferenceStore(b, datasetCount)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := st.Save(); err != nil {
			b.Fatalf("Save failed: %v", err)
		}
	}
}

func BenchmarkPersistence_Load_56k(b *testing.B) {
	const datasetCount = 56799
	st, statePath := buildReferenceStore(b, datasetCount)
	if err := st.Save(); err != nil {
		b.Fatalf("setup Save failed: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		stReload := store.New(statePath, 2)
		if err := stReload.Load(); err != nil {
			b.Fatalf("Load failed: %v", err)
		}
	}
}
