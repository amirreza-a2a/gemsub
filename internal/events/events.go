package events

import (
	"time"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
)

// ConfigUpdated is emitted when configuration has been successfully updated and persisted.
type ConfigUpdated = config.ConfigUpdated

// ProgressMetrics contains real-time counts during probe execution and cycle lifecycle.
type ProgressMetrics struct {
	Completed    int
	Total        int
	Passed       int
	Failed       int
	Inconclusive int
}

// CycleStarted is emitted when a new scheduler cycle begins.
type CycleStarted struct {
	StartedAt time.Time
}

// CandidatesLoaded is emitted after source links have been fetched and parsed into candidates.
type CandidatesLoaded struct {
	Total      int
	Candidates []parser.Candidate
}

// ProbeCompleted is emitted after each probe finishes during pool execution.
type ProbeCompleted struct {
	ProgressMetrics
	Result store.Result
}

// CycleFinished is emitted when a scheduler cycle concludes (either completed, aborted, or cancelled).
type CycleFinished struct {
	ProgressMetrics
	Duration  time.Duration
	Cancelled bool
	Servable  int
}
