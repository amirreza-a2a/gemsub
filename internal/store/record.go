package store

import "time"

// CandidateRecord holds the complete state and bounded probe history for a single candidate.
type CandidateRecord struct {
	CanonicalLink     string                  `json:"canonical_link"`
	ActiveLink        string                  `json:"active_link"`
	Score             float64                 `json:"score,omitempty"`
	AbsentCycles      int                     `json:"absent_cycles,omitempty"`
	LastPassedLatency time.Duration           `json:"last_passed_latency,omitempty"`
	HasPassed         bool                    `json:"has_passed,omitempty"`
	Latest            Result                  `json:"latest"`
	History           BoundedHistory          `json:"history"`
	Services          map[string]TargetResult `json:"services,omitempty"`
}

// Clone returns a deep copy of CandidateRecord safe for concurrent read access.
func (r *CandidateRecord) Clone() *CandidateRecord {
	if r == nil {
		return nil
	}
	cp := *r
	cp.History = r.History.Clone()
	if len(r.Latest.Warnings) > 0 {
		cp.Latest.Warnings = make([]string, len(r.Latest.Warnings))
		copy(cp.Latest.Warnings, r.Latest.Warnings)
	}
	if len(r.Services) > 0 {
		cp.Services = make(map[string]TargetResult, len(r.Services))
		for k, v := range r.Services {
			cp.Services[k] = v
		}
	}
	if len(r.Latest.Services) > 0 {
		cp.Latest.Services = make(map[string]TargetResult, len(r.Latest.Services))
		for k, v := range r.Latest.Services {
			cp.Latest.Services[k] = v
		}
	}
	return &cp
}
