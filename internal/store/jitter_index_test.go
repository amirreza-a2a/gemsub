package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"gemsub/internal/store"
)

func TestCandidateIndex_Jitter(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 10)

	linkWithJitter := "vless://test1@1.1.1.1:443#WithJitter"
	linkWithoutJitter := "vless://test2@2.2.2.2:443#WithoutJitter"

	st.PutWithTransition(store.Result{
		Link:     linkWithJitter,
		Status:   store.StatusPassed,
		Latency:  100 * time.Millisecond,
		Jitter:   24 * time.Millisecond,
		TestedAt: time.Now(),
	})

	st.PutWithTransition(store.Result{
		Link:     linkWithoutJitter,
		Status:   store.StatusPassed,
		Latency:  90 * time.Millisecond,
		Jitter:   0,
		TestedAt: time.Now(),
	})

	entries := st.CandidateIndex()
	if len(entries) != 2 {
		t.Fatalf("expected 2 index entries, got %d", len(entries))
	}

	entryMap := make(map[string]store.CandidateIndexEntry, len(entries))
	for _, entry := range entries {
		entryMap[entry.CanonicalLink] = entry
	}

	e1, ok1 := entryMap[store.CanonicalizeLink(linkWithJitter)]
	if !ok1 {
		t.Fatalf("missing entry for %s", linkWithJitter)
	}
	if e1.Jitter != 24*time.Millisecond {
		t.Errorf("expected Jitter 24ms, got %v", e1.Jitter)
	}

	e2, ok2 := entryMap[store.CanonicalizeLink(linkWithoutJitter)]
	if !ok2 {
		t.Fatalf("missing entry for %s", linkWithoutJitter)
	}
	if e2.Jitter != 0 {
		t.Errorf("expected Jitter 0, got %v", e2.Jitter)
	}
}
