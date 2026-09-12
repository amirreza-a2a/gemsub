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

// PublishingStarted is emitted when a subscription publication begins.
type PublishingStarted struct {
	StartedAt  time.Time `json:"started_at"`
	Repository string    `json:"repository"`
	Branch     string    `json:"branch"`
}

// PublishingFinished is emitted when subscription publication completes successfully.
type PublishingFinished struct {
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Duration   time.Duration `json:"duration"`
	Repository string        `json:"repository"`
	Branch     string        `json:"branch"`
	Commit     string        `json:"commit,omitempty"`
}

// PublishingFailed is emitted when subscription publication fails.
type PublishingFailed struct {
	StartedAt  time.Time     `json:"started_at"`
	FailedAt   time.Time     `json:"failed_at"`
	Duration   time.Duration `json:"duration"`
	Repository string        `json:"repository"`
	Branch     string        `json:"branch"`
	Error      string        `json:"error"`
}

// SchedulerPaused is emitted when automatic timer-based cycle execution is paused.
type SchedulerPaused struct {
	PausedAt time.Time `json:"paused_at"`
}

// SchedulerResumed is emitted when automatic timer-based cycle execution is resumed.
type SchedulerResumed struct {
	ResumedAt time.Time `json:"resumed_at"`
}
