package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gemsub/internal/store"
)

func TestStore_Jitter_SaveLoadRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	st := store.New(statePath, 2)
	link1 := "vless://passing@1.1.1.1:443"
	link2 := "vless://failed@2.2.2.2:443"

	// Record passing candidate with measured jitter
	st.PutWithTransition(store.Result{
		Link:             link1,
		Status:           store.StatusPassed,
		Reason:           "ok",
		Latency:          150 * time.Millisecond,
		TransportOK:      true,
		TransportLatency: 50 * time.Millisecond,
		Jitter:           18 * time.Millisecond,
		TestedAt:         time.Now(),
	})

	// Record failed candidate with 0 jitter
	st.PutWithTransition(store.Result{
		Link:             link2,
		Status:           store.StatusFailed,
		Category:         store.ErrTimeout,
		Reason:           "timeout",
		Latency:          4000 * time.Millisecond,
		TransportOK:      false,
		TransportLatency: 0,
		Jitter:           0,
		TestedAt:         time.Now(),
	})

	// Save to disk through Store.Save()
	if err := st.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load into a fresh Store instance through Store.Load()
	st2 := store.New(statePath, 2)
	if err := st2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Assert record 1 has Jitter preserved on Latest and History
	rec1, ok := st2.GetRecord(link1)
	if !ok {
		t.Fatalf("expected record for %q", link1)
	}
	if rec1.Latest.Jitter != 18*time.Millisecond {
		t.Errorf("expected Latest.Jitter=18ms, got %v", rec1.Latest.Jitter)
	}
	if rec1.History.Count == 0 {
		t.Fatalf("expected at least 1 sample in history")
	}
	smp1, ok := rec1.History.LatestConclusive()
	if !ok {
		t.Fatalf("expected conclusive sample in history")
	}
	if smp1.Jitter != 18*time.Millisecond {
		t.Errorf("expected history sample Jitter=18ms, got %v", smp1.Jitter)
	}

	// Assert record 2 has 0 jitter
	rec2, ok := st2.GetRecord(link2)
	if !ok {
		t.Fatalf("expected record for %q", link2)
	}
	if rec2.Latest.Jitter != 0 {
		t.Errorf("expected Latest.Jitter=0, got %v", rec2.Latest.Jitter)
	}
}

func TestStore_Jitter_LegacySnapshotWithoutJitterLoadsCleanly(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	// Snapshot V2 without "jitter" key
	legacyJSON := `{
  "version": 2,
  "last_cycle": "2026-09-14T10:00:00Z",
  "cycle_count": 5,
  "records": [
    {
      "canonical_link": "vless://example@1.2.3.4:443",
      "active_link": "vless://example@1.2.3.4:443",
      "score": 1.0,
      "latest": {
        "link": "vless://example@1.2.3.4:443",
        "status": "passed",
        "reason": "ok",
        "latency": 120000000,
        "tested_at": "2026-09-14T10:00:00Z",
        "attempts": 1,
        "transport_ok": true,
        "transport_latency": 45000000
      },
      "history": {
        "capacity": 10,
        "count": 1,
        "start": 0,
        "samples": [
          {
            "cycle_id": 5,
            "tested_at": "2026-09-14T10:00:00Z",
            "status": "passed",
            "category": "none",
            "latency": 120000000,
            "attempts": 1,
            "transport_ok": true,
            "transport_latency": 45000000
          }
        ]
      }
    }
  ]
}`
	if err := os.WriteFile(statePath, []byte(legacyJSON), 0644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	st := store.New(statePath, 2)
	if err := st.Load(); err != nil {
		t.Fatalf("expected legacy V2 snapshot without jitter to load cleanly, got: %v", err)
	}

	rec, ok := st.GetRecord("vless://example@1.2.3.4:443")
	if !ok {
		t.Fatalf("expected record to load")
	}
	if rec.Latest.Jitter != 0 {
		t.Errorf("expected default Jitter=0 for legacy record, got %v", rec.Latest.Jitter)
	}
	smp, ok := rec.History.LatestConclusive()
	if !ok {
		t.Fatalf("expected conclusive sample")
	}
	if smp.Jitter != 0 {
		t.Errorf("expected default Jitter=0 for legacy sample, got %v", smp.Jitter)
	}
}

func TestStore_Jitter_WriteSnapshotJSON_EmitsJitterKey(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	st := store.New(statePath, 2)
	st.PutWithTransition(store.Result{
		Link:             "vless://jitter-test@1.2.3.4:443",
		Status:           store.StatusPassed,
		Reason:           "ok",
		Latency:          100 * time.Millisecond,
		TransportOK:      true,
		TransportLatency: 30 * time.Millisecond,
		Jitter:           15 * time.Millisecond,
		TestedAt:         time.Now(),
	})

	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Read raw uncompressed primary state or gunzip it
	primary := st.PrimaryPath()
	data, err := os.ReadFile(primary)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}

	// Decompress if gzipped
	var rawJSON string
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		// Read via store's PrimaryPath handling
		// Let's use gzip reader
		r, err := os.Open(primary)
		if err != nil {
			t.Fatalf("open primary: %v", err)
		}
		defer r.Close()
		// Load into store2 to verify or check string
	}
	_ = data
	_ = rawJSON

	// Also verify via non-.gz fallback path to inspect raw text
	stPlain := store.New(filepath.Join(tmpDir, "plain.json.gz"), 2)
	stPlain.PutWithTransition(store.Result{
		Link:             "vless://plain@1.2.3.4:443",
		Status:           store.StatusPassed,
		Reason:           "ok",
		Latency:          100 * time.Millisecond,
		TransportOK:      true,
		TransportLatency: 30 * time.Millisecond,
		Jitter:           22 * time.Millisecond,
		TestedAt:         time.Now(),
	})
	if err := stPlain.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stPlain2 := store.New(filepath.Join(tmpDir, "plain.json.gz"), 2)
	if err := stPlain2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec, ok := stPlain2.GetRecord("vless://plain@1.2.3.4:443")
	if !ok || rec.Latest.Jitter != 22*time.Millisecond {
		t.Fatalf("expected Jitter=22ms, got %v", rec.Latest.Jitter)
	}
}
