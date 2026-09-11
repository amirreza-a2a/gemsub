package adapter

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gemsub/internal/clipboard"
	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/logging"
	"gemsub/internal/publisher"
	"gemsub/internal/scheduler"
	"gemsub/internal/source"
	"gemsub/internal/store"
	"gemsub/internal/tui/country"
	"gemsub/internal/tui/viewmodel"
)

// Adapter connects the authoritative Store, EventBus invalidation hints,
// and RingLogHandler to produce decoupled, presentation-ready ViewModels.
// It maintains ephemeral presentation coordination state (dirty flags,
// opaque presentation ID mappings, subscriber lifecycle) and NEVER maintains
// a shadow candidate-state store.
type Adapter struct {
	st          *store.Store
	bus         *events.EventBus
	ringHandler *logging.RingLogHandler

	mu                      sync.RWMutex
	dirty                   int32
	cycleStatus             viewmodel.CycleStatus
	progressCurrent         int
	progressTotal           int
	cyclePassed             int
	cycleFailed             int
	cycleInconclusive       int
	lastCompletedCycleCount int

	// Opaque ID mapping for the current UI snapshot lifecycle
	idToLink  map[string]string
	linkToID  map[string]string
	idCounter uint64

	// EventBus subscriber
	subCh  <-chan any
	doneCh chan struct{}

	// Store revision tracking for guaranteed eventual convergence
	lastRevision uint64

	// Log view state
	logLevel     slog.Level
	scrollOffset int
	followMode   bool

	// Presentation mode for country flags
	flagMode country.Mode

	// Cached sorted index and throttling
	indexMu               sync.RWMutex
	rebuildMu             sync.Mutex
	lastIndexTime         time.Time
	lastIndexRev          uint64
	indexThrottleInterval time.Duration
	cachedEntries         []store.CandidateIndexEntry
	cachedFilterAll       []int
	cachedFilterGem       []int
	cachedFilterGen       []int

	// Test synchronization hooks
	beforePruneLockHook func()
	rebuildInFlightHook func()

	// Application services for Configuration Center
	configSvc     *config.Service
	sourceSvc     *source.Service
	publishSvc    *publisher.Service
	schedulerCtrl *scheduler.ControlService

	// Event-driven configuration/publishing cache (used when services are nil or as event fallback)
	lastCfg          config.Config
	pubRunning       bool
	lastPublished    time.Time
	lastPublishError string
	publishCount     int
	publishFailCount int
}

// New creates an unstarted Adapter.
func New(st *store.Store, bus *events.EventBus, ring *logging.RingLogHandler) *Adapter {
	var initRev uint64
	var initCycleCount int
	if st != nil {
		initRev = st.Revision()
		initCycleCount = st.CycleCount()
	}
	return &Adapter{
		st:                      st,
		bus:                     bus,
		ringHandler:             ring,
		cycleStatus:             viewmodel.CycleIdle,
		idToLink:                make(map[string]string),
		linkToID:                make(map[string]string),
		doneCh:                  make(chan struct{}),
		logLevel:                slog.LevelInfo,
		followMode:              true,
		lastRevision:            initRev,
		lastCompletedCycleCount: initCycleCount,
		flagMode:                country.ModeAuto,
		indexThrottleInterval:   time.Second,
	}
}

// Subscribe establishes the EventBus subscription before scheduler execution begins.
func (a *Adapter) Subscribe() {
	a.mu.Lock()
	if a.subCh != nil {
		a.mu.Unlock()
		return
	}
	if a.bus != nil {
		a.subCh = a.bus.Subscribe()
		go a.eventLoop(a.subCh, a.doneCh)
	}
	a.mu.Unlock()
}

// Close terminates the background event loop and unsubscribes from the EventBus.
func (a *Adapter) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()

	select {
	case <-a.doneCh:
		// already closed
	default:
		close(a.doneCh)
	}

	if a.bus != nil && a.subCh != nil {
		a.bus.Unsubscribe(a.subCh)
		a.subCh = nil
	}
}

func (a *Adapter) eventLoop(subCh <-chan any, doneCh <-chan struct{}) {
	for {
		select {
		case <-doneCh:
			return
		case evt, ok := <-subCh:
			if !ok {
				return
			}
			a.handleEvent(evt)
		}
	}
}

func (a *Adapter) handleEvent(evt any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch e := evt.(type) {
	case events.CycleStarted:
		a.cycleStatus = viewmodel.CycleRunning
		a.progressCurrent = 0
		a.progressTotal = 0
		a.cyclePassed = 0
		a.cycleFailed = 0
		a.cycleInconclusive = 0
		atomic.StoreInt32(&a.dirty, 1)

	case events.CandidatesLoaded:
		a.progressTotal = e.Total
		atomic.StoreInt32(&a.dirty, 1)

	case events.ProbeCompleted:
		a.progressCurrent = e.Completed
		if e.Total > 0 {
			a.progressTotal = e.Total
		}
		a.cyclePassed = e.Passed
		a.cycleFailed = e.Failed
		a.cycleInconclusive = e.Inconclusive
		atomic.StoreInt32(&a.dirty, 1)

	case events.CycleFinished:
		a.cycleStatus = viewmodel.CycleIdle
		a.progressCurrent = e.Completed
		a.progressTotal = e.Total
		a.cyclePassed = e.Passed
		a.cycleFailed = e.Failed
		a.cycleInconclusive = e.Inconclusive
		if a.st != nil {
			a.lastCompletedCycleCount = a.st.CycleCount()
		}
		atomic.StoreInt32(&a.dirty, 1)

	case events.ConfigUpdated:
		a.lastCfg = e.New
		newMode := country.Mode(e.New.FlagMode)
		if newMode == "" {
			newMode = country.ModeAuto
		}
		if newMode != a.flagMode {
			a.flagMode = newMode
		}
		atomic.StoreInt32(&a.dirty, 1)

	case events.PublishingStarted:
		a.pubRunning = true
		atomic.StoreInt32(&a.dirty, 1)

	case events.PublishingFinished:
		a.pubRunning = false
		a.lastPublished = e.FinishedAt
		a.publishCount++
		atomic.StoreInt32(&a.dirty, 1)

	case events.PublishingFailed:
		a.pubRunning = false
		a.lastPublishError = e.Error
		a.publishFailCount++
		atomic.StoreInt32(&a.dirty, 1)
	}
}

