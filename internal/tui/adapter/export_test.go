package adapter

import "time"

// SetIndexThrottleIntervalForTest sets the candidate index rebuild throttle duration for tests.
func (a *Adapter) SetIndexThrottleIntervalForTest(d time.Duration) {
	a.indexMu.Lock()
	defer a.indexMu.Unlock()
	a.indexThrottleInterval = d
}

// LastIndexRevForTest returns the Store revision currently reflected in cachedEntries.
func (a *Adapter) LastIndexRevForTest() uint64 {
	a.indexMu.RLock()
	defer a.indexMu.RUnlock()
	return a.lastIndexRev
}

// OpaqueIDMapCountsForTest returns the current sizes of linkToID and idToLink for churn verification.
func (a *Adapter) OpaqueIDMapCountsForTest() (int, int) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.linkToID), len(a.idToLink)
}

// RebuildIndexForTest exposes rebuildIndex for benchmarks and diagnostics.
func (a *Adapter) RebuildIndexForTest(force bool) {
	a.rebuildIndex(force)
}

// PruneOrphanIDsForTest exposes pruneOrphanIDs for testing.
func (a *Adapter) PruneOrphanIDsForTest() {
	a.pruneOrphanIDs()
}

// SetBeforePruneLockHookForTest registers a hook called in pruneOrphanIDs before acquiring a.mu.Lock.
func (a *Adapter) SetBeforePruneLockHookForTest(hook func()) {
	a.beforePruneLockHook = hook
}

// SetRebuildInFlightHookForTest registers a hook called in rebuildIndex while rebuild is actively in flight.
func (a *Adapter) SetRebuildInFlightHookForTest(hook func()) {
	a.rebuildInFlightHook = hook
}

// OpaqueIDForLinkForTest returns the opaque ID mapped to canonical link, or false if absent.
func (a *Adapter) OpaqueIDForLinkForTest(canonicalLink string) (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	id, ok := a.linkToID[canonicalLink]
	return id, ok
}
