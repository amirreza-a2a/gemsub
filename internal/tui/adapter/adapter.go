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
}

// New creates an unstarted Adapter.
func New(st *store.Store, bus *events.EventBus, ring *logging.RingLogHandler) *Adapter {
	var initRev uint64
	var initCycleCount int
	if st != nil {
		initRev = st.Revision()
		initCycleCount = st.Stats().CycleCount
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
			a.lastCompletedCycleCount = a.st.Stats().CycleCount
		}
		atomic.StoreInt32(&a.dirty, 1)
	}
}

// reconcileCycleStateLocked detects if Store has completed a cycle whose CycleFinished event
// was lost, and reconciles cycle status and metrics from authoritative Store state.
// Caller must hold a.mu Lock.
func (a *Adapter) reconcileCycleStateLocked() bool {
	if a.st == nil {
		return false
	}
	stats := a.st.Stats()
	if a.cycleStatus == viewmodel.CycleRunning && stats.CycleCount > a.lastCompletedCycleCount {
		a.cycleStatus = viewmodel.CycleIdle
		a.lastCompletedCycleCount = stats.CycleCount

		if stats.CycleCount > 0 {
			completedCycleID := uint64(stats.CycleCount - 1)
			var passed, failed, inconclusive int
			for _, snap := range a.st.Snapshots() {
				if lastSample, ok := snap.Record.History.Last(); ok && lastSample.CycleID == completedCycleID {
					switch lastSample.Status {
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
	return false
}

// CheckAndResetDirty returns true if state has been invalidated since last check,
// checking both EventBus notifications and the authoritative Store revision.
func (a *Adapter) CheckAndResetDirty() bool {
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
	}
	return dirty
}

// MarkDirty manually flags the presentation state as needing refresh.
func (a *Adapter) MarkDirty() {
	atomic.StoreInt32(&a.dirty, 1)
}

// Header returns the current HeaderViewModel constructed from authoritative Store.Stats()
// and ephemeral cycle lifecycle metrics. Pass/Fail/Incon counts represent the active cycle only.
func (a *Adapter) Header() viewmodel.HeaderViewModel {
	a.mu.Lock()
	if a.st != nil {
		a.reconcileCycleStateLocked()
	}
	cycleStatus := a.cycleStatus
	progCurrent := a.progressCurrent
	progTotal := a.progressTotal
	cyclePassed := a.cyclePassed
	cycleFailed := a.cycleFailed
	cycleIncon := a.cycleInconclusive
	a.mu.Unlock()

	var stats store.Stats
	if a.st != nil {
		stats = a.st.Stats()
	}

	return viewmodel.HeaderViewModel{
		CycleCount:        stats.CycleCount,
		LastCycle:         stats.LastCycle,
		CycleStatus:       cycleStatus,
		ProgressCurrent:   progCurrent,
		ProgressTotal:     progTotal,
		TotalCandidates:   stats.Total,
		PassedCount:       cyclePassed,
		FailedCount:       cycleFailed,
		InconclusiveCount: cycleIncon,
		ServableCount:     stats.Servable,
		GenericServableCount: stats.GenericServable,
	}
}

// StoreStats returns the authoritative Store aggregate statistics.
func (a *Adapter) StoreStats() store.Stats {
	if a.st != nil {
		return a.st.Stats()
	}
	return store.Stats{}
}

// CandidateRows queries the authoritative Store, applies the canonical candidate ranking
// policy, assigns opaque presentation IDs, and returns a pre-sorted slice of CandidateRowViewModel.
func (a *Adapter) CandidateRows(filter viewmodel.FilterMode) []viewmodel.CandidateRowViewModel {
	if a.st == nil {
		return nil
	}

	snaps := a.st.Snapshots()

	// Deterministic Candidate Ordering Policy:
	// 1. Tier 1: Gemini-servable candidates (Servable == true).
	// 2. Tier 2: Generic-servable candidates (NetworkHealthy == true && Servable == false).
	// 3. Tier 3: Unservable candidates.
	// 4. Within each tier, proven candidates (HasPassed == true) before unproven candidates.
	// 5. Reliability Score descending.
	// 6. Effective latency ascending (LastPassedLatency, then TransportLatency).
	// 7. Stable internal identity ascending as deterministic tie-breaker.
	tier := func(snap store.CandidateSnapshot) int {
		if snap.Servable {
			return 1
		}
		if snap.NetworkHealthy {
			return 2
		}
		return 3
	}

	sort.Slice(snaps, func(i, j int) bool {
		tI := tier(snaps[i])
		tJ := tier(snaps[j])
		if tI != tJ {
			return tI < tJ
		}
		if snaps[i].Record.HasPassed != snaps[j].Record.HasPassed {
			return snaps[i].Record.HasPassed
		}
		if snaps[i].Record.Score != snaps[j].Record.Score {
			return snaps[i].Record.Score > snaps[j].Record.Score
		}
		latI := snaps[i].Record.LastPassedLatency
		if latI == 0 && snaps[i].TransportLatency > 0 {
			latI = snaps[i].TransportLatency
		}
		latJ := snaps[j].Record.LastPassedLatency
		if latJ == 0 && snaps[j].TransportLatency > 0 {
			latJ = snaps[j].TransportLatency
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
		return snaps[i].Record.CanonicalLink < snaps[j].Record.CanonicalLink
	})

	a.mu.Lock()
	defer a.mu.Unlock()

	// Prune unreferenced candidate mappings to bound memory strictly to current snapshot
	newLinkToID := make(map[string]string, len(snaps))
	newIDToLink := make(map[string]string, len(snaps))
	for _, snap := range snaps {
		canonical := snap.Record.CanonicalLink
		opaqueID, exists := a.linkToID[canonical]
		if !exists {
			a.idCounter++
			opaqueID = fmt.Sprintf("cand-%d", a.idCounter)
		}
		newLinkToID[canonical] = opaqueID
		newIDToLink[opaqueID] = canonical
	}
	a.linkToID = newLinkToID
	a.idToLink = newIDToLink

	out := make([]viewmodel.CandidateRowViewModel, 0, len(snaps))
	for _, snap := range snaps {
		switch filter {
		case viewmodel.FilterGemini:
			if !snap.Servable {
				continue
			}
		case viewmodel.FilterGeneric:
			if !snap.NetworkHealthy {
				continue
			}
		case viewmodel.FilterAll:
			// include all
		}

		canonical := snap.Record.CanonicalLink
		opaqueID := a.linkToID[canonical]

		activeLink := snap.Record.ActiveLink
		if activeLink == "" {
			activeLink = canonical
		}

		proto := ExtractProtocol(activeLink)
		endpoint := ExtractEndpoint(activeLink)
		remark := country.FormatRemark(ExtractRemark(activeLink), a.flagMode)

		status := "PEND"
		if snap.Record.Latest.Status != "" {
			switch snap.Record.Latest.Status {
			case store.StatusPassed:
				status = "PASS"
			case store.StatusFailed:
				if snap.TransportOK || (snap.NetworkHealthy && snap.Record.Latest.Category.IsTargetSpecific()) {
					switch snap.Record.Latest.Category {
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
		if snap.Record.History.Count > 0 {
			scoreStr = fmt.Sprintf("%.2f", snap.Record.Score)
		}

		// Aligned with Store reliability contract:
		// Only display LastPassedLatency for proven candidates (HasPassed == true).
		latStr := "---"
		if snap.Record.HasPassed && snap.Record.LastPassedLatency > 0 {
			latStr = snap.Record.LastPassedLatency.Round(time.Millisecond).String()
		}

		glyphs := FormatHistoryGlyphs(snap.Record.History.ChronologicalSamples(), snap.Record.History.Capacity)

		out = append(out, viewmodel.CandidateRowViewModel{
			ID:                     opaqueID,
			Protocol:               proto,
			Endpoint:               endpoint,
			Remark:                 remark,
			Status:                 status,
			ScoreFormatted:         scoreStr,
			LatencyFormatted:       latStr,
			HistoryGlyphs:          glyphs,
			Servable:               snap.Servable,
			NetworkHealthy:         snap.NetworkHealthy,
			TransportOK:            snap.TransportOK,
			TransportEvidenceKnown: snap.TransportEvidenceKnown,
			TransportLatency:       snap.TransportLatency,
			HasPassed:              snap.Record.HasPassed,
		})
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

// Snapshot returns a SnapshotViewModel assembled from authoritative reads (Header, CandidateRows, and Logs).
func (a *Adapter) Snapshot(filter viewmodel.FilterMode) viewmodel.SnapshotViewModel {
	return viewmodel.SnapshotViewModel{
		Header: a.Header(),
		Rows:   a.CandidateRows(filter),
		Logs:   a.Logs(),
	}
}

// PollSnapshot returns updated presentation viewmodels if state has been invalidated.
// If not invalidated, it returns updated=false without building ViewModels.
func (a *Adapter) PollSnapshot(filter viewmodel.FilterMode) (viewmodel.SnapshotViewModel, bool) {
	if !a.CheckAndResetDirty() {
		return viewmodel.SnapshotViewModel{}, false
	}
	return a.Snapshot(filter), true
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