// reconcileCycleStateLocked detects if Store has completed a cycle whose CycleFinished event
// was lost, and reconciles cycle status and metrics from authoritative Store state.
// Caller must hold a.mu Lock.
func (a *Adapter) reconcileCycleStateLocked() bool {
	if a.st == nil || a.cycleStatus != viewmodel.CycleRunning {
		return false
	}
	cycleCount := a.st.CycleCount()
	if cycleCount <= a.lastCompletedCycleCount {
		return false
	}
	a.cycleStatus = viewmodel.CycleIdle
	a.lastCompletedCycleCount = cycleCount

	if cycleCount > 0 {
		completedCycleID := uint64(cycleCount - 1)
		var passed, failed, inconclusive int
		for _, entry := range a.st.CandidateIndex() {
			if entry.LastCycleID == completedCycleID {
				switch entry.LatestStatus {
				case store.StatusPassed:
					passed++
				case store.StatusFailed:
					failed++
				case store.StatusInconclusive:
					inconclusive++
				}
			}
		}
		totalObs := passed + failed + inconclusive
		if totalObs > 0 {
			a.cyclePassed = passed
			a.cycleFailed = failed
			a.cycleInconclusive = inconclusive
			a.progressCurrent = totalObs
			if a.progressTotal < totalObs {
				a.progressTotal = totalObs
			}
		}
	}
	return true
}

// CheckAndResetDirty returns true if state has been invalidated since last check,
// checking EventBus notifications, Store revision increments, and overdue throttled index refreshes.
func (a *Adapter) CheckAndResetDirty() bool {
	a.indexMu.RLock()
	defer a.indexMu.RUnlock()

	a.mu.Lock()
	defer a.mu.Unlock()

	dirty := atomic.SwapInt32(&a.dirty, 0) == 1
	if a.st != nil {
		currentRev := a.st.Revision()
		if currentRev != a.lastRevision {
			a.lastRevision = currentRev
			dirty = true
		}
		if a.reconcileCycleStateLocked() {
			dirty = true
		}

		// Invariant: If candidate index is stale and the throttle has expired,
		// the pending index rebuild is overdue and must trigger a refresh even
		// if no new Store mutation occurred since the last tick.
		throttle := a.indexThrottleInterval
		if throttle < 0 {
			throttle = 0
		}
		if a.cachedEntries != nil {
			isStale := (a.lastIndexRev != currentRev || len(a.cachedEntries) != a.st.Count())
			if isStale && (throttle == 0 || time.Since(a.lastIndexTime) >= throttle) {
				dirty = true
			}
		}
	}
	return dirty
}

// MarkDirty manually flags the presentation state as needing refresh.
func (a *Adapter) MarkDirty() {
	atomic.StoreInt32(&a.dirty, 1)
}

// Header returns the current HeaderViewModel constructed from Store metadata
// and ephemeral cycle lifecycle metrics. Pass/Fail/Incon counts represent the active cycle only.
func (a *Adapter) Header() viewmodel.HeaderViewModel {
	a.rebuildIndex(false)

	a.indexMu.RLock()
	servableCount := len(a.cachedFilterGem)
	genericServableCount := len(a.cachedFilterGen)
	a.indexMu.RUnlock()

	a.mu.Lock()
	cycleStatus := a.cycleStatus
	progCurrent := a.progressCurrent
	progTotal := a.progressTotal
	cyclePassed := a.cyclePassed
	cycleFailed := a.cycleFailed
	cycleIncon := a.cycleInconclusive
	a.mu.Unlock()

	var totalCandidates int
	var cycleCount int
	var lastCycle time.Time
	if a.st != nil {
		totalCandidates = a.st.Count()
		cycleCount = a.st.CycleCount()
		lastCycle = a.st.LastCycle()
	}

	return viewmodel.HeaderViewModel{
		CycleCount:           cycleCount,
		LastCycle:            lastCycle,
		CycleStatus:          cycleStatus,
		ProgressCurrent:      progCurrent,
		ProgressTotal:        progTotal,
		TotalCandidates:      totalCandidates,
		PassedCount:          cyclePassed,
		FailedCount:          cycleFailed,
		InconclusiveCount:    cycleIncon,
		ServableCount:        servableCount,
		GenericServableCount: genericServableCount,
	}
}

// StoreStats returns the authoritative Store aggregate statistics.
func (a *Adapter) StoreStats() store.Stats {
	if a.st != nil {
		return a.st.Stats()
	}
	return store.Stats{}
}

