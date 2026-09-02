// Package store holds the current pass/fail state of every candidate
// config in memory, and persists a lightweight snapshot to disk so a
// restart doesn't have to wait for a full test cycle before the sub
// server has something to serve.
package store

import (
	"encoding/json"
	"os"
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
	Passed                  bool          `json:"passed"` // Servability projection for backward compatibility
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
	Results    []Result  `json:"results"`
	LastCycle  time.Time `json:"last_cycle"`
	CycleCount int       `json:"cycle_count"`
}

// Store is safe for concurrent use.
type Store struct {
	mu                    sync.RWMutex
	results               map[string]Result // keyed by link
	lastCycle             time.Time
	cycleCount            int
	path                  string
	maxInconclusiveCycles int
}

// New creates an empty store bound to the given snapshot file path and retention policy.
// Call Load to populate it from disk, if a previous snapshot exists.
func New(path string, maxInconclusiveCycles int) *Store {
	if maxInconclusiveCycles <= 0 {
		maxInconclusiveCycles = 2
	}
	return &Store{
		results:               make(map[string]Result),
		path:                  path,
		maxInconclusiveCycles: maxInconclusiveCycles,
	}
}

// IsServable returns true if candidate passes directly or retains last-known-good status.
func (s *Store) IsServable(r Result) bool {
	if r.Status == StatusPassed {
		return true
	}
	if r.Status == StatusInconclusive && r.PreviouslyPassed && r.ConsecutiveInconclusive <= s.maxInconclusiveCycles {
		return true
	}
	return false
}

// Load reads a previously saved snapshot from disk, if present.
// Missing file is not an error — it just means a cold start.
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
	for _, r := range snap.Results {
		// Canonical status migration for older state files
		if r.Status == "" {
			if r.Passed {
				r.Status = StatusPassed
				r.PreviouslyPassed = true
			} else {
				r.Status = StatusFailed
				r.PreviouslyPassed = false
			}
		}
		if r.Category == "" {
			if r.Status == StatusPassed {
				r.Category = ErrNone
			} else {
				r.Category = deriveCategory(r.Reason, r.StatusCode)
			}
		}
		// Ensure projection consistency
		r.Passed = s.IsServable(r)
		s.results[r.Link] = r
	}
	s.lastCycle = snap.LastCycle
	s.cycleCount = snap.CycleCount
	return nil
}

func deriveCategory(reason string, statusCode int) ErrorCategory {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "regional block") || strings.Contains(lower, "supported in your country"):
		return ErrRegionBlocked
	case statusCode == 403 || strings.Contains(lower, "status 403"):
		return ErrTargetDenied
	case strings.Contains(lower, "unexpected http response status: 429"):
		return ErrProxyRateLimited
	case statusCode == 429 || strings.Contains(lower, "status 429"):
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

// Save writes the current state to disk, atomically (write to temp
// file then rename) so a crash mid-write can't corrupt the snapshot.
func (s *Store) Save() error {
	s.mu.RLock()
	snap := Snapshot{
		Results:    make([]Result, 0, len(s.results)),
		LastCycle:  s.lastCycle,
		CycleCount: s.cycleCount,
	}
	for _, r := range s.results {
		snap.Results = append(snap.Results, r)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// PutWithTransition applies canonical state transitions and updates servability projection.
func (s *Store) PutWithTransition(r Result) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, exists := s.results[r.Link]

	switch r.Status {
	case StatusPassed:
		r.PreviouslyPassed = true
		r.ConsecutiveInconclusive = 0
	case StatusFailed:
		r.PreviouslyPassed = false
		r.ConsecutiveInconclusive = 0
	case StatusInconclusive:
		if exists {
			r.PreviouslyPassed = old.PreviouslyPassed
			r.ConsecutiveInconclusive = old.ConsecutiveInconclusive + 1
		} else {
			r.PreviouslyPassed = false
			r.ConsecutiveInconclusive = 1
		}
	default:
		r.Status = StatusFailed
		r.PreviouslyPassed = false
		r.ConsecutiveInconclusive = 0
	}

	// Compute servability projection for backward compatibility
	r.Passed = s.IsServable(r)

	// Preserve existing warnings if new result doesn't have any
	if len(r.Warnings) == 0 && exists && len(old.Warnings) > 0 {
		r.Warnings = old.Warnings
	}

	s.results[r.Link] = r
}

// Put records the result of testing one candidate. Safe to call
// concurrently from many tester workers.
func (s *Store) Put(r Result) {
	s.PutWithTransition(r)
}

// StartCycle marks the beginning of a new fetch+test cycle and drops
// results for links that are no longer present in the fresh candidate
// set (so stale entries from a previous subscription don't linger).
func (s *Store) StartCycle(currentLinks map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for link := range s.results {
		if _, ok := currentLinks[link]; !ok {
			delete(s.results, link)
		}
	}
}

// FinishCycle records that a full test cycle completed.
func (s *Store) FinishCycle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCycle = time.Now()
	s.cycleCount++
}

// Passing returns the links that currently pass (or retain last-known-good), in stable order.
func (s *Store) Passing() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for link, r := range s.results {
		if s.IsServable(r) {
			out = append(out, link)
		}
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
		Total:      len(s.results),
		LastCycle:  s.lastCycle,
		CycleCount: s.cycleCount,
	}
	for _, r := range s.results {
		switch r.Status {
		case StatusPassed:
			st.Passed++
		case StatusFailed:
			st.Failed++
		case StatusInconclusive:
			st.Inconclusive++
		}
		if s.IsServable(r) {
			st.Servable++
		}
	}
	return st
}

// All returns every current result, for detailed inspection.
func (s *Store) All() []Result {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Result, 0, len(s.results))
	for _, r := range s.results {
		out = append(out, r)
	}
	return out
}
