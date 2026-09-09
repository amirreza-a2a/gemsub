// Package store holds the current pass/fail state of every candidate
// config in memory, and persists a lightweight snapshot to disk so a
// restart doesn't have to wait for a full test cycle before the sub
// server has something to serve.
package store

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is the canonical internal state of a candidate probe outcome.
type Status string

const (
	StatusPassed       Status = "passed"
	StatusFailed       Status = "failed"
	StatusInconclusive Status = "inconclusive"
)

// ErrorCategory provides machine-readable classification for aggregation and monitoring.
type ErrorCategory string

const (
	ErrNone              ErrorCategory = "none"
	ErrRegionBlocked     ErrorCategory = "region_blocked"
	ErrTargetDenied      ErrorCategory = "target_denied"       // HTTP 403 from Gemini
	ErrTargetRateLimited ErrorCategory = "target_rate_limited" // HTTP 429 from Gemini
	ErrProxyRateLimited  ErrorCategory = "proxy_rate_limited"  // HTTP 429 during proxy tunnel dial
	ErrTargetError       ErrorCategory = "target_error"        // 5xx from Gemini
	ErrTargetOther       ErrorCategory = "target_other"        // Non-200 or captive portal
	ErrConnRefused       ErrorCategory = "conn_refused"        // TCP connection refused
	ErrTimeout           ErrorCategory = "timeout"             // TCP/dial/read timeout
	ErrReset             ErrorCategory = "conn_reset"          // TCP reset / EOF
	ErrTLS               ErrorCategory = "tls_error"           // TLS handshake / cert error
	ErrReality           ErrorCategory = "reality_error"       // Reality verification failed
	ErrProxyError        ErrorCategory = "proxy_error"         // Proxy CDN 4xx/5xx during dial
	ErrConfig            ErrorCategory = "config_error"        // Outbound config error
)

// IsTargetSpecific returns true if the error category represents a target-application-layer
// rejection (such as regional blocking or target access denial) rather than a network transport failure.
func (c ErrorCategory) IsTargetSpecific() bool {
	return c == ErrRegionBlocked || c == ErrTargetDenied
}

// Result is the outcome of testing a single candidate link.
type Result struct {
	Link                    string        `json:"link"`
	Status                  Status        `json:"status"` // Canonical internal state
	Passed                  bool          `json:"passed"` // Outcome of latest probe (Status == StatusPassed)
	Reason                  string        `json:"reason"`
	Category                ErrorCategory `json:"category,omitempty"`
	StatusCode              int           `json:"status_code,omitempty"`
	Latency                 time.Duration `json:"latency"`
	TestedAt                time.Time     `json:"tested_at"`
	Attempts                int           `json:"attempts,omitempty"`
	ConsecutiveInconclusive int           `json:"consecutive_inconclusive,omitempty"`
	PreviouslyPassed        bool          `json:"previously_passed,omitempty"`
	Warnings                []string      `json:"warnings,omitempty"` // Observable parser normalization notes
	TransportOK             bool          `json:"transport_ok,omitempty"`
	TransportLatency        time.Duration `json:"transport_latency,omitempty"`
	TransportEvidenceKnown  bool          `json:"transport_evidence_known,omitempty"`
}

// Snapshot is the full persisted state.
type Snapshot struct {
	Version    int                `json:"version,omitempty"`
	LastCycle  time.Time          `json:"last_cycle"`
	CycleCount int                `json:"cycle_count"`
	Records    []*CandidateRecord `json:"records,omitempty"`
	Results    []Result           `json:"results,omitempty"` // Legacy V1 support
}

// Store is safe for concurrent use.
type Store struct {
	mu             sync.RWMutex
	records        map[string]*CandidateRecord // keyed by CanonicalLink
	pendingAbsent  map[string]struct{}         // candidates absent in current active cycle
	pendingPresent map[string]struct{}         // candidates confirmed present in current active cycle
	lastCycle      time.Time
	cycleCount     int
	cycleID        uint64
	revision       uint64
	path           string
	cfg            ScoringConfig
}

// New creates an empty store bound to the given snapshot file path and retention policy.
// Call Load to populate it from disk, if a previous snapshot exists.
func New(path string, maxInconclusiveCycles int) *Store {
	cfg := DefaultScoringConfig()
	if maxInconclusiveCycles > 0 {
		cfg.MaxAbsentCycles = maxInconclusiveCycles
	}
	return NewWithConfig(path, cfg)
}