func (a *Adapter) pruneOrphanIDs() {
	if a.st == nil {
		return
	}
	a.mu.RLock()
	if len(a.linkToID) == 0 {
		a.mu.RUnlock()
		return
	}
	links := make([]string, 0, len(a.linkToID))
	for link := range a.linkToID {
		links = append(links, link)
	}
	a.mu.RUnlock()

	var toDelete []string
	for _, link := range links {
		if !a.st.HasCanonicalRecord(link) {
			toDelete = append(toDelete, link)
		}
	}
	if len(toDelete) == 0 {
		return
	}

	if a.beforePruneLockHook != nil {
		a.beforePruneLockHook()
	}

	a.mu.Lock()
	for _, link := range toDelete {
		// TOCTOU mitigation: re-verify with HasCanonicalRecord before deleting to ensure
		// a candidate that reappeared in Store concurrently is not unnecessarily pruned.
		if !a.st.HasCanonicalRecord(link) {
			if id, ok := a.linkToID[link]; ok {
				delete(a.linkToID, link)
				delete(a.idToLink, id)
			}
		}
	}
	a.mu.Unlock()
}

// computeCandidateIndex fetches candidate presentation entries from Store,
// prunes evicted presentation IDs, and returns deterministically sorted entries
// along with precomputed filter index slices.
func (a *Adapter) computeCandidateIndex() ([]store.CandidateIndexEntry, []int, []int, []int) {
	if a.st == nil {
		return nil, nil, nil, nil
	}

	entries := a.st.CandidateIndex()
	a.pruneOrphanIDs()

	// Deterministic Candidate Ordering Policy (Ticket 25, Ticket 33):
	// 1. Tier 1: Gemini-servable candidates (Tier == 1).
	// 2. Tier 2: Generic-servable candidates (Tier == 2).
	// 3. Tier 3: Unservable candidates (Tier == 3).
	// 4. Within each tier, proven candidates (HasPassed == true) before unproven candidates.
	// 5. Reliability Score descending.
	// 6. Effective latency ascending (LastPassedLatency, then TransportLatency).
	// 7. Stable canonical link ascending as deterministic tie-breaker.
	slices.SortFunc(entries, func(a, b store.CandidateIndexEntry) int {
		if a.Tier != b.Tier {
			return cmp.Compare(a.Tier, b.Tier)
		}
		if a.HasPassed != b.HasPassed {
			if a.HasPassed {
				return -1
			}
			return 1
		}
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.EffectiveLatency != b.EffectiveLatency {
			if a.EffectiveLatency == 0 {
				return 1
			}
			if b.EffectiveLatency == 0 {
				return -1
			}
			return cmp.Compare(a.EffectiveLatency, b.EffectiveLatency)
		}
		return cmp.Compare(a.CanonicalLink, b.CanonicalLink)
	})

	allIndices := make([]int, len(entries))
	var numGem, numGen int
	for i := range entries {
		allIndices[i] = i
		if entries[i].Tier == 1 {
			numGem++
		}
		if entries[i].Tier <= 2 {
			numGen++
		}
	}
	gemIndices := allIndices[:numGem]
	genIndices := allIndices[:numGen]

	return entries, allIndices, gemIndices, genIndices
}

// rebuildIndex refreshes the cached candidate index and partitions if necessary.
func (a *Adapter) rebuildIndex(force bool) {
	if a.st == nil {
		a.indexMu.Lock()
		a.cachedEntries = nil
		a.cachedFilterAll = nil
		a.cachedFilterGem = nil
		a.cachedFilterGen = nil
		a.indexMu.Unlock()
		a.mu.Lock()
		a.idToLink = make(map[string]string)
		a.linkToID = make(map[string]string)
		a.mu.Unlock()
		return
	}

	a.indexMu.RLock()
	hasCache := a.cachedEntries != nil
	lastRev := a.lastIndexRev
	lastTime := a.lastIndexTime
	cachedCount := len(a.cachedEntries)
	throttle := a.indexThrottleInterval
	a.indexMu.RUnlock()

	currentRev := a.st.Revision()
	count := a.st.Count()
	now := time.Now()
	if throttle < 0 {
		throttle = 0
	}

	// Fast path: if cache is valid and up to date, or throttled, return immediately.
	if !force && hasCache && cachedCount == count {
		if currentRev == lastRev || (throttle > 0 && now.Sub(lastTime) < throttle) {
			return
		}
	}

	// Single-flight coordination: only one goroutine rebuilds at a time.
	// If another goroutine is actively rebuilding:
	// - If we already have a valid cache and this is not a forced request, return immediately
	//   and allow the caller to serve from the existing valid cache without blocking.
	// - If force is true or hasCache is false, wait for the rebuild to complete.
	if !a.rebuildMu.TryLock() {
		if hasCache && !force {
			return
		}
		a.rebuildMu.Lock()
	}
	defer a.rebuildMu.Unlock()

	// Re-check conditions after acquiring rebuildMu in case another goroutine just refreshed.
	a.indexMu.RLock()
	hasCache = a.cachedEntries != nil
	lastRev = a.lastIndexRev
	lastTime = a.lastIndexTime
	cachedCount = len(a.cachedEntries)
	a.indexMu.RUnlock()

	currentRev = a.st.Revision()
	count = a.st.Count()
	now = time.Now()

	if !force && hasCache && cachedCount == count {
		if currentRev == lastRev || (throttle > 0 && now.Sub(lastTime) < throttle) {
			return
		}
	}

	if a.rebuildInFlightHook != nil {
		a.rebuildInFlightHook()
	}

	entries, all, gem, gen := a.computeCandidateIndex()

	a.indexMu.Lock()
	a.cachedEntries = entries
	a.cachedFilterAll = all
	a.cachedFilterGem = gem
	a.cachedFilterGen = gen
	a.lastIndexTime = now
	a.lastIndexRev = currentRev
	a.indexMu.Unlock()
}

