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
	a.indexMu.Lock()
	defer a.indexMu.Unlock()
	return a.lastIndexRev
}

// OpaqueIDMapCountsForTest returns the current sizes of linkToID and idToLink for churn verification.
func (a *Adapter) OpaqueIDMapCountsForTest() (int, int) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.linkToID), len(a.idToLink)
}