// NewWithConfig creates a Store with a custom ScoringConfig.
func NewWithConfig(path string, cfg ScoringConfig) *Store {
	cfg = cfg.Clone()
	if cfg.HistoryCapacity <= 0 {
		cfg.HistoryCapacity = 10
	}
	if cfg.DecayLambda <= 0.0 || cfg.DecayLambda > 1.0 {
		cfg.DecayLambda = 0.75
	}
	if cfg.MinServableScore <= 0.0 {
		cfg.MinServableScore = 0.65
	}
	if cfg.MinObservationsForServing <= 0 {
		cfg.MinObservationsForServing = 1
	}
	if cfg.MaxAbsentCycles <= 0 {
		cfg.MaxAbsentCycles = 2
	}
	if cfg.CategoryWeights == nil {
		cfg.CategoryWeights = DefaultScoringConfig().CategoryWeights
	}

	return &Store{
		records:        make(map[string]*CandidateRecord),
		pendingAbsent:  make(map[string]struct{}),
		pendingPresent: make(map[string]struct{}),
		path:           path,
		cfg:            cfg,
	}
}

// Config returns a deep copy of the store's scoring configuration.
func (s *Store) Config() ScoringConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Clone()
}

// servabilityGateLocked evaluates the 4 operational policy gates for candidate servability
// and returns both the decision and human-readable explanation.
// mu must be locked (RLock or Lock) by caller.
func (s *Store) servabilityGateLocked(rec *CandidateRecord) (bool, string) {
	if rec == nil {
		return false, "No record"
	}

	// Gate 1: Source Presence Gate
	// Disqualified if candidate is absent in active cycle or absent from upstream without reappearing.
	if _, pending := s.pendingAbsent[rec.CanonicalLink]; pending {
		return false, "Absent in active cycle"
	}
	if _, present := s.pendingPresent[rec.CanonicalLink]; !present && rec.AbsentCycles > 0 {
		return false, "Absent from source upstream"
	}

	// Gate 2: Target Policy Override
	// Disqualified if latest conclusive target observation is StatusFailed with ErrRegionBlocked or ErrTargetDenied.
	if sample, ok := rec.History.LatestConclusive(); ok {
		if sample.Status == StatusFailed && (sample.Category == ErrRegionBlocked || sample.Category == ErrTargetDenied) {
			return false, "Target policy: " + string(sample.Category)
		}
	} else if rec.Latest.Status == StatusFailed && (rec.Latest.Category == ErrRegionBlocked || rec.Latest.Category == ErrTargetDenied) {
		return false, "Target policy: " + string(rec.Latest.Category)
	}

	// Gate 3: Cold Start Gate
	// Disqualified until minimum observation count is reached.
	if rec.History.Count < s.cfg.MinObservationsForServing {
		return false, "Cold start: needs observation"
	}

	// Gate 4: Reliability Score Threshold Gate
	// Disqualified if recency-weighted score is below threshold.
	if rec.Score < s.cfg.MinServableScore {
		// Legacy Last-Known-Good compatibility: if migrated from legacy state with prior pass
		// and inconclusive count within limit, preserve servability until probed in new cycle.
		if rec.Latest.Status == StatusInconclusive && rec.Latest.PreviouslyPassed && rec.Latest.ConsecutiveInconclusive <= s.cfg.MaxAbsentCycles && rec.History.Count == 1 {
			return true, "Servable (grace period)"
		}
		return false, "Score below threshold"
	}

	return true, "Servable"
}

// isServableRecordLocked evaluates the 4 operational policy gates for candidate servability.
// mu must be locked (RLock or Lock) by caller.
func (s *Store) isServableRecordLocked(rec *CandidateRecord) bool {
	servable, _ := s.servabilityGateLocked(rec)
	return servable
}

// ServabilityGate returns the servability decision and gate explanation for a record.
func (s *Store) ServabilityGate(rec *CandidateRecord) (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.servabilityGateLocked(rec)
}

// IsServableRecord returns true if the candidate record satisfies all servability policy gates.
func (s *Store) IsServableRecord(rec *CandidateRecord) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isServableRecordLocked(rec)
}

