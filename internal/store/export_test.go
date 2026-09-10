package store

import "time"

// StartCycleLockedForTest executes only the internal synchronized phase of StartCycle,
// allowing test suites and benchmarks to measure write-lock hold duration in isolation.
func (s *Store) StartCycleLockedForTest(canonicalPresent map[string]struct{}) {
	s.startCycleLocked(canonicalPresent)
}

// StartCycleWithTimingForTest runs StartCycle while recording the exact duration of
// the preprocessing phase and the exclusive write-lock hold phase.
func (s *Store) StartCycleWithTimingForTest(currentLinks map[string]struct{}) (preDuration, lockDuration time.Duration) {
	t0 := time.Now()
	canonicalPresent := CanonicalizeLinks(currentLinks)
	preDuration = time.Since(t0)

	t1 := time.Now()
	s.startCycleLocked(canonicalPresent)
	lockDuration = time.Since(t1)
	return preDuration, lockDuration
}

// PendingAbsentCountForTest returns the size of pendingAbsent map under RLock.
func (s *Store) PendingAbsentCountForTest() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pendingAbsent)
}

// PendingPresentCountForTest returns the size of pendingPresent map under RLock.
func (s *Store) PendingPresentCountForTest() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pendingPresent)
}

// IsPendingPresentForTest checks if a canonical link is in pendingPresent under RLock.
func (s *Store) IsPendingPresentForTest(canonicalLink string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.pendingPresent[canonicalLink]
	return ok
}

// IsPendingAbsentForTest checks if a canonical link is in pendingAbsent under RLock.
func (s *Store) IsPendingAbsentForTest(canonicalLink string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.pendingAbsent[canonicalLink]
	return ok
}
