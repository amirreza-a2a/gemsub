// Package store holds the current pass/fail state of every candidate
// config in memory, and persists a lightweight snapshot to disk so a
// restart doesn't have to wait for a full test cycle before the sub
// server has something to serve.
package store

import (
	"encoding/json"
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
	mu            sync.RWMutex
	records       map[string]*CandidateRecord // keyed by CanonicalLink
	pendingAbsent map[string]struct{}         // candidates absent in current active cycle
	lastCycle     time.Time
	cycleCount    int
	cycleID       uint64
	path          string
	cfg           ScoringConfig
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
		records:       make(map[string]*CandidateRecord),
		pendingAbsent: make(map[string]struct{}),
		path:          path,
		cfg:           cfg,
	}
}

// Config returns a copy of the store's scoring configuration.
func (s *Store) Config() ScoringConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// isServableRecordLocked evaluates the 4 operational policy gates for candidate servability.
// mu must be locked (RLock or Lock) by caller.
func (s *Store) isServableRecordLocked(rec *CandidateRecord) bool {
	if rec == nil {
		return false
	}

	// Gate 1: Source Presence Gate
	// Disqualified if absent from upstream in previous completed cycles or in active cycle.
	if rec.AbsentCycles > 0 {
		return false
	}
	if _, pending := s.pendingAbsent[rec.CanonicalLink]; pending {
		return false
	}

	// Gate 2: Target Policy Override
	// Disqualified if latest conclusive target observation is ErrRegionBlocked or ErrTargetDenied.
	if rec.Latest.Category == ErrRegionBlocked || rec.Latest.Category == ErrTargetDenied {
		return false
	}
	if sample, ok := rec.History.LatestConclusive(); ok {
		if sample.Status == StatusFailed && (sample.Category == ErrRegionBlocked || sample.Category == ErrTargetDenied) {
			return false
		}
	}

	// Gate 3: Cold Start Gate
	// Disqualified until minimum observation count is reached.
	if rec.History.Count < s.cfg.MinObservationsForServing {
		return false
	}

	// Gate 4: Reliability Score Threshold Gate
	// Disqualified if recency-weighted score is below threshold.
	if rec.Score < s.cfg.MinServableScore {
		return false
	}

	return true
}

// IsServableRecord returns true if the candidate record satisfies all servability policy gates.
func (s *Store) IsServableRecord(rec *CandidateRecord) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isServableRecordLocked(rec)
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
			if rec.History.Capacity <= 0 {
				rec.History.Capacity = s.cfg.HistoryCapacity
			}
			// Recompute score deterministically from authoritative history on load
			rec.Score = rec.History.ComputeScore(s.cfg.DecayLambda, s.cfg.CategoryWeights)

			// Align projection semantics on latest Result
			rec.Latest.Passed = (rec.Latest.Status == StatusPassed)
			rec.Latest.PreviouslyPassed = rec.History.PreviouslyPassed()
			rec.Latest.ConsecutiveInconclusive = rec.History.ConsecutiveInconclusive()

			s.records[rec.CanonicalLink] = rec
		}
		return nil
	}

	// Legacy V1 migration
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

		sample := ProbeSample{
			CycleID:    uint64(snap.CycleCount),
			TestedAt:   r.TestedAt,
			Status:     r.Status,
			Category:   r.Category,
			StatusCode: r.StatusCode,
			Latency:    r.Latency,
			Attempts:   r.Attempts,
		}
		if sample.Attempts <= 0 {
			sample.Attempts = 1
		}

		if r.Status == StatusInconclusive {
			if r.PreviouslyPassed {
				rec.History.Push(ProbeSample{
					CycleID:    uint64(snap.CycleCount),
					TestedAt:   r.TestedAt.Add(-time.Minute),
					Status:     StatusPassed,
					Category:   ErrNone,
					StatusCode: 200,
					Attempts:   1,
				})
			}
			inconclusiveCount := r.ConsecutiveInconclusive
			if inconclusiveCount <= 0 {
				inconclusiveCount = 1
			}
			for i := 0; i < inconclusiveCount; i++ {
				rec.History.Push(sample)
			}
		} else {
			rec.History.Push(sample)
		}

		rec.Score = rec.History.ComputeScore(s.cfg.DecayLambda, s.cfg.CategoryWeights)
		r.Passed = (r.Status == StatusPassed)
		r.PreviouslyPassed = rec.History.PreviouslyPassed()
		r.ConsecutiveInconclusive = rec.History.ConsecutiveInconclusive()
		rec.Latest = r

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

	// Candidate was probed in current cycle: clear absence state
	delete(s.pendingAbsent, canonical)
	rec.AbsentCycles = 0

	testedAt := r.TestedAt
	if testedAt.IsZero() {
		testedAt = time.Now()
	}
	attempts := r.Attempts
	if attempts <= 0 {
		attempts = 1
	}

	sample := ProbeSample{
		CycleID:    s.cycleID,
		TestedAt:   testedAt,
		Status:     r.Status,
		Category:   r.Category,
		StatusCode: r.StatusCode,
		Latency:    r.Latency,
		Attempts:   attempts,
	}

	rec.History.Push(sample)
	rec.Score = rec.History.ComputeScore(s.cfg.DecayLambda, s.cfg.CategoryWeights)

	// Result projection invariants
	r.Passed = (r.Status == StatusPassed)
	r.PreviouslyPassed = rec.History.PreviouslyPassed()
	r.ConsecutiveInconclusive = rec.History.ConsecutiveInconclusive()

	// Preserve existing warnings if new result doesn't provide any
	if len(r.Warnings) == 0 && exists && len(rec.Latest.Warnings) > 0 {
		r.Warnings = rec.Latest.Warnings
	}

	rec.Latest = r
}

