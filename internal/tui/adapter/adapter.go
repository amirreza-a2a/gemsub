package adapter

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"gemsub/internal/clipboard"
	"gemsub/internal/events"
	"gemsub/internal/logging"
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
	indexMu               sync.Mutex
	lastIndexTime         time.Time
	lastIndexRev          uint64
	indexThrottleInterval time.Duration
	cachedEntries         []store.CandidateIndexEntry
	cachedFilterAll       []int
	cachedFilterGem       []int
	cachedFilterGen       []int
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
	a.indexMu.Lock()
	defer a.indexMu.Unlock()

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
	a.indexMu.Lock()
	a.rebuildIndexLocked(false)
	servableCount := len(a.cachedFilterGem)
	genericServableCount := len(a.cachedFilterGen)
	a.indexMu.Unlock()

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

// rebuildIndexLocked refreshes the cached candidate index and partitions if necessary.
// Must be called with a.indexMu locked.
func (a *Adapter) rebuildIndexLocked(force bool) {
	if a.st == nil {
		a.cachedEntries = nil
		a.cachedFilterAll = nil
		a.cachedFilterGem = nil
		a.cachedFilterGen = nil
		a.mu.Lock()
		a.idToLink = make(map[string]string)
		a.linkToID = make(map[string]string)
		a.mu.Unlock()
		return
	}

	currentRev := a.st.Revision()
	count := a.st.Count()
	now := time.Now()
	throttle := a.indexThrottleInterval
	if throttle < 0 {
		throttle = 0
	}
	if !force && a.cachedEntries != nil && len(a.cachedEntries) == count {
		if currentRev == a.lastIndexRev || (throttle > 0 && now.Sub(a.lastIndexTime) < throttle) {
			return
		}
	}

	entries := a.st.CandidateIndex()

	// Prune orphaned presentation IDs for candidates evicted from Store (P1 fix).
	// Surviving candidates retain their stable opaque IDs; evicted candidates are removed.
	activeLinks := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		activeLinks[e.CanonicalLink] = struct{}{}
	}
	a.mu.Lock()
	for link, id := range a.linkToID {
		if _, ok := activeLinks[link]; !ok {
			delete(a.linkToID, link)
			delete(a.idToLink, id)
		}
	}
	a.mu.Unlock()

	// Deterministic Candidate Ordering Policy (Ticket 25):
	// 1. Tier 1: Gemini-servable candidates (Servable == true).
	// 2. Tier 2: Generic-servable candidates (NetworkHealthy == true && Servable == false).
	// 3. Tier 3: Unservable candidates.
	// 4. Within each tier, proven candidates (HasPassed == true) before unproven candidates.
	// 5. Reliability Score descending.
	// 6. Effective latency ascending (LastPassedLatency, then TransportLatency).
	// 7. Stable internal identity ascending as deterministic tie-breaker.
	tier := func(e store.CandidateIndexEntry) int {
		if e.Servable {
			return 1
		}
		if e.NetworkHealthy {
			return 2
		}
		return 3
	}

	sort.Slice(entries, func(i, j int) bool {
		tI := tier(entries[i])
		tJ := tier(entries[j])
		if tI != tJ {
			return tI < tJ
		}
		if entries[i].HasPassed != entries[j].HasPassed {
			return entries[i].HasPassed
		}
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		latI := entries[i].LastPassedLatency
		if latI == 0 && entries[i].TransportLatency > 0 {
			latI = entries[i].TransportLatency
		}
		latJ := entries[j].LastPassedLatency
		if latJ == 0 && entries[j].TransportLatency > 0 {
			latJ = entries[j].TransportLatency
		}
		if latI != latJ {
			if latI == 0 {
				return false
			}
			if latJ == 0 {
				return true
			}
			return latI < latJ
		}
		return entries[i].CanonicalLink < entries[j].CanonicalLink
	})

	allIndices := make([]int, len(entries))
	gemIndices := make([]int, 0, len(entries))
	genIndices := make([]int, 0, len(entries))

	for i, e := range entries {
		allIndices[i] = i
		if e.Servable {
			gemIndices = append(gemIndices, i)
		}
		if e.NetworkHealthy {
			genIndices = append(genIndices, i)
		}
	}

	a.cachedEntries = entries
	a.cachedFilterAll = allIndices
	a.cachedFilterGem = gemIndices
	a.cachedFilterGen = genIndices
	a.lastIndexTime = now
	a.lastIndexRev = currentRev
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

func (a *Adapter) materializeRowViewModelLocked(entry store.CandidateIndexEntry) viewmodel.CandidateRowViewModel {
	canonical := entry.CanonicalLink
	opaqueID, exists := a.linkToID[canonical]
	if !exists {
		a.idCounter++
		opaqueID = fmt.Sprintf("cand-%d", a.idCounter)
		a.linkToID[canonical] = opaqueID
		a.idToLink[opaqueID] = canonical
	}

	activeLink := entry.ActiveLink
	if activeLink == "" {
		activeLink = canonical
	}

	proto := ExtractProtocol(activeLink)
	endpoint := ExtractEndpoint(activeLink)
	remark := country.FormatRemark(ExtractRemark(activeLink), a.flagMode)

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
	if a.st != nil {
		if rec, ok := a.st.GetRecord(canonical); ok && rec != nil {
			glyphs = FormatHistoryGlyphs(rec.History.ChronologicalSamples(), rec.History.Capacity)
		}
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
	a.indexMu.Lock()
	defer a.indexMu.Unlock()

	a.rebuildIndexLocked(false)
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
		return nil
	}

	window := indices[offset:end]
	out := make([]viewmodel.CandidateRowViewModel, 0, len(window))

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, idx := range window {
		entry := a.cachedEntries[idx]
		out = append(out, a.materializeRowViewModelLocked(entry))
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
	a.indexMu.Lock()
	a.rebuildIndexLocked(forceIndex)
	total := len(a.filteredIndicesLocked(filter))
	a.indexMu.Unlock()

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
