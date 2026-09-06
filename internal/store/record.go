package store

// CandidateRecord holds the complete state and bounded probe history for a single candidate.
type CandidateRecord struct {
	CanonicalLink string         `json:"canonical_link"`
	ActiveLink    string         `json:"active_link"`
	Score         float64        `json:"score"`
	AbsentCycles  int            `json:"absent_cycles"`
	Latest        Result         `json:"latest"`
	History       BoundedHistory `json:"history"`
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
	return &cp
}