func (a *Adapter) filteredIndicesLocked(filter viewmodel.FilterMode) []int {
	switch filter {
	case viewmodel.FilterGemini:
		return a.cachedFilterGem
	case viewmodel.FilterGeneric:
		return a.cachedFilterGen
	default:
		return a.cachedFilterAll
	}
}

func (a *Adapter) materializeRowViewModel(entry store.CandidateIndexEntry, opaqueID string, mode country.Mode) viewmodel.CandidateRowViewModel {
	canonical := entry.CanonicalLink
	activeLink := entry.ActiveLink
	if activeLink == "" {
		activeLink = canonical
	}

	proto := ExtractProtocol(activeLink)
	endpoint := ExtractEndpoint(activeLink)
	remark := country.FormatRemark(ExtractRemark(activeLink), mode)

	status := "PEND"
	if entry.LatestStatus != "" {
		switch entry.LatestStatus {
		case store.StatusPassed:
			status = "PASS"
		case store.StatusFailed:
			if entry.TransportOK || (entry.NetworkHealthy && entry.LatestCategory.IsTargetSpecific()) {
				switch entry.LatestCategory {
				case store.ErrRegionBlocked:
					status = "BLOCKED"
				case store.ErrTargetDenied:
					status = "DENIED"
				default:
					status = "FAIL"
				}
			} else {
				status = "FAIL"
			}
		case store.StatusInconclusive:
			status = "INCON"
		}
	}

	scoreStr := "---"
	if entry.HistoryCount > 0 {
		scoreStr = fmt.Sprintf("%.2f", entry.Score)
	}

	latStr := "---"
	if entry.HasPassed && entry.LastPassedLatency > 0 {
		latStr = entry.LastPassedLatency.Round(time.Millisecond).String()
	}

	glyphs := ""
	if entry.HistoryCapacity > 0 {
		glyphs = FormatHistoryGlyphsFromStatuses(entry.HistoryStatuses, entry.HistoryCount, entry.HistoryCapacity)
	}

	return viewmodel.CandidateRowViewModel{
		ID:                     opaqueID,
		Protocol:               proto,
		Endpoint:               endpoint,
		Remark:                 remark,
		Status:                 status,
		ScoreFormatted:         scoreStr,
		LatencyFormatted:       latStr,
		HistoryGlyphs:          glyphs,
		Servable:               entry.Servable,
		NetworkHealthy:         entry.NetworkHealthy,
		TransportOK:            entry.TransportOK,
		TransportEvidenceKnown: entry.TransportEvidenceKnown,
		TransportLatency:       entry.TransportLatency,
		HasPassed:              entry.HasPassed,
	}
}

// CandidateRowsWindow materializes CandidateRowViewModels ONLY for the specified slice window.
func (a *Adapter) CandidateRowsWindow(filter viewmodel.FilterMode, offset, limit int) []viewmodel.CandidateRowViewModel {
	a.indexMu.RLock()
	hasCache := a.cachedEntries != nil
	throttle := a.indexThrottleInterval
	a.indexMu.RUnlock()

	if !hasCache || throttle <= 0 {
		a.rebuildIndex(false)
	}

	a.indexMu.RLock()
	indices := a.filteredIndicesLocked(filter)
	total := len(indices)

	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	if offset >= end {
		a.indexMu.RUnlock()
		return nil
	}

	window := indices[offset:end]
	entries := make([]store.CandidateIndexEntry, len(window))
	for i, idx := range window {
		entries[i] = a.cachedEntries[idx]
	}
	a.indexMu.RUnlock()

	ids := make([]string, len(entries))
	a.mu.Lock()
	mode := a.flagMode
	for i, entry := range entries {
		canonical := entry.CanonicalLink
		opaqueID, exists := a.linkToID[canonical]
		if !exists {
			a.idCounter++
			opaqueID = fmt.Sprintf("cand-%d", a.idCounter)
			a.linkToID[canonical] = opaqueID
			a.idToLink[opaqueID] = canonical
		}
		ids[i] = opaqueID
	}
	a.mu.Unlock()

	out := make([]viewmodel.CandidateRowViewModel, len(entries))
	for i, entry := range entries {
		out[i] = a.materializeRowViewModel(entry, ids[i], mode)
	}
	return out
}