// networkHealthyStateLocked checks whether a record is network-healthy, and returns both
// whether it is network-healthy and whether it is Gemini-servable, avoiding duplicate gate evaluations.
// mu must be locked (RLock or Lock) by caller.
func (s *Store) networkHealthyStateLocked(rec *CandidateRecord) (healthy bool, servable bool) {
	if rec == nil {
		return false, false
	}

	// Gate 1: Source Presence Gate
	// Disqualified if candidate is absent in active cycle or absent from upstream without reappearing.
	if _, pending := s.pendingAbsent[rec.CanonicalLink]; pending {
		return false, false
	}
	if _, present := s.pendingPresent[rec.CanonicalLink]; !present && rec.AbsentCycles > 0 {
		return false, false
	}

	// Gate 3: Cold Start Gate
	// Disqualified until minimum observation count is reached.
	if rec.History.Count < s.cfg.MinObservationsForServing {
		return false, false
	}

	// Tier 1: If candidate satisfies the standard servability gate (passed Gemini), it is network-healthy and servable.
	if s.isServableRecordLocked(rec) {
		return true, true
	}

	// Tier 2 (New Explicit Evidence):
	// If explicit transport evidence is known on Latest, it is authoritative:
	if rec.Latest.TransportEvidenceKnown {
		if rec.Latest.TransportOK {
			return true, false
		}
		// Explicit transport failure in latest observation: candidate is not network-healthy
		return false, false
	}

	// If Latest had no explicit evidence (e.g. trailing inconclusive or legacy), check latest conclusive sample:
	sample, ok := rec.History.LatestConclusive()
	if !ok {
		sample = ProbeSample{
			Status:                 rec.Latest.Status,
			Category:               rec.Latest.Category,
			TransportOK:            rec.Latest.TransportOK,
			TransportLatency:       rec.Latest.TransportLatency,
			TransportEvidenceKnown: rec.Latest.TransportEvidenceKnown,
		}
	}
	if sample.TransportEvidenceKnown {
		if sample.TransportOK {
			return true, false
		}
		return false, false
	}

	// Tier 3 (Historical Compatibility Bridge): Fall back to Ticket 16's logic checking if
	// the latest authoritative conclusive observation was rejected solely by target-specific restrictions
	// where proxy dial and HTTP round-trip succeeded.
	if sample.Status == StatusFailed && sample.Category.IsTargetSpecific() {
		return true, false
	}

	return false, false
}

// isNetworkHealthyRecordLocked evaluates whether a candidate has verified network/transport health,
// regardless of target-specific restrictions such as region blocks or target denial.
// mu must be locked (RLock or Lock) by caller.
func (s *Store) isNetworkHealthyRecordLocked(rec *CandidateRecord) bool {
	healthy, _ := s.networkHealthyStateLocked(rec)
	return healthy
}

// IsNetworkHealthyRecord returns true if the candidate record has verified network/transport health,
// regardless of target-specific restrictions such as region blocks.
func (s *Store) IsNetworkHealthyRecord(rec *CandidateRecord) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isNetworkHealthyRecordLocked(rec)
}

// IsServable returns true if candidate passes directly or retains servable status.
func (s *Store) IsServable(r Result) bool {
	canonical := CanonicalizeLink(r.Link)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if rec, ok := s.records[canonical]; ok {
		return s.isServableRecordLocked(rec)
	}
	// Fallback for ad-hoc Result not yet in store
	if r.Status == StatusPassed {
		return true
	}
	return false
}

