// Package store holds the current pass/fail state of every candidate
// config in memory, and persists a lightweight snapshot to disk so a
// restart doesn't have to wait for a full test cycle before the sub
// server has something to serve.
package store

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
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
	Link                    string        `json:"link,omitempty"`
	Status                  Status        `json:"status"`           // Canonical internal state
	Passed                  bool          `json:"passed,omitempty"` // Outcome of latest probe (Status == StatusPassed)
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

// PrimaryPath returns the physical primary persistence file path.
// If the configured path already ends with ".gz", it is returned as-is;
// otherwise ".gz" is appended.
func (s *Store) PrimaryPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.primaryPathLocked()
}

func (s *Store) primaryPathLocked() string {
	if strings.HasSuffix(s.path, ".gz") {
		return s.path
	}
	return s.path + ".gz"
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
// It checks the primary compressed path (<path>.gz) first, falling back to legacy raw JSON (<path>).
// Gzip compression is detected via magic bytes 0x1f 0x8b regardless of file extension.
// When Version 2 snapshot is found, BoundedHistory is authoritative and Score is recomputed.
// When legacy Version 1 snapshot is found, results are deterministically migrated.
func (s *Store) Load() error {
	primary := s.PrimaryPath()

	s.mu.RLock()
	legacyPath := s.path
	s.mu.RUnlock()

	f, err := os.Open(primary)
	if os.IsNotExist(err) {
		if primary != legacyPath {
			f, err = os.Open(legacyPath)
			if os.IsNotExist(err) {
				return nil // Clean cold start
			}
		} else {
			return nil // Clean cold start
		}
	}
	if err != nil {
		return err
	}
	defer f.Close()

	// Inspect magic bytes (0x1f 0x8b) to detect gzip
	br := bufio.NewReaderSize(f, 256*1024)
	magic, peekErr := br.Peek(2)
	var isGzip bool
	if peekErr == nil && len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		isGzip = true
	}

	var data []byte
	if isGzip {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		defer gz.Close()
		decomp, err := io.ReadAll(gz)
		if err != nil {
			return err
		}
		data = decomp
	} else {
		uncompressed, err := io.ReadAll(br)
		if err != nil {
			return err
		}
		data = uncompressed
	}

	var snap Snapshot
	var decoded bool
	if sDec, ok := fastDecodeSnapshot(data); ok {
		snap = *sDec
		decoded = true
	}
	if !decoded {
		if err := json.Unmarshal(data, &snap); err != nil {
			return err
		}
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
			if rec.Latest.Link == "" {
				rec.Latest.Link = rec.ActiveLink
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
// flush/sync, then rename) using transparent gzip compression.
// A crash mid-write cannot corrupt the primary snapshot or the existing file.
func (s *Store) Save() error {
	s.mu.RLock()
	n := len(s.records)
	recordPool := make([]CandidateRecord, n)
	records := make([]*CandidateRecord, n)

	totalSamples := 0
	for _, rec := range s.records {
		totalSamples += rec.History.Count
	}
	samplePool := make([]ProbeSample, totalSamples)

	sampleOffset := 0
	idx := 0
	for _, rec := range s.records {
		recordPool[idx] = *rec
		if len(rec.Latest.Warnings) > 0 {
			recordPool[idx].Latest.Warnings = slices.Clone(rec.Latest.Warnings)
		}
		if rec.History.Count > 0 {
			samples := samplePool[sampleOffset : sampleOffset+rec.History.Count]
			cap := rec.History.Capacity
			if cap <= 0 || cap > len(rec.History.Samples) {
				cap = len(rec.History.Samples)
			}
			start := rec.History.Start
			if start < 0 || start >= cap {
				start = 0
			}
			for j := 0; j < rec.History.Count; j++ {
				samples[j] = rec.History.Samples[(start+j)%cap]
			}
			recordPool[idx].History.Samples = samples
			recordPool[idx].History.Start = 0
			sampleOffset += rec.History.Count
		} else {
			recordPool[idx].History.Samples = nil
		}
		records[idx] = &recordPool[idx]
		idx++
	}
	snap := Snapshot{
		Version:    2,
		LastCycle:  s.lastCycle,
		CycleCount: s.cycleCount,
		Records:    records,
	}
	s.mu.RUnlock()

	// Sort records deterministically by CanonicalLink without reflection
	slices.SortFunc(snap.Records, func(a, b *CandidateRecord) int {
		return cmp.Compare(a.CanonicalLink, b.CanonicalLink)
	})

	primary := s.PrimaryPath()
	tmp := primary + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	bw := bufio.NewWriterSize(f, 256*1024)
	gw, err := gzip.NewWriterLevel(bw, gzip.BestSpeed)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}

	if err := writeSnapshotJSON(gw, snap); err != nil {
		_ = gw.Close()
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}

	// CRITICAL: gzip.Writer MUST be closed BEFORE f.Sync() so the gzip trailer
	// (CRC-32 and uncompressed length) is flushed to the OS file descriptor.
	if err := gw.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}

	if err := bw.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if err := os.Rename(tmp, primary); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	// If legacy uncompressed file still exists on disk, emit informational log
	s.mu.RLock()
	legacyPath := s.path
	s.mu.RUnlock()
	if primary != legacyPath {
		if _, err := os.Stat(legacyPath); err == nil {
			slog.Info("legacy raw state file is superseded by compressed state", "legacy", legacyPath, "primary", primary)
		}
	}

	return nil
}

const hexChars = "0123456789abcdef"

func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' || c < 0x20 {
			if i > start {
				dst = append(dst, s[start:i]...)
			}
			switch c {
			case '"':
				dst = append(dst, '\\', '"')
			case '\\':
				dst = append(dst, '\\', '\\')
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, `\u00`...)
				dst = append(dst, hexChars[c>>4], hexChars[c&0x0f])
			}
			start = i + 1
		}
	}
	if start == 0 {
		dst = append(dst, s...)
	} else if start < len(s) {
		dst = append(dst, s[start:]...)
	}
	return append(dst, '"')
}