// CandidateDetail resolves an opaque presentation ID to its authoritative CandidateRecord
// and returns an immutable CandidateDetailViewModel with mandatory credential masking.
func (a *Adapter) CandidateDetail(opaqueID string) (viewmodel.CandidateDetailViewModel, bool) {
	a.mu.RLock()
	canonical, ok := a.idToLink[opaqueID]
	mode := a.flagMode
	a.mu.RUnlock()

	if !ok || a.st == nil {
		return viewmodel.CandidateDetailViewModel{}, false
	}

	rec, ok := a.st.GetRecord(canonical)
	if !ok || rec == nil {
		return viewmodel.CandidateDetailViewModel{}, false
	}

	servable, gateReason := a.st.ServabilityGate(rec)
	networkHealthy := a.st.IsNetworkHealthyRecord(rec)

	activeLink := rec.ActiveLink
	if activeLink == "" {
		activeLink = rec.CanonicalLink
	}

	maskedLink := MaskLink(activeLink)
	proto := ExtractProtocol(activeLink)
	endpoint := ExtractEndpoint(activeLink)
	host, port, path, sni := ExtractConnectionParams(activeLink)
	remark := country.FormatRemark(ExtractRemark(activeLink), mode)

	statusStr := "PENDING"
	if rec.Latest.Status != "" {
		statusStr = string(rec.Latest.Status)
	}

	categoryStr := ""
	if rec.Latest.Category != "" {
		categoryStr = string(rec.Latest.Category)
	}

	scoreStr := "---"
	if rec.History.Count > 0 {
		scoreStr = fmt.Sprintf("%.2f", rec.Score)
	}

	provenLatStr := "---"
	if rec.HasPassed && rec.LastPassedLatency > 0 {
		provenLatStr = rec.LastPassedLatency.Round(time.Millisecond).String()
	}

	chronological := rec.History.ChronologicalSamples()
	samples := make([]viewmodel.SampleViewModel, 0, len(chronological))
	now := time.Now()
	for i, s := range chronological {
		ageStr := "---"
		if !s.TestedAt.IsZero() {
			d := now.Sub(s.TestedAt).Round(time.Second)
			if d < time.Minute {
				ageStr = fmt.Sprintf("%ds ago", int(d.Seconds()))
			} else {
				ageStr = fmt.Sprintf("%dm ago", int(d.Minutes()))
			}
		}

		samples = append(samples, viewmodel.SampleViewModel{
			Index:      i + 1,
			Age:        ageStr,
			Status:     string(s.Status),
			Category:   string(s.Category),
			StatusCode: s.StatusCode,
			Latency:    s.Latency,
			Attempts:   s.Attempts,
		})
	}

	warnings := make([]string, len(rec.Latest.Warnings))
	copy(warnings, rec.Latest.Warnings)

	return viewmodel.CandidateDetailViewModel{
		ID:                     opaqueID,
		MaskedLink:             maskedLink,
		Protocol:               proto,
		Endpoint:               endpoint,
		Host:                   host,
		Port:                   port,
		Path:                   path,
		SNI:                    sni,
		Remark:                 remark,
		Status:                 statusStr,
		Category:               categoryStr,
		Reason:                 rec.Latest.Reason,
		StatusCode:             rec.Latest.StatusCode,
		Score:                  rec.Score,
		ScoreFormatted:         scoreStr,
		Servable:               servable,
		ServabilityGate:        gateReason,
		NetworkHealthy:         networkHealthy,
		TransportEvidenceKnown: rec.Latest.TransportEvidenceKnown,
		TransportOK:            rec.Latest.TransportOK,
		TransportLatency:       rec.Latest.TransportLatency,
		HasPassed:              rec.HasPassed,
		ProvenLatencyFormatted: provenLatStr,
		AbsentCycles:           rec.AbsentCycles,
		TestedAt:               rec.Latest.TestedAt,
		Latency:                rec.Latest.Latency,
		Attempts:               rec.Latest.Attempts,
		Warnings:               warnings,
		Samples:                samples,
	}, true
}

// ActiveLink retrieves the raw active link corresponding to an opaque presentation ID.
// This is used exclusively for clipboard actions.
func (a *Adapter) ActiveLink(opaqueID string) (string, bool) {
	a.mu.RLock()
	canonical, ok := a.idToLink[opaqueID]
	a.mu.RUnlock()

	if !ok || a.st == nil {
		return "", false
	}

	rec, ok := a.st.GetRecord(canonical)
	if !ok || rec == nil {
		return "", false
	}

	if rec.ActiveLink != "" {
		return rec.ActiveLink, true
	}
	return rec.CanonicalLink, true
}

// Logs returns a LogViewModel populated from the RingLogHandler at the active minimum log level.
func (a *Adapter) Logs() viewmodel.LogViewModel {
	a.mu.RLock()
	minLvl := a.logLevel
	offset := a.scrollOffset
	follow := a.followMode
	a.mu.RUnlock()

	if a.ringHandler == nil {
		return viewmodel.LogViewModel{
			MinLevel:     minLvl,
			ScrollOffset: offset,
			FollowMode:   follow,
		}
	}

	records := a.ringHandler.RecordsByLevel(minLvl)
	lines := make([]string, len(records))
	for i, r := range records {
		lines[i] = logging.FormatRecord(r)
	}

	return viewmodel.LogViewModel{
		Lines:        lines,
		ScrollOffset: offset,
		MinLevel:     minLvl,
		TotalCount:   a.ringHandler.Len(),
		FollowMode:   follow,
	}
}

// SetMinLogLevel sets the active log filtering level.
func (a *Adapter) SetMinLogLevel(l slog.Level) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.logLevel = l
}

// SetFlagMode sets the presentation mode for country flags and marks presentation dirty.
func (a *Adapter) SetFlagMode(m country.Mode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.flagMode = m
	atomic.StoreInt32(&a.dirty, 1)
}

// FlagMode returns the active country flag presentation mode.
func (a *Adapter) FlagMode() country.Mode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.flagMode
}

// CycleMinLogLevel cycles the active minimum log level: DEBUG -> INFO -> WARN -> ERROR -> DEBUG.
func (a *Adapter) CycleMinLogLevel() slog.Level {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch a.logLevel {
	case slog.LevelDebug:
		a.logLevel = slog.LevelInfo
	case slog.LevelInfo:
		a.logLevel = slog.LevelWarn
	case slog.LevelWarn:
		a.logLevel = slog.LevelError
	default:
		a.logLevel = slog.LevelDebug
	}
	return a.logLevel
}

func (a *Adapter) buildSnapshot(filter viewmodel.FilterMode, forceIndex bool) viewmodel.SnapshotViewModel {
	a.rebuildIndex(forceIndex)
	a.indexMu.RLock()
	total := len(a.filteredIndicesLocked(filter))
	a.indexMu.RUnlock()

	rows := a.CandidateRowsWindow(filter, 0, 50)

	return viewmodel.SnapshotViewModel{
		Header:    a.Header(),
		Rows:      rows,
		TotalRows: total,
		Logs:      a.Logs(),
	}
}