// Load reads a previously saved snapshot from disk, if present.
// Missing file is not an error — it just means a cold start.
// When Version 2 snapshot is found, BoundedHistory is authoritative and Score is recomputed.
// When legacy Version 1 snapshot is found, results are deterministically migrated.
func (s *Store) Load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastCycle = snap.LastCycle
	s.cycleCount = snap.CycleCount
	s.cycleID = uint64(snap.CycleCount)
	s.revision++
	s.records = make(map[string]*CandidateRecord)
	s.pendingAbsent = make(map[string]struct{})

	if snap.Version >= 2 || len(snap.Records) > 0 {
		for _, rec := range snap.Records {
			if rec == nil {
				continue
			}
			if rec.CanonicalLink == "" && rec.ActiveLink != "" {
				rec.CanonicalLink = CanonicalizeLink(rec.ActiveLink)
			}
			if rec.ActiveLink == "" && rec.CanonicalLink != "" {
				rec.ActiveLink = rec.CanonicalLink
			}
			// Validate and normalize circular buffer invariants
			rec.History.NormalizeAndValidate(s.cfg.HistoryCapacity)

			// Recompute score deterministically from authoritative history on load
			rec.Score = rec.History.ComputeScore(s.cfg.DecayLambda, s.cfg.CategoryWeights)

			// Recompute HasPassed and LastPassedLatency deterministically from authoritative history on load,
			// treating persisted values as derived/cache data.
			if lat, ok := rec.History.LastPassedLatency(); ok {
				rec.LastPassedLatency = lat
				rec.HasPassed = true
			} else {
				rec.LastPassedLatency = 0
				rec.HasPassed = false
			}

			// Align projection semantics on latest Result
			rec.Latest.Passed = (rec.Latest.Status == StatusPassed)
			if rec.History.Count > 0 {
				rec.Latest.PreviouslyPassed = rec.History.PreviouslyPassed()
				rec.Latest.ConsecutiveInconclusive = rec.History.ConsecutiveInconclusive()
			}

			s.records[rec.CanonicalLink] = rec
		}
		return nil
	}

	// Legacy V1 migration: strictly preserve actual observations without fabricating history
	for _, r := range snap.Results {
		if r.Status == "" {
			if r.Passed {
				r.Status = StatusPassed
			} else {
				r.Status = StatusFailed
			}
		}
		if r.Category == "" {
			if r.Status == StatusPassed {
				r.Category = ErrNone
			} else if r.Reason != "" || r.StatusCode != 0 {
				cat := deriveCategory(r.Reason, r.StatusCode)
				if r.Status == StatusInconclusive && cat == ErrProxyError {
					cat = ErrTimeout
				}
				r.Category = cat
			} else if r.Status == StatusInconclusive {
				r.Category = ErrTimeout
			} else {
				r.Category = ErrProxyError
			}
		}

		canonical := CanonicalizeLink(r.Link)
		rec := &CandidateRecord{
			CanonicalLink: canonical,
			ActiveLink:    r.Link,
			AbsentCycles:  0,
			History:       NewBoundedHistory(s.cfg.HistoryCapacity),
		}

		attempts := r.Attempts
		if attempts <= 0 {
			attempts = 1
		}

		// Push EXACTLY ONE authentic probe sample representing the legacy result.
		// DO NOT fabricate historical StatusPassed samples from PreviouslyPassed.
		// DO NOT fabricate timestamps or status codes.
		// DO NOT duplicate samples from ConsecutiveInconclusive.
		sample := ProbeSample{
			CycleID:                uint64(snap.CycleCount),
			TestedAt:               r.TestedAt,
			Status:                 r.Status,
			Category:               r.Category,
			StatusCode:             r.StatusCode,
			Latency:                r.Latency,
			Attempts:               attempts,
			TransportOK:            r.TransportOK,
			TransportLatency:       r.TransportLatency,
			TransportEvidenceKnown: r.TransportEvidenceKnown,
		}
		rec.History.Push(sample)

		if r.Status == StatusPassed {
			rec.LastPassedLatency = r.Latency
			rec.HasPassed = true
		}

		rec.Score = rec.History.ComputeScore(s.cfg.DecayLambda, s.cfg.CategoryWeights)

		// Projection invariants: Passed reflects actual observation outcome
		r.Passed = (r.Status == StatusPassed)
		rec.Latest = r
		// Preserve legacy LKG metadata on Latest for backward compatibility
		rec.Latest.PreviouslyPassed = r.PreviouslyPassed
		rec.Latest.ConsecutiveInconclusive = r.ConsecutiveInconclusive

		s.records[canonical] = rec
	}

	return nil
}

func deriveCategory(reason string, statusCode int) ErrorCategory {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "regional block") || strings.Contains(lower, "supported in your country"):
		return ErrRegionBlocked
	case statusCode == 403 || strings.Contains(lower, "status 403"):
		return ErrTargetDenied
	case (strings.Contains(lower, "proxy") || strings.Contains(lower, "tunnel") || strings.Contains(lower, "cdn")) && strings.Contains(lower, "429"):
		return ErrProxyRateLimited
	case strings.Contains(lower, "unexpected http response status: 429"):
		return ErrProxyRateLimited
	case statusCode == 429 || strings.Contains(lower, "status 429") || strings.Contains(lower, "429"):
		return ErrTargetRateLimited
	case strings.Contains(lower, "connection refused"):
		return ErrConnRefused
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return ErrTimeout
	case strings.Contains(lower, "eof") || strings.Contains(lower, "reset by peer"):
		return ErrReset
	case strings.Contains(lower, "tls") || strings.Contains(lower, "handshake"):
		return ErrTLS
	case strings.Contains(lower, "reality"):
		return ErrReality
	case strings.Contains(lower, "utls"):
		return ErrConfig
	default:
		return ErrProxyError
	}
}