func writeSnapshotJSON(w io.Writer, snap Snapshot) error {
	bw := bufio.NewWriterSize(w, 256*1024)
	scratch := make([]byte, 0, 2048)

	scratch = append(scratch, `{"version":`...)
	scratch = strconv.AppendInt(scratch, int64(snap.Version), 10)
	scratch = append(scratch, `,"last_cycle":"`...)
	scratch = snap.LastCycle.AppendFormat(scratch, time.RFC3339Nano)
	scratch = append(scratch, `","cycle_count":`...)
	scratch = strconv.AppendInt(scratch, int64(snap.CycleCount), 10)
	scratch = append(scratch, `,"records":[`...)
	if _, err := bw.Write(scratch); err != nil {
		return err
	}

	for i, rec := range snap.Records {
		if rec == nil {
			continue
		}
		scratch = scratch[:0]
		if i > 0 {
			scratch = append(scratch, ',')
		}
		scratch = append(scratch, `{"canonical_link":`...)
		scratch = appendJSONString(scratch, rec.CanonicalLink)
		scratch = append(scratch, `,"active_link":`...)
		scratch = appendJSONString(scratch, rec.ActiveLink)
		if rec.AbsentCycles > 0 {
			scratch = append(scratch, `,"absent_cycles":`...)
			scratch = strconv.AppendInt(scratch, int64(rec.AbsentCycles), 10)
		}

		// latest
		scratch = append(scratch, `,"latest":{`...)
		firstL := true
		if rec.Latest.Link != "" && rec.Latest.Link != rec.ActiveLink {
			scratch = append(scratch, `"link":`...)
			scratch = appendJSONString(scratch, rec.Latest.Link)
			firstL = false
		}
		if !firstL {
			scratch = append(scratch, ',')
		}
		scratch = append(scratch, `"status":`...)
		scratch = appendJSONString(scratch, string(rec.Latest.Status))
		scratch = append(scratch, `,"reason":`...)
		scratch = appendJSONString(scratch, rec.Latest.Reason)
		if rec.Latest.Category != "" {
			scratch = append(scratch, `,"category":`...)
			scratch = appendJSONString(scratch, string(rec.Latest.Category))
		}
		if rec.Latest.StatusCode != 0 {
			scratch = append(scratch, `,"status_code":`...)
			scratch = strconv.AppendInt(scratch, int64(rec.Latest.StatusCode), 10)
		}
		scratch = append(scratch, `,"latency":`...)
		scratch = strconv.AppendInt(scratch, int64(rec.Latest.Latency), 10)
		scratch = append(scratch, `,"tested_at":"`...)
		scratch = rec.Latest.TestedAt.AppendFormat(scratch, time.RFC3339Nano)
		scratch = append(scratch, `","attempts":`...)
		scratch = strconv.AppendInt(scratch, int64(rec.Latest.Attempts), 10)
		if rec.Latest.TransportOK {
			scratch = append(scratch, `,"transport_ok":true`...)
		}
		if rec.Latest.TransportLatency > 0 {
			scratch = append(scratch, `,"transport_latency":`...)
			scratch = strconv.AppendInt(scratch, int64(rec.Latest.TransportLatency), 10)
		}
		if rec.Latest.TransportEvidenceKnown {
			scratch = append(scratch, `,"transport_evidence_known":true`...)
		}
		if len(rec.Latest.Warnings) > 0 {
			scratch = append(scratch, `,"warnings":[`...)
			for wIdx, w := range rec.Latest.Warnings {
				if wIdx > 0 {
					scratch = append(scratch, ',')
				}
				scratch = appendJSONString(scratch, w)
			}
			scratch = append(scratch, ']')
		}
		scratch = append(scratch, '}')

		// history
		scratch = append(scratch, `,"history":{"capacity":`...)
		scratch = strconv.AppendInt(scratch, int64(rec.History.Capacity), 10)
		scratch = append(scratch, `,"count":`...)
		scratch = strconv.AppendInt(scratch, int64(rec.History.Count), 10)
		scratch = append(scratch, `,"samples":[`...)
		for sIdx, smp := range rec.History.Samples {
			if sIdx > 0 {
				scratch = append(scratch, ',')
			}
			scratch = append(scratch, `{"cycle_id":`...)
			scratch = strconv.AppendUint(scratch, smp.CycleID, 10)
			scratch = append(scratch, `,"tested_at":"`...)
			scratch = smp.TestedAt.AppendFormat(scratch, time.RFC3339Nano)
			scratch = append(scratch, `","status":`...)
			scratch = appendJSONString(scratch, string(smp.Status))
			scratch = append(scratch, `,"category":`...)
			scratch = appendJSONString(scratch, string(smp.Category))
			if smp.StatusCode != 0 {
				scratch = append(scratch, `,"status_code":`...)
				scratch = strconv.AppendInt(scratch, int64(smp.StatusCode), 10)
			}
			if smp.Latency > 0 {
				scratch = append(scratch, `,"latency":`...)
				scratch = strconv.AppendInt(scratch, int64(smp.Latency), 10)
			}
			scratch = append(scratch, `,"attempts":`...)
			scratch = strconv.AppendInt(scratch, int64(smp.Attempts), 10)
			if smp.TransportOK {
				scratch = append(scratch, `,"transport_ok":true`...)
			}
			if smp.TransportLatency > 0 {
				scratch = append(scratch, `,"transport_latency":`...)
				scratch = strconv.AppendInt(scratch, int64(smp.TransportLatency), 10)
			}
			if smp.TransportEvidenceKnown {
				scratch = append(scratch, `,"transport_evidence_known":true`...)
			}
			scratch = append(scratch, '}')
		}
		scratch = append(scratch, `]}}`...)
		if _, err := bw.Write(scratch); err != nil {
			return err
		}
	}

	if _, err := bw.WriteString("]}"); err != nil {
		return err
	}
	return bw.Flush()
}