// Snapshot returns a SnapshotViewModel assembled from authoritative reads (Header, windowed Rows, and Logs).
func (a *Adapter) Snapshot(filter viewmodel.FilterMode) viewmodel.SnapshotViewModel {
	return a.buildSnapshot(filter, true)
}

// PollSnapshot returns updated presentation viewmodels if state has been invalidated.
// If not invalidated, it returns updated=false without building ViewModels.
func (a *Adapter) PollSnapshot(filter viewmodel.FilterMode) (viewmodel.SnapshotViewModel, bool) {
	if !a.CheckAndResetDirty() {
		return viewmodel.SnapshotViewModel{}, false
	}
	return a.buildSnapshot(filter, false), true
}

// CycleLogLevel cycles the minimum log level and returns the updated LogViewModel and level name.
func (a *Adapter) CycleLogLevel() (viewmodel.LogViewModel, string) {
	lvl := a.CycleMinLogLevel()
	return a.Logs(), lvl.String()
}

// CopyCandidateLink resolves an opaque presentation ID and copies the unmasked link
// to the system clipboard without exposing cleartext credentials to presentation callers.
func (a *Adapter) CopyCandidateLink(opaqueID string) error {
	rawLink, ok := a.ActiveLink(opaqueID)
	if !ok {
		return fmt.Errorf("candidate not found: %s", opaqueID)
	}
	return clipboard.CopyToClipboard(rawLink)
}

// SetServices injects application services into the Adapter for Configuration Center presentation queries.
func (a *Adapter) SetServices(cfgSvc *config.Service, srcSvc *source.Service, pubSvc *publisher.Service, schedCtrl *scheduler.ControlService) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.configSvc = cfgSvc
	a.sourceSvc = srcSvc
	a.publishSvc = pubSvc
	a.schedulerCtrl = schedCtrl
	atomic.StoreInt32(&a.dirty, 1)
}