// Save writes the current state to disk atomically (write to temp file,
// flush/sync, then rename) so a crash mid-write cannot corrupt the snapshot.
func (s *Store) Save() error {
	s.mu.RLock()
	snap := Snapshot{
		Version:    2,
		LastCycle:  s.lastCycle,
		CycleCount: s.cycleCount,
		Records:    make([]*CandidateRecord, 0, len(s.records)),
	}
	for _, rec := range s.records {
		snap.Records = append(snap.Records, rec.Clone())
	}
	s.mu.RUnlock()

	// Sort records deterministically by CanonicalLink
	sort.Slice(snap.Records, func(i, j int) bool {
		return snap.Records[i].CanonicalLink < snap.Records[j].CanonicalLink
	})

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}

// PutWithTransition applies canonical state transitions, records a probe sample,
// and updates the recency-weighted reliability score and projections.
func (s *Store) PutWithTransition(r Result) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.Status == "" {
		if r.Passed {
			r.Status = StatusPassed
		} else {
			r.Status = StatusFailed
		}
	}
	if r.Category == "" {
		if r.Status == StatusPassed {
			r.Category = ErrNone
		} else if r.Reason != "" || r.StatusCode != 0 {
			cat := deriveCategory(r.Reason, r.StatusCode)
			if r.Status == StatusInconclusive && cat == ErrProxyError {
				cat = ErrTimeout
			}
			r.Category = cat
		} else if r.Status == StatusInconclusive {
			r.Category = ErrTimeout
		} else {
			r.Category = ErrProxyError
		}
	}

	canonical := CanonicalizeLink(r.Link)
	rec, exists := s.records[canonical]
	if !exists {
		rec = &CandidateRecord{
			CanonicalLink: canonical,
			ActiveLink:    r.Link,
			History:       NewBoundedHistory(s.cfg.HistoryCapacity),
		}
		s.records[canonical] = rec
	}

	// Update active serving representation to match the link used
	rec.ActiveLink = r.Link

	// Candidate was probed in current cycle: mark pending present and clear pending absent.
	// Note: AbsentCycles reset to 0 is deferred until FinishCycle() to ensure cycle-level atomicity.
	delete(s.pendingAbsent, canonical)
	s.pendingPresent[canonical] = struct{}{}

	testedAt := r.TestedAt
	if testedAt.IsZero() {
		testedAt = time.Now()
	}
	attempts := r.Attempts
	if attempts <= 0 {
		attempts = 1
	}

	sample := ProbeSample{
		CycleID:                s.cycleID,
		TestedAt:               testedAt,
		Status:                 r.Status,
		Category:               r.Category,
		StatusCode:             r.StatusCode,
		Latency:                r.Latency,
		Attempts:               attempts,
		TransportOK:            r.TransportOK,
		TransportLatency:       r.TransportLatency,
		TransportEvidenceKnown: r.TransportEvidenceKnown,
	}

	rec.History.Push(sample)
	rec.Score = rec.History.ComputeScore(s.cfg.DecayLambda, s.cfg.CategoryWeights)

	// Derive HasPassed and LastPassedLatency dynamically from authoritative history.
	// Ranking latency derives strictly from the most recent StatusPassed sample currently
	// present in the active bounded history window. If all successful observations have been
	// evicted from active history, HasPassed becomes false and LastPassedLatency is reset to 0.
	if lat, ok := rec.History.LastPassedLatency(); ok {
		rec.LastPassedLatency = lat
		rec.HasPassed = true
	} else {
		rec.LastPassedLatency = 0
		rec.HasPassed = false
	}

	// Result projection invariants
	r.Passed = (r.Status == StatusPassed)
	r.PreviouslyPassed = rec.History.PreviouslyPassed()
	r.ConsecutiveInconclusive = rec.History.ConsecutiveInconclusive()

	// Preserve existing warnings if new result doesn't provide any
	if len(r.Warnings) == 0 && exists && len(rec.Latest.Warnings) > 0 {
		r.Warnings = rec.Latest.Warnings
	}

	rec.Latest = r
	s.revision++
}