type fastJSONParser struct {
	data []byte
	pos  int
}

func (p *fastJSONParser) skipWhitespace() {
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			p.pos++
		} else {
			break
		}
	}
}

func (p *fastJSONParser) peek() byte {
	p.skipWhitespace()
	if p.pos < len(p.data) {
		return p.data[p.pos]
	}
	return 0
}

func (p *fastJSONParser) consume(expected byte) bool {
	p.skipWhitespace()
	if p.pos < len(p.data) && p.data[p.pos] == expected {
		p.pos++
		return true
	}
	return false
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func parseHex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	for i := 0; i < 4; i++ {
		if !isHexDigit(b[i]) {
			return 0, false
		}
	}
	val, err := strconv.ParseUint(string(b[:4]), 16, 16)
	if err != nil {
		return 0, false
	}
	return rune(val), true
}

func unescapeJSON(b []byte) ([]byte, bool) {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '\\' {
			i++
			if i >= len(b) {
				return nil, false
			}
			switch b[i] {
			case '"':
				out = append(out, '"')
			case '\\':
				out = append(out, '\\')
			case '/':
				out = append(out, '/')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				if i+4 >= len(b) {
					return nil, false
				}
				r1, ok := parseHex4(b[i+1 : i+5])
				if !ok {
					return nil, false
				}
				i += 4

				// Check for UTF-16 surrogate pair
				if utf16.IsSurrogate(r1) {
					if i+6 < len(b) && b[i+1] == '\\' && b[i+2] == 'u' {
						r2, ok := parseHex4(b[i+3 : i+7])
						if ok {
							combined := utf16.DecodeRune(r1, r2)
							if combined != unicode.ReplacementChar {
								out = utf8.AppendRune(out, combined)
								i += 6
								continue
							}
						}
					}
					out = utf8.AppendRune(out, unicode.ReplacementChar)
					continue
				}
				out = utf8.AppendRune(out, r1)
			default:
				return nil, false
			}
		} else {
			out = append(out, b[i])
		}
	}
	return out, true
}