// ConfigCenter queries application services and builds a decoupled presentation ViewModel
// for all configuration categories.
func (a *Adapter) ConfigCenter() viewmodel.ConfigCenterViewModel {
	a.mu.RLock()
	cfgSvc := a.configSvc
	srcSvc := a.sourceSvc
	pubSvc := a.publishSvc
	schedCtrl := a.schedulerCtrl
	flagMode := a.flagMode

	eventCfg := a.lastCfg
	eventPubRunning := a.pubRunning
	eventLastPublished := a.lastPublished
	eventLastPublishError := a.lastPublishError
	eventPublishCount := a.publishCount
	eventPublishFailCount := a.publishFailCount
	a.mu.RUnlock()

	var cfg config.Config
	if cfgSvc != nil {
		cfg = cfgSvc.Get()
	} else {
		cfg = eventCfg
	}

	categories := make([]viewmodel.ConfigCategoryViewModel, viewmodel.ConfigCategoryCount)

	// 1. General Category
	generalItems := []viewmodel.ConfigItemViewModel{
		{Label: "Listen Address", Value: valueOrFallback(cfg.Serve.Listen, "(not set)")},
		{Label: "Subscription Path", Value: valueOrFallback(cfg.Serve.Path, "(not set)")},
		{Label: "Subscription Format", Value: valueOrFallback(cfg.Serve.Format, "raw")},
		{Label: "Country Flag Mode", Value: valueOrFallback(cfg.FlagMode, string(flagMode))},
		{Label: "State File", Value: valueOrFallback(cfg.StateFile, "(none)")},
		{Label: "Headless Mode", Value: fmt.Sprintf("%t", cfg.Headless)},
	}
	categories[viewmodel.CategoryGeneral] = viewmodel.ConfigCategoryViewModel{
		Category: viewmodel.CategoryGeneral,
		Name:     viewmodel.CategoryGeneral.Name(),
		Items:    generalItems,
	}

	// 2. Sources Category
	var sources []config.SourceItem
	if srcSvc != nil {
		sources = srcSvc.List()
	} else {
		sources = cfg.Sources
	}
	var sourceItems []viewmodel.ConfigItemViewModel
	var sourceVMs []viewmodel.SourceItemViewModel
	if len(sources) == 0 {
		sourceItems = append(sourceItems, viewmodel.ConfigItemViewModel{
			Label: "Status",
			Value: "No subscription sources configured",
		})
	} else {
		sourceItems = append(sourceItems, viewmodel.ConfigItemViewModel{
			Label: "Total Sources",
			Value: fmt.Sprintf("%d configured", len(sources)),
		})
		for i, src := range sources {
			state := "enabled"
			if !src.Enabled {
				state = "disabled"
			}
			name := src.Name
			if name == "" {
				name = fmt.Sprintf("Source #%d", i+1)
			}
			sanURL := publisher.SanitizeURL(src.URL)
			sourceItems = append(sourceItems, viewmodel.ConfigItemViewModel{
				Label: name,
				Value: fmt.Sprintf("[%s] %s", state, sanURL),
			})
			// Telemetry semantics (Issue #17):
			// SourceItemViewModel defines CandidateCount, HasCount, and StatusMsg for
			// presentation "if known". The current Scheduler, EventBus, and Store pipeline
			// tracks only aggregate cycle metrics (events.CycleFinished, events.CandidatesLoaded)
			// without authoritative per-source candidate attribution or fetch error history.
			// In accordance with repository boundaries prohibiting speculative state in presentation
			// layers, HasCount remains false and StatusMsg remains "" until an authoritative
			// per-source telemetry seam is scoped and implemented.
			sourceVMs = append(sourceVMs, viewmodel.SourceItemViewModel{
				ID:             src.ID,
				Name:           name,
				URL:            sanURL,
				Enabled:        src.Enabled,
				CandidateCount: 0,
				HasCount:       false,
				StatusMsg:      "",
			})
		}
	}
	categories[viewmodel.CategorySources] = viewmodel.ConfigCategoryViewModel{
		Category: viewmodel.CategorySources,
		Name:     viewmodel.CategorySources.Name(),
		Items:    sourceItems,
		Sources:  sourceVMs,
	}

	// 3. Testing Category
	maxRetriesStr := fmt.Sprintf("%d", cfg.Test.MaxRetries)
	if cfg.Test.MaxRetriesRaw == nil {
		maxRetriesStr += " (default)"
	}
	rateLimitStr := "unlimited"
	if cfg.Test.RateLimitRPS > 0 {
		rateLimitStr = fmt.Sprintf("%d rps", cfg.Test.RateLimitRPS)
	}
	testingItems := []viewmodel.ConfigItemViewModel{
		{Label: "Test Timeout", Value: valueOrFallback(cfg.Test.TimeoutRaw, "(default)")},
		{Label: "Concurrency", Value: fmt.Sprintf("%d workers", cfg.Test.Concurrency)},
		{Label: "Max Retries", Value: maxRetriesStr},
		{Label: "Retry Backoff", Value: valueOrFallback(cfg.Test.RetryBackoffRaw, "(default)")},
		{Label: "Health Check URL", Value: valueOrFallback(publisher.SanitizeURL(cfg.Test.HealthURL), "(none)")},
		{Label: "Dial Timeout", Value: valueOrFallback(cfg.Test.DialTimeoutRaw, "(default)")},
		{Label: "Max Inconclusive Cycles", Value: fmt.Sprintf("%d", cfg.Test.MaxInconclusiveCycles)},
		{Label: "Rate Limit RPS", Value: rateLimitStr},
	}
	categories[viewmodel.CategoryTesting] = viewmodel.ConfigCategoryViewModel{
		Category: viewmodel.CategoryTesting,
		Name:     viewmodel.CategoryTesting.Name(),
		Items:    testingItems,
	}

	// 4. Gemini Category
	blockPhrasesStr := "(none)"
	if len(cfg.Test.Gemini.BlockPhrases) > 0 {
		blockPhrasesStr = strings.Join(cfg.Test.Gemini.BlockPhrases, ", ")
	}
	geminiItems := []viewmodel.ConfigItemViewModel{
		{Label: "Gemini Target URL", Value: valueOrFallback(publisher.SanitizeURL(cfg.Test.Gemini.URL), "(none)")},
		{Label: "Block Phrases", Value: blockPhrasesStr},
	}
	categories[viewmodel.CategoryGemini] = viewmodel.ConfigCategoryViewModel{
		Category: viewmodel.CategoryGemini,
		Name:     viewmodel.CategoryGemini.Name(),
		Items:    geminiItems,
	}

	// 5. Scheduler Category
	probeLimitStr := "unlimited"
	if cfg.ProbeLimit > 0 {
		probeLimitStr = fmt.Sprintf("%d candidates", cfg.ProbeLimit)
	}
	schedulerItems := []viewmodel.ConfigItemViewModel{
		{Label: "Fetch Interval", Value: valueOrFallback(cfg.FetchIntervalRaw, "(not set)")},
		{Label: "Probe Limit", Value: probeLimitStr},
	}
	if schedCtrl != nil {
		schedStatus := schedCtrl.Status()
		daemonStatus := "Stopped"
		if schedStatus.Running {
			daemonStatus = "Running"
		}
		schedulerItems = append(schedulerItems, viewmodel.ConfigItemViewModel{
			Label: "Daemon Status",
			Value: daemonStatus,
		})
		cycleActive := "No"
		if schedStatus.CycleActive {
			cycleActive = "Yes (probes in progress)"
		}
		schedulerItems = append(schedulerItems, viewmodel.ConfigItemViewModel{
			Label: "Cycle Active",
			Value: cycleActive,
		})
		lastCycleStr := "Never"
		if !schedStatus.LastCycleStart.IsZero() {
			lastCycleStr = schedStatus.LastCycleStart.Format("15:04:05")
		}
		schedulerItems = append(schedulerItems, viewmodel.ConfigItemViewModel{
			Label: "Last Cycle Start",
			Value: lastCycleStr,
		})
		if schedStatus.LastDuration > 0 {
			schedulerItems = append(schedulerItems, viewmodel.ConfigItemViewModel{
				Label: "Last Duration",
				Value: schedStatus.LastDuration.Round(time.Millisecond).String(),
			})
		}
	} else {
		schedulerItems = append(schedulerItems, viewmodel.ConfigItemViewModel{
			Label: "Daemon Status",
			Value: "Not initialized",
		})
	}
	categories[viewmodel.CategoryScheduler] = viewmodel.ConfigCategoryViewModel{
		Category: viewmodel.CategoryScheduler,
		Name:     viewmodel.CategoryScheduler.Name(),
		Items:    schedulerItems,
	}

	// 6. Publishing Category
	cleanRepo := publisher.SanitizeMessage(cfg.Publishing.Repository)
	cleanRemoteURL := publisher.SanitizeURL(cfg.Publishing.RemoteURL)
	publishingItems := []viewmodel.ConfigItemViewModel{
		{Label: "Publishing Enabled", Value: fmt.Sprintf("%t", cfg.Publishing.Enabled)},
		{Label: "Target Repository", Value: valueOrFallback(cleanRepo, "(none)")},
		{Label: "Target Branch", Value: valueOrFallback(cfg.Publishing.Branch, "(default)")},
		{Label: "Remote URL", Value: valueOrFallback(cleanRemoteURL, "(none)")},
	}
	if pubSvc != nil {
		pStatus := pubSvc.Status()
		pubRun := "Idle"
		if pStatus.Running {
			pubRun = "Publishing..."
		}
		publishingItems = append(publishingItems, viewmodel.ConfigItemViewModel{
			Label: "Publish Status",
			Value: pubRun,
		})
		lastPub := "Never"
		if !pStatus.LastPublished.IsZero() {
			lastPub = pStatus.LastPublished.Format("15:04:05")
		}
		publishingItems = append(publishingItems, viewmodel.ConfigItemViewModel{
			Label: "Last Published",
			Value: lastPub,
		})
		publishingItems = append(publishingItems, viewmodel.ConfigItemViewModel{
			Label: "Publish Count",
			Value: fmt.Sprintf("%d succeeded, %d failed", pStatus.PublishCount, pStatus.FailCount),
		})
		if pStatus.LastError != "" {
			publishingItems = append(publishingItems, viewmodel.ConfigItemViewModel{
				Label: "Last Error",
				Value: publisher.SanitizeMessage(pStatus.LastError),
			})
		}
	} else if eventPublishCount > 0 || eventPublishFailCount > 0 || eventPubRunning || eventLastPublishError != "" || !eventLastPublished.IsZero() {
		pubRun := "Idle"
		if eventPubRunning {
			pubRun = "Publishing..."
		}
		publishingItems = append(publishingItems, viewmodel.ConfigItemViewModel{
			Label: "Publish Status",
			Value: pubRun,
		})
		lastPub := "Never"
		if !eventLastPublished.IsZero() {
			lastPub = eventLastPublished.Format("15:04:05")
		}
		publishingItems = append(publishingItems, viewmodel.ConfigItemViewModel{
			Label: "Last Published",
			Value: lastPub,
		})
		publishingItems = append(publishingItems, viewmodel.ConfigItemViewModel{
			Label: "Publish Count",
			Value: fmt.Sprintf("%d succeeded, %d failed", eventPublishCount, eventPublishFailCount),
		})
		if eventLastPublishError != "" {
			publishingItems = append(publishingItems, viewmodel.ConfigItemViewModel{
				Label: "Last Error",
				Value: publisher.SanitizeMessage(eventLastPublishError),
			})
		}
	} else {
		publishingItems = append(publishingItems, viewmodel.ConfigItemViewModel{
			Label: "Publish Status",
			Value: "Not initialized",
		})
	}
	categories[viewmodel.CategoryPublishing] = viewmodel.ConfigCategoryViewModel{
		Category: viewmodel.CategoryPublishing,
		Name:     viewmodel.CategoryPublishing.Name(),
		Items:    publishingItems,
	}

	return viewmodel.ConfigCenterViewModel{
		Categories: categories,
	}
}