// Put records the result of testing one candidate. Safe to call
// concurrently from many tester workers.
func (s *Store) Put(r Result) {
	s.PutWithTransition(r)
}

// StartCycle marks the beginning of a new test cycle. Candidates present in currentLinks
// are marked pending-present (retaining servability without prematurely mutating record state).
// Candidates missing from currentLinks are marked pending-absent. Absence increments and resets
// are only committed atomically to candidate records upon successful FinishCycle.
func (s *Store) StartCycle(currentLinks map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.revision++

	canonicalPresent := make(map[string]struct{}, len(currentLinks))
	for raw := range currentLinks {
		canonicalPresent[CanonicalizeLink(raw)] = struct{}{}
	}

	s.pendingAbsent = make(map[string]struct{})
	s.pendingPresent = make(map[string]struct{}, len(canonicalPresent))
	for canonical := range s.records {
		if _, ok := canonicalPresent[canonical]; ok {
			s.pendingPresent[canonical] = struct{}{}
		} else {
			s.pendingAbsent[canonical] = struct{}{}
		}
	}
}

// FinishCycle records that a full test cycle completed. It commits absence increments
// for pending-absent candidates (evicting those exceeding MaxAbsentCycles) and commits
// absence resets for candidates confirmed present during the cycle.
func (s *Store) FinishCycle() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for canonical := range s.pendingAbsent {
		rec, exists := s.records[canonical]
		if !exists {
			continue
		}
		rec.AbsentCycles++
		if rec.AbsentCycles > s.cfg.MaxAbsentCycles {
			delete(s.records, canonical)
		}
	}

	for canonical := range s.pendingPresent {
		if rec, exists := s.records[canonical]; exists {
			rec.AbsentCycles = 0
		}
	}

	s.pendingAbsent = make(map[string]struct{})
	s.pendingPresent = make(map[string]struct{})
	s.lastCycle = time.Now()
	s.cycleCount++
	s.cycleID++
	s.revision++
}

// Passing returns the active links of all servable candidates, in stable sorted order.
func (s *Store) Passing() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []string
	for _, rec := range s.records {
		if s.isServableRecordLocked(rec) {
			out = append(out, rec.ActiveLink)
		}
	}
	slices.Sort(out)
	return out
}

// PassingRanked returns the active links of all servable candidates.
// Ranking policy precedence:
//  1. Proven candidates (HasPassed == true) strictly outrank unproven candidates (HasPassed == false),
//     preventing candidates without confirmed passes from outranking proven ones via score or latency.
//  2. Reliability score descending.
//  3. Last passed latency ascending (unproven candidates sort worst at math.MaxInt64).
//  4. ActiveLink ascending for deterministic tie-breaking.
func (s *Store) PassingRanked() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type rankedRecord struct {
		activeLink string
		score      float64
		latency    time.Duration
		hasPassed  bool
	}

	servable := make([]rankedRecord, 0, len(s.records))
	for _, rec := range s.records {
		if s.isServableRecordLocked(rec) {
			lat := rec.LastPassedLatency
			hasPassed := rec.HasPassed
			if !hasPassed {
				lat = time.Duration(math.MaxInt64)
			}
			servable = append(servable, rankedRecord{
				activeLink: rec.ActiveLink,
				score:      rec.Score,
				latency:    lat,
				hasPassed:  hasPassed,
			})
		}
	}

	sort.Slice(servable, func(i, j int) bool {
		// Proven candidates (with at least one successful pass) always rank before unproven ones
		if servable[i].hasPassed != servable[j].hasPassed {
			return servable[i].hasPassed
		}
		if servable[i].score != servable[j].score {
			return servable[i].score > servable[j].score // Score desc
		}
		if servable[i].latency != servable[j].latency {
			return servable[i].latency < servable[j].latency // Latency asc
		}
		return servable[i].activeLink < servable[j].activeLink
	})

	out := make([]string, len(servable))
	for i, r := range servable {
		out[i] = r.activeLink
	}
	return out
}

// NetworkPassing returns the active links of all candidates with verified network/transport health,
// in stable sorted order. This includes candidates that are Gemini-servable as well as candidates
// whose network transport succeeded but were blocked solely by Gemini-specific target restrictions.
func (s *Store) NetworkPassing() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []string
	for _, rec := range s.records {
		if s.isNetworkHealthyRecordLocked(rec) {
			out = append(out, rec.ActiveLink)
		}
	}
	slices.Sort(out)
	return out
}