// Put records the result of testing one candidate. Safe to call
// concurrently from many tester workers.
func (s *Store) Put(r Result) {
	s.PutWithTransition(r)
}

// StartCycle marks the beginning of a new test cycle. Candidates present in currentLinks
// have their absence counter reset to 0. Candidates missing from currentLinks are marked
// pending-absent; their absence counters are only incremented upon successful FinishCycle.
func (s *Store) StartCycle(currentLinks map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	canonicalPresent := make(map[string]struct{}, len(currentLinks))
	for raw := range currentLinks {
		canonicalPresent[CanonicalizeLink(raw)] = struct{}{}
	}

	s.pendingAbsent = make(map[string]struct{})
	for canonical, rec := range s.records {
		if _, ok := canonicalPresent[canonical]; ok {
			rec.AbsentCycles = 0
		} else {
			s.pendingAbsent[canonical] = struct{}{}
		}
	}
}

// FinishCycle records that a full test cycle completed. It commits absence increments
// for pending-absent candidates and evicts those exceeding MaxAbsentCycles.
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
	s.pendingAbsent = make(map[string]struct{})
	s.lastCycle = time.Now()
	s.cycleCount++
	s.cycleID++
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

// PassingRanked returns the active links of all servable candidates,
// sorted primarily by reliability score descending, then by latency ascending,
// and finally by active link for determinism.
func (s *Store) PassingRanked() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type rankedRecord struct {
		activeLink string
		score      float64
		latency    time.Duration
	}

	servable := make([]rankedRecord, 0, len(s.records))
	for _, rec := range s.records {
		if s.isServableRecordLocked(rec) {
			servable = append(servable, rankedRecord{
				activeLink: rec.ActiveLink,
				score:      rec.Score,
				latency:    rec.Latest.Latency,
			})
		}
	}

	sort.Slice(servable, func(i, j int) bool {
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

// Stats is a snapshot of counts based on canonical Status and servability.
type Stats struct {
	Total        int       `json:"total"`
	Passed       int       `json:"passed"`
	Failed       int       `json:"failed"`
	Inconclusive int       `json:"inconclusive"`
	Servable     int       `json:"servable"`
	LastCycle    time.Time `json:"last_cycle"`
	CycleCount   int       `json:"cycle_count"`
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