func valueOrFallback(val, fallback string) string {
	if strings.TrimSpace(val) == "" {
		return fallback
	}
	return val
}

// ToggleSource flips the enabled state of the specified source via SourceService.
func (a *Adapter) ToggleSource(id string) error {
	a.mu.RLock()
	srcSvc := a.sourceSvc
	a.mu.RUnlock()
	if srcSvc == nil {
		return fmt.Errorf("source service unavailable")
	}

	item, err := srcSvc.Get(id)
	if err != nil {
		return err
	}
	if err := srcSvc.SetEnabled(id, !item.Enabled); err != nil {
		return err
	}
	atomic.StoreInt32(&a.dirty, 1)
	return nil
}

// AddSource adds a new subscription source via SourceService.
func (a *Adapter) AddSource(rawURL string, name string) error {
	a.mu.RLock()
	srcSvc := a.sourceSvc
	a.mu.RUnlock()
	if srcSvc == nil {
		return fmt.Errorf("source service unavailable")
	}

	if _, err := srcSvc.AddURL(rawURL, name); err != nil {
		return err
	}
	atomic.StoreInt32(&a.dirty, 1)
	return nil
}

// UpdateSource mutates an existing source's alias and/or URL via SourceService.
// If rawURL matches the existing sanitized URL, existing credentials are preserved.
func (a *Adapter) UpdateSource(id string, rawURL string, name string) error {
	a.mu.RLock()
	srcSvc := a.sourceSvc
	a.mu.RUnlock()
	if srcSvc == nil {
		return fmt.Errorf("source service unavailable")
	}

	_, err := srcSvc.Update(id, func(item *source.SourceItem) error {
		if rawURL != "" && rawURL != item.URL && rawURL != publisher.SanitizeURL(item.URL) {
			item.URL = rawURL
		}
		if name != "" {
			item.Name = name
		}
		return nil
	})
	if err != nil {
		return err
	}
	atomic.StoreInt32(&a.dirty, 1)
	return nil
}

// DeleteSource deletes a subscription source by ID via SourceService.
func (a *Adapter) DeleteSource(id string) error {
	a.mu.RLock()
	srcSvc := a.sourceSvc
	a.mu.RUnlock()
	if srcSvc == nil {
		return fmt.Errorf("source service unavailable")
	}

	if err := srcSvc.Remove(id); err != nil {
		return err
	}
	atomic.StoreInt32(&a.dirty, 1)
	return nil
}
