// Package store holds the current pass/fail state of every candidate
// config in memory, and persists a lightweight snapshot to disk so a
// restart doesn't have to wait for a full test cycle before the sub
// server has something to serve.
package store

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Result is the outcome of testing a single candidate link.
type Result struct {
	Link     string        `json:"link"`
	Passed   bool          `json:"passed"`
	Reason   string        `json:"reason"`
	Latency  time.Duration `json:"latency"`
	TestedAt time.Time     `json:"tested_at"`
}

// Snapshot is the full persisted state.
type Snapshot struct {
	Results    []Result  `json:"results"`
	LastCycle  time.Time `json:"last_cycle"`
	CycleCount int       `json:"cycle_count"`
}

// Store is safe for concurrent use.
type Store struct {
	mu         sync.RWMutex
	results    map[string]Result // keyed by link
	lastCycle  time.Time
	cycleCount int
	path       string
}

// New creates an empty store bound to the given snapshot file path.
// Call Load to populate it from disk, if a previous snapshot exists.
func New(path string) *Store {
	return &Store{
		results: make(map[string]Result),
		path:    path,
	}
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
		s.results[r.Link] = r
	}
	s.lastCycle = snap.LastCycle
	s.cycleCount = snap.CycleCount
	return nil
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

// Put records the result of testing one candidate. Safe to call
// concurrently from many tester workers.
func (s *Store) Put(r Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[r.Link] = r
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

// Passing returns the links that currently pass, in stable order.
func (s *Store) Passing() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for link, r := range s.results {
		if r.Passed {
			out = append(out, link)
		}
	}
	return out
}

// Stats is a snapshot of counts, for the TUI.
type Stats struct {
	Total      int
	Passed     int
	Failed     int
	LastCycle  time.Time
	CycleCount int
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
		if r.Passed {
			st.Passed++
		} else {
			st.Failed++
		}
	}
	return st
}

// All returns every current result, for the TUI's detailed view.
func (s *Store) All() []Result {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Result, 0, len(s.results))
	for _, r := range s.results {
		out = append(out, r)
	}
	return out
}
