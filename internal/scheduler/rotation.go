package scheduler

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"gemsub/internal/parser"
	"gemsub/internal/store"
)

// candidateRotator maintains ephemeral least-recently-tested (LRT) rotation state
// for candidates under ProbeLimit constraints. It is strictly scheduler-local in-memory
// state and is never persisted to disk or Store models.
type candidateRotator struct {
	mu         sync.Mutex
	lastTested map[string]time.Time
}

// newCandidateRotator initializes a fresh ephemeral rotator instance.
func newCandidateRotator() *candidateRotator {
	return &candidateRotator{
		lastTested: make(map[string]time.Time),
	}
}

// RecordTested updates the last-tested timestamp for a candidate link upon completed probe service
// using the probe attempt initiation time (TestedAt).
// A zero timestamp represents "never completed/accounted probe"; if testedAt is zero (or link is empty),
// RecordTested is a no-op, preserving the invariant that zero represents "never completed/accounted probe"
// without silently manufacturing timestamps.
func (r *candidateRotator) RecordTested(link string, testedAt time.Time) {
	if link == "" || testedAt.IsZero() {
		return
	}
	canonical := store.CanonicalizeLink(link)

	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, exists := r.lastTested[canonical]; !exists || testedAt.After(prev) {
		r.lastTested[canonical] = testedAt
	}
}

// PruneStale removes rotation entries for canonical links that are no longer present
// in the current cycle's upstream linkSet.
func (r *candidateRotator) PruneStale(linkSet map[string]struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	presentCanonical := make(map[string]struct{}, len(linkSet))
	for raw := range linkSet {
		presentCanonical[store.CanonicalizeLink(raw)] = struct{}{}
	}

	for k := range r.lastTested {
		if _, ok := presentCanonical[k]; !ok {
			delete(r.lastTested, k)
		}
	}
}

// SelectCandidates applies the Least-Recently-Tested (LRT) scheduling policy:
// 1. Never-tested candidates have a zero timestamp (time.Time{}) and receive highest priority.
// 2. Previously tested candidates are ordered by lastTested timestamp ascending.
// 3. Ties are broken deterministically by canonical link ascending, and then original list index.
// 4. Returns at most probeLimit candidates (or all candidates if probeLimit <= 0).
func (r *candidateRotator) SelectCandidates(candidates []parser.Candidate, probeLimit int) []parser.Candidate {
	if len(candidates) == 0 {
		return nil
	}

	r.mu.Lock()
	type candidateMeta struct {
		cand       parser.Candidate
		canonical  string
		lastTested time.Time
		origIndex  int
	}

	meta := make([]candidateMeta, len(candidates))
	for i, c := range candidates {
		canon := store.CanonicalizeLink(c.Link)
		t, ok := r.lastTested[canon]
		if !ok {
			t = time.Time{} // zero time = highest priority
		}
		meta[i] = candidateMeta{
			cand:       c,
			canonical:  canon,
			lastTested: t,
			origIndex:  i,
		}
	}
	r.mu.Unlock()

	slices.SortFunc(meta, func(a, b candidateMeta) int {
		if !a.lastTested.Equal(b.lastTested) {
			if a.lastTested.Before(b.lastTested) {
				return -1
			}
			return 1
		}
		if n := cmp.Compare(a.canonical, b.canonical); n != 0 {
			return n
		}
		return cmp.Compare(a.origIndex, b.origIndex)
	})

	limit := len(meta)
	if probeLimit > 0 && probeLimit < limit {
		limit = probeLimit
	}

	selected := make([]parser.Candidate, limit)
	for i := 0; i < limit; i++ {
		selected[i] = meta[i].cand
	}
	return selected
}