func (p *fastJSONParser) parseStringBytes() ([]byte, bool) {
	p.skipWhitespace()
	if p.pos >= len(p.data) || p.data[p.pos] != '"' {
		return nil, false
	}
	p.pos++
	start := p.pos
	hasEscapes := false
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		if b == '"' {
			res := p.data[start:p.pos]
			p.pos++
			if !hasEscapes {
				return res, true
			}
			return unescapeJSON(res)
		}
		if b < 0x20 {
			// RFC 8259 Section 7: Unescaped control characters < 0x20 are forbidden in strings
			return nil, false
		}
		if b == '\\' {
			hasEscapes = true
			p.pos++
			if p.pos >= len(p.data) {
				return nil, false
			}
			esc := p.data[p.pos]
			switch esc {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				p.pos++
			case 'u':
				p.pos++
				if p.pos+4 > len(p.data) {
					return nil, false
				}
				for k := 0; k < 4; k++ {
					if !isHexDigit(p.data[p.pos+k]) {
						return nil, false
					}
				}
				p.pos += 4
			default:
				return nil, false
			}
			continue
		}
		p.pos++
	}
	return nil, false
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func (p *fastJSONParser) parseInt() (int64, bool) {
	p.skipWhitespace()
	if p.pos >= len(p.data) {
		return 0, false
	}
	start := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
		if p.pos >= len(p.data) {
			return 0, false
		}
	}
	if p.data[p.pos] == '0' {
		p.pos++
		if p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			return 0, false // leading zeros forbidden in JSON
		}
	} else if p.data[p.pos] >= '1' && p.data[p.pos] <= '9' {
		p.pos++
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	} else {
		return 0, false
	}
	if p.pos < len(p.data) {
		b := p.data[p.pos]
		if b != ',' && b != '}' && b != ']' && b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(string(p.data[start:p.pos]), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (p *fastJSONParser) parseUint() (uint64, bool) {
	p.skipWhitespace()
	if p.pos >= len(p.data) {
		return 0, false
	}
	start := p.pos
	if p.data[p.pos] == '0' {
		p.pos++
		if p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			return 0, false
		}
	} else if p.data[p.pos] >= '1' && p.data[p.pos] <= '9' {
		p.pos++
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	} else {
		return 0, false
	}
	if p.pos < len(p.data) {
		b := p.data[p.pos]
		if b != ',' && b != '}' && b != ']' && b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(string(p.data[start:p.pos]), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (p *fastJSONParser) parseBool() (bool, bool) {
	p.skipWhitespace()
	if p.pos+4 <= len(p.data) && bytes.Equal(p.data[p.pos:p.pos+4], []byte("true")) {
		end := p.pos + 4
		if end < len(p.data) && isIdentChar(p.data[end]) {
			return false, false
		}
		p.pos = end
		return true, true
	}
	if p.pos+5 <= len(p.data) && bytes.Equal(p.data[p.pos:p.pos+5], []byte("false")) {
		end := p.pos + 5
		if end < len(p.data) && isIdentChar(p.data[end]) {
			return false, false
		}
		p.pos = end
		return false, true
	}
	return false, false
}

func (p *fastJSONParser) skipValue() bool {
	p.skipWhitespace()
	if p.pos >= len(p.data) {
		return false
	}
	b := p.data[p.pos]
	if b == '"' {
		_, ok := p.parseStringBytes()
		return ok
	}
	if b == '{' {
		p.pos++
		p.skipWhitespace()
		if p.consume('}') {
			return true
		}
		for {
			_, ok := p.parseStringBytes()
			if !ok || !p.consume(':') {
				return false
			}
			if !p.skipValue() {
				return false
			}
			p.skipWhitespace()
			if p.consume('}') {
				return true
			}
			if !p.consume(',') {
				return false
			}
			p.skipWhitespace()
			if p.pos < len(p.data) && p.data[p.pos] == '}' {
				return false // trailing comma
			}
		}
	}
	if b == '[' {
		p.pos++
		p.skipWhitespace()
		if p.consume(']') {
			return true
		}
		for {
			if !p.skipValue() {
				return false
			}
			p.skipWhitespace()
			if p.consume(']') {
				return true
			}
			if !p.consume(',') {
				return false
			}
			p.skipWhitespace()
			if p.pos < len(p.data) && p.data[p.pos] == ']' {
				return false // trailing comma
			}
		}
	}
	if b == 't' || b == 'f' {
		_, ok := p.parseBool()
		return ok
	}
	if b == 'n' {
		if p.pos+4 <= len(p.data) && bytes.Equal(p.data[p.pos:p.pos+4], []byte("null")) {
			end := p.pos + 4
			if end < len(p.data) && isIdentChar(p.data[end]) {
				return false
			}
			p.pos = end
			return true
		}
		return false
	}
	if b == '-' || (b >= '0' && b <= '9') {
		return p.parseNumber()
	}
	return false
}

func (p *fastJSONParser) parseNumber() bool {
	p.skipWhitespace()
	if p.pos >= len(p.data) {
		return false
	}
	if p.data[p.pos] == '-' {
		p.pos++
		if p.pos >= len(p.data) {
			return false
		}
	}
	if p.data[p.pos] == '0' {
		p.pos++
		if p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			return false // leading zero forbidden
		}
	} else if p.data[p.pos] >= '1' && p.data[p.pos] <= '9' {
		p.pos++
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	} else {
		return false
	}
	// Frac
	if p.pos < len(p.data) && p.data[p.pos] == '.' {
		p.pos++
		if p.pos >= len(p.data) || p.data[p.pos] < '0' || p.data[p.pos] > '9' {
			return false
		}
		p.pos++
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	}
	// Exp
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
		}
		if p.pos >= len(p.data) || p.data[p.pos] < '0' || p.data[p.pos] > '9' {
			return false
		}
		p.pos++
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.pos < len(p.data) {
		b := p.data[p.pos]
		if b != ',' && b != '}' && b != ']' && b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return false
		}
	}
	return true
}

func fastDecodeSnapshot(data []byte) (*Snapshot, bool) {
	p := &fastJSONParser{data: data}
	p.skipWhitespace()
	if !p.consume('{') {
		return nil, false
	}
	snap := &Snapshot{}
	var hasRecords bool

	p.skipWhitespace()
	if !p.consume('}') {
		for {
			key, ok := p.parseStringBytes()
			if !ok || !p.consume(':') {
				return nil, false
			}
			switch string(key) {
			case "version":
				v, ok := p.parseInt()
				if !ok {
					return nil, false
				}
				snap.Version = int(v)
			case "last_cycle":
				s, ok := p.parseStringBytes()
				if !ok {
					return nil, false
				}
				t, err := time.Parse(time.RFC3339Nano, string(s))
				if err != nil {
					t, err = time.Parse(time.RFC3339, string(s))
					if err != nil {
						return nil, false
					}
				}
				snap.LastCycle = t
			case "cycle_count":
				c, ok := p.parseInt()
				if !ok {
					return nil, false
				}
				snap.CycleCount = int(c)
			case "records":
				hasRecords = true
				if !p.consume('[') {
					return nil, false
				}
				records := make([]*CandidateRecord, 0, 60000)
				p.skipWhitespace()
				if !p.consume(']') {
					for {
						if !p.consume('{') {
							return nil, false
						}
						rec := &CandidateRecord{}
						p.skipWhitespace()
						if !p.consume('}') {
							for {
								rkey, ok := p.parseStringBytes()
								if !ok || !p.consume(':') {
									return nil, false
								}
								switch string(rkey) {
								case "canonical_link":
									s, ok := p.parseStringBytes()
									if !ok {
										return nil, false
									}
									rec.CanonicalLink = string(s)
								case "active_link":
									s, ok := p.parseStringBytes()
									if !ok {
										return nil, false
									}
									rec.ActiveLink = string(s)
								case "absent_cycles":
									c, ok := p.parseInt()
									if !ok {
										return nil, false
									}
									rec.AbsentCycles = int(c)
								case "score", "has_passed", "last_passed_latency":
									if !p.skipValue() {
										return nil, false
									}
								case "latest":
									if !p.consume('{') {
										return nil, false
									}
									p.skipWhitespace()
									if !p.consume('}') {
										for {
											lkey, ok := p.parseStringBytes()
											if !ok || !p.consume(':') {
												return nil, false
											}
											switch string(lkey) {
											case "link":
												s, ok := p.parseStringBytes()
												if !ok {
													return nil, false
												}
												rec.Latest.Link = string(s)
											case "status":
												s, ok := p.parseStringBytes()
												if !ok {
													return nil, false
												}
												rec.Latest.Status = Status(s)
											case "reason":
												s, ok := p.parseStringBytes()
												if !ok {
													return nil, false
												}
												rec.Latest.Reason = string(s)
											case "category":
												s, ok := p.parseStringBytes()
												if !ok {
													return nil, false
												}
												rec.Latest.Category = ErrorCategory(s)
											case "status_code":
												sc, ok := p.parseInt()
												if !ok {
													return nil, false
												}
												rec.Latest.StatusCode = int(sc)
											case "latency":
												lat, ok := p.parseInt()
												if !ok {
													return nil, false
												}
												rec.Latest.Latency = time.Duration(lat)
											case "tested_at":
												s, ok := p.parseStringBytes()
												if !ok {
													return nil, false
												}
												t, err := time.Parse(time.RFC3339Nano, string(s))
												if err != nil {
													t, err = time.Parse(time.RFC3339, string(s))
													if err != nil {
														return nil, false
													}
												}
												rec.Latest.TestedAt = t
											case "attempts":
												att, ok := p.parseInt()
												if !ok {
													return nil, false
												}
												rec.Latest.Attempts = int(att)
											case "transport_ok":
												tok, ok := p.parseBool()
												if !ok {
													return nil, false
												}
												rec.Latest.TransportOK = tok
											case "transport_latency":
												tlat, ok := p.parseInt()
												if !ok {
													return nil, false
												}
												rec.Latest.TransportLatency = time.Duration(tlat)
											case "transport_evidence_known":
												tek, ok := p.parseBool()
												if !ok {
													return nil, false
												}
												rec.Latest.TransportEvidenceKnown = tek
											case "warnings":
												if !p.consume('[') {
													return nil, false
												}
												var warnings []string
												p.skipWhitespace()
												if !p.consume(']') {
													for {
														ws, ok := p.parseStringBytes()
														if !ok {
															return nil, false
														}
														warnings = append(warnings, string(ws))
														p.skipWhitespace()
														if p.consume(']') {
															break
														}
														if !p.consume(',') {
															return nil, false
														}
														p.skipWhitespace()
														if p.pos < len(p.data) && p.data[p.pos] == ']' {
															return nil, false // trailing comma
														}
													}
												}
												rec.Latest.Warnings = warnings
											default:
												if !p.skipValue() {
													return nil, false
												}
											}
											p.skipWhitespace()
											if p.consume('}') {
												break
											}
											if !p.consume(',') {
												return nil, false
											}
											p.skipWhitespace()
											if p.pos < len(p.data) && p.data[p.pos] == '}' {
												return nil, false // trailing comma
											}
										}
									}
								case "history":
									if !p.consume('{') {
										return nil, false
									}
									p.skipWhitespace()
									if !p.consume('}') {
										for {
											hkey, ok := p.parseStringBytes()
											if !ok || !p.consume(':') {
												return nil, false
											}
											switch string(hkey) {
											case "capacity":
												cap, ok := p.parseInt()
												if !ok {
													return nil, false
												}
												rec.History.Capacity = int(cap)
											case "count":
												cnt, ok := p.parseInt()
												if !ok {
													return nil, false
												}
												rec.History.Count = int(cnt)
											case "start":
												st, ok := p.parseInt()
												if !ok {
													return nil, false
												}
												rec.History.Start = int(st)
											case "samples":
												if !p.consume('[') {
													return nil, false
												}
												samples := make([]ProbeSample, 0, rec.History.Count)
												p.skipWhitespace()
												if !p.consume(']') {
													for {
														if !p.consume('{') {
															return nil, false
														}
														var smp ProbeSample
														p.skipWhitespace()
														if !p.consume('}') {
															for {
																skey, ok := p.parseStringBytes()
																if !ok || !p.consume(':') {
																	return nil, false
																}
																switch string(skey) {
																case "cycle_id":
																	cid, ok := p.parseUint()
																	if !ok {
																		return nil, false
																	}
																	smp.CycleID = cid
																case "tested_at":
																	s, ok := p.parseStringBytes()
																	if !ok {
																		return nil, false
																	}
																	t, err := time.Parse(time.RFC3339Nano, string(s))
																	if err != nil {
																		t, err = time.Parse(time.RFC3339, string(s))
																		if err != nil {
																			return nil, false
																		}
																	}
																	smp.TestedAt = t
																case "status":
																	s, ok := p.parseStringBytes()
																	if !ok {
																		return nil, false
																	}
																	smp.Status = Status(s)
																case "category":
																	s, ok := p.parseStringBytes()
																	if !ok {
																		return nil, false
																	}
																	smp.Category = ErrorCategory(s)
																case "status_code":
																	sc, ok := p.parseInt()
																	if !ok {
																		return nil, false
																	}
																	smp.StatusCode = int(sc)
																case "latency":
																	lat, ok := p.parseInt()
																	if !ok {
																		return nil, false
																	}
																	smp.Latency = time.Duration(lat)
																case "attempts":
																	att, ok := p.parseInt()
																	if !ok {
																		return nil, false
																	}
																	smp.Attempts = int(att)
																case "transport_ok":
																	tok, ok := p.parseBool()
																	if !ok {
																		return nil, false
																	}
																	smp.TransportOK = tok
																case "transport_latency":
																	tlat, ok := p.parseInt()
																	if !ok {
																		return nil, false
																	}
																	smp.TransportLatency = time.Duration(tlat)
																case "transport_evidence_known":
																	tek, ok := p.parseBool()
																	if !ok {
																		return nil, false
																	}
																	smp.TransportEvidenceKnown = tek
																default:
																	if !p.skipValue() {
																		return nil, false
																	}
																}
																p.skipWhitespace()
																if p.consume('}') {
																	break
																}
																if !p.consume(',') {
																	return nil, false
																}
																p.skipWhitespace()
																if p.pos < len(p.data) && p.data[p.pos] == '}' {
																	return nil, false // trailing comma
																}
															}
														}
														samples = append(samples, smp)
														p.skipWhitespace()
														if p.consume(']') {
															break
														}
														if !p.consume(',') {
															return nil, false
														}
														p.skipWhitespace()
														if p.pos < len(p.data) && p.data[p.pos] == ']' {
															return nil, false // trailing comma
														}
													}
												}
												rec.History.Samples = samples
											default:
												if !p.skipValue() {
													return nil, false
												}
											}
											p.skipWhitespace()
											if p.consume('}') {
												break
											}
											if !p.consume(',') {
												return nil, false
											}
											p.skipWhitespace()
											if p.pos < len(p.data) && p.data[p.pos] == '}' {
												return nil, false // trailing comma
											}
										}
									}
								default:
									if !p.skipValue() {
										return nil, false
									}
								}
								p.skipWhitespace()
								if p.consume('}') {
									break
								}
								if !p.consume(',') {
									return nil, false
								}
								p.skipWhitespace()
								if p.pos < len(p.data) && p.data[p.pos] == '}' {
									return nil, false // trailing comma
								}
							}
						}
						records = append(records, rec)
						p.skipWhitespace()
						if p.consume(']') {
							break
						}
						if !p.consume(',') {
							return nil, false
						}
						p.skipWhitespace()
						if p.pos < len(p.data) && p.data[p.pos] == ']' {
							return nil, false // trailing comma
						}
					}
				}
				snap.Records = records
			default:
				if !p.skipValue() {
					return nil, false
				}
			}
			p.skipWhitespace()
			if p.consume('}') {
				break
			}
			if !p.consume(',') {
				return nil, false
			}
			p.skipWhitespace()
			if p.pos < len(p.data) && p.data[p.pos] == '}' {
				return nil, false // trailing comma
			}
		}
	}

	if !hasRecords {
		return nil, false
	}

	// CRITICAL: Proof of full consumption. Only whitespace is allowed after root object.
	p.skipWhitespace()
	if p.pos != len(p.data) {
		return nil, false // trailing garbage!
	}

	return snap, true
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

// StartCycle marks the beginning of a new test cycle. Candidate links are canonicalized
// outside the Store lock to keep exclusive lock hold time minimal (<25ms on 50k–62k links).
// Candidates present in currentLinks are marked pending-present (retaining servability without
// prematurely mutating record state). Candidates missing from currentLinks are marked pending-absent.
// Absence increments and resets are only committed atomically to candidate records upon successful FinishCycle.
func (s *Store) StartCycle(currentLinks map[string]struct{}) {
	canonicalPresent := CanonicalizeLinks(currentLinks)
	s.startCycleLocked(canonicalPresent)
}

func (s *Store) startCycleLocked(canonicalPresent map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.revision++

	if len(canonicalPresent) == 0 {
		s.pendingPresent = make(map[string]struct{})
		s.pendingAbsent = make(map[string]struct{}, len(s.records))
		for canonical := range s.records {
			s.pendingAbsent[canonical] = struct{}{}
		}
		return
	}

	s.pendingPresent = canonicalPresent

	absentHint := 0
	if len(s.records) > len(canonicalPresent) {
		absentHint = len(s.records) - len(canonicalPresent)
	}
	s.pendingAbsent = make(map[string]struct{}, absentHint)

	for canonical := range s.records {
		if _, ok := s.pendingPresent[canonical]; !ok {
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

// Count returns the total number of stored candidate records in O(1) time
// without traversing records or evaluating servability policies.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// CycleCount returns the completed cycle count in O(1) time under RLock.
func (s *Store) CycleCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cycleCount
}

// LastCycle returns the timestamp of the last completed cycle in O(1) time under RLock.
func (s *Store) LastCycle() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastCycle
}