// NetworkPassingRanked returns the active links of all network-healthy candidates.
// Ranking policy precedence:
//  1. Gemini-servable candidates (isServableRecordLocked == true) strictly outrank
//     target-incompatible candidates.
//  2. Within Tier 1 (Gemini-servable candidates):
//     - Reliability score descending.
//     - Last passed latency ascending (unproven candidates sort worst at math.MaxInt64).
//     - ActiveLink ascending for deterministic tie-breaking.
//  3. Within Tier 2 (Target-incompatible but network-healthy candidates):
//     - Network-healthy latency ascending (using LastNetworkHealthyLatency; unknown latency sorts worst at math.MaxInt64).
//     - ActiveLink ascending for deterministic tie-breaking.
//     - NOTE: Gemini-specific reliability score (rec.Score) is intentionally NOT used for Tier 2 ranking,
//     preventing candidates with prior timeouts from outranking clean target-blocked nodes.
func (s *Store) NetworkPassingRanked() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type rankedRecord struct {
		activeLink string
		score      float64
		latency    time.Duration
		servable   bool
	}

	healthy := make([]rankedRecord, 0, len(s.records))
	for _, rec := range s.records {
		if isHealthy, isServable := s.networkHealthyStateLocked(rec); isHealthy {
			var lat time.Duration
			if isServable {
				if rec.HasPassed && rec.LastPassedLatency > 0 {
					lat = rec.LastPassedLatency
				} else if rec.Latest.Latency > 0 {
					lat = rec.Latest.Latency
				} else {
					lat = time.Duration(math.MaxInt64)
				}
			} else {
				if l, ok := rec.History.LastNetworkHealthyLatency(); ok && l > 0 {
					lat = l
				} else if rec.Latest.TransportLatency > 0 {
					lat = rec.Latest.TransportLatency
				} else if rec.Latest.Latency > 0 {
					lat = rec.Latest.Latency
				} else {
					lat = time.Duration(math.MaxInt64)
				}
			}
			healthy = append(healthy, rankedRecord{
				activeLink: rec.ActiveLink,
				score:      rec.Score,
				latency:    lat,
				servable:   isServable,
			})
		}
	}

	sort.Slice(healthy, func(i, j int) bool {
		// Tier 1: Gemini-servable candidates strictly outrank target-incompatible candidates
		if healthy[i].servable != healthy[j].servable {
			return healthy[i].servable
		}

		// Within Tier 1 (Gemini-servable candidates): preserve authoritative PassingRanked semantics
		if healthy[i].servable {
			if healthy[i].score != healthy[j].score {
				return healthy[i].score > healthy[j].score
			}
			if healthy[i].latency != healthy[j].latency {
				return healthy[i].latency < healthy[j].latency
			}
			return healthy[i].activeLink < healthy[j].activeLink
		}

		// Within Tier 2 (Target-incompatible candidates):
		// Do NOT use Gemini-specific rec.Score. Rank strictly by:
		// 1. Network-healthy latency ascending.
		// 2. ActiveLink ascending as deterministic tie-breaker.
		if healthy[i].latency != healthy[j].latency {
			return healthy[i].latency < healthy[j].latency
		}
		return healthy[i].activeLink < healthy[j].activeLink
	})

	out := make([]string, len(healthy))
	for i, r := range healthy {
		out[i] = r.activeLink
	}
	return out
}

// Stats is a snapshot of counts based on canonical Status and servability.
type Stats struct {
	Total           int       `json:"total"`
	Passed          int       `json:"passed"`
	Failed          int       `json:"failed"`
	Inconclusive    int       `json:"inconclusive"`
	Servable        int       `json:"servable"`
	GenericServable int       `json:"generic_servable"`
	LastCycle       time.Time `json:"last_cycle"`
	CycleCount      int       `json:"cycle_count"`
}

func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := Stats{
		Total:      len(s.records),
		LastCycle:  s.lastCycle,
		CycleCount: s.cycleCount,
	}
	for _, rec := range s.records {
		switch rec.Latest.Status {
		case StatusPassed:
			st.Passed++
		case StatusFailed:
			st.Failed++
		case StatusInconclusive:
			st.Inconclusive++
		}
		if s.isServableRecordLocked(rec) {
			st.Servable++
		}
		if s.isNetworkHealthyRecordLocked(rec) {
			st.GenericServable++
		}
	}
	return st
}

// All returns every current result, for detailed inspection.
func (s *Store) All() []Result {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Result, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec.Latest)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Link < out[j].Link
	})
	return out
}

// Get returns the latest Result for a candidate link, if present.
func (s *Store) Get(link string) (Result, bool) {
	canonical := CanonicalizeLink(link)
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.records[canonical]
	if !ok {
		return Result{}, false
	}
	return rec.Latest, true
}

// GetRecord returns a copy of the CandidateRecord for a candidate link, if present.
func (s *Store) GetRecord(link string) (*CandidateRecord, bool) {
	canonical := CanonicalizeLink(link)
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.records[canonical]
	if !ok {
		return nil, false
	}
	return rec.Clone(), true
}

// CandidateSnapshot pairs a cloned CandidateRecord with its atomic servability status and gate reason.
type CandidateSnapshot struct {
	Record                 *CandidateRecord
	Servable               bool
	Gate                   string
	NetworkHealthy         bool
	TransportOK            bool
	TransportLatency       time.Duration
	TransportEvidenceKnown bool
}

// Snapshots returns a consistent snapshot of all candidate records and their servability evaluations.
func (s *Store) Snapshots() []CandidateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]CandidateSnapshot, 0, len(s.records))
	for _, rec := range s.records {
		servable, gate := s.servabilityGateLocked(rec)
		out = append(out, CandidateSnapshot{
			Record:                 rec.Clone(),
			Servable:               servable,
			Gate:                   gate,
			NetworkHealthy:         s.isNetworkHealthyRecordLocked(rec),
			TransportOK:            rec.Latest.TransportOK,
			TransportLatency:       rec.Latest.TransportLatency,
			TransportEvidenceKnown: rec.Latest.TransportEvidenceKnown,
		})
	}
	return out
}

// CandidateIndexEntry represents lightweight, sortable presentation information for a single candidate.
// It exposes only the fields required for presentation ranking, filtering, and cycle reconciliation,
// without deep-copying CandidateRecord or BoundedHistory.
type CandidateIndexEntry struct {
	CanonicalLink          string
	ActiveLink             string
	Servable               bool
	NetworkHealthy         bool
	HasPassed              bool
	Score                  float64
	LastPassedLatency      time.Duration
	TransportLatency       time.Duration
	TransportEvidenceKnown bool
	TransportOK            bool
	LatestStatus           Status
	LatestCategory         ErrorCategory
	HistoryCount           int
	LastCycleID            uint64
}

// CandidateIndex returns a lightweight slice of presentation index entries for all candidates.
// It avoids deep-copying CandidateRecord or BoundedHistory.
func (s *Store) CandidateIndex() []CandidateIndexEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]CandidateIndexEntry, 0, len(s.records))
	for _, rec := range s.records {
		servable, _ := s.servabilityGateLocked(rec)
		var lastCycleID uint64
		var lastStatus Status
		if lastSample, ok := rec.History.Last(); ok {
			lastCycleID = lastSample.CycleID
			lastStatus = lastSample.Status
		} else if rec.Latest.Status != "" {
			lastStatus = rec.Latest.Status
		}

		activeLink := rec.ActiveLink
		if activeLink == "" {
			activeLink = rec.CanonicalLink
		}

		out = append(out, CandidateIndexEntry{
			CanonicalLink:          rec.CanonicalLink,
			ActiveLink:             activeLink,
			Servable:               servable,
			NetworkHealthy:         s.isNetworkHealthyRecordLocked(rec),
			HasPassed:              rec.HasPassed,
			Score:                  rec.Score,
			LastPassedLatency:      rec.LastPassedLatency,
			TransportLatency:       rec.Latest.TransportLatency,
			TransportEvidenceKnown: rec.Latest.TransportEvidenceKnown,
			TransportOK:            rec.Latest.TransportOK,
			LatestStatus:           lastStatus,
			LatestCategory:         rec.Latest.Category,
			HistoryCount:           rec.History.Count,
			LastCycleID:            lastCycleID,
		})
	}
	return out
}

// Revision returns the monotonic generation counter of the store,
// incremented on every candidate state transition or cycle boundary.
func (s *Store) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}
