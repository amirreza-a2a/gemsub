package viewmodel

import (
	"log/slog"
	"time"
)

// CycleStatus is limited to lifecycle states explicitly observable from the existing scheduler lifecycle and cancellation mechanisms.
type CycleStatus string

const (
	CycleIdle    CycleStatus = "Idle"
	CycleRunning CycleStatus = "Running"
)

// FilterMode controls candidate projection filtering in the table view.
type FilterMode int

const (
	FilterAll FilterMode = iota
	FilterGemini
	FilterGeneric
)

// HeaderViewModel contains global cycle stats and progress metrics.
type HeaderViewModel struct {
	CycleCount           int
	LastCycle            time.Time
	CycleStatus          CycleStatus
	ProgressCurrent      int
	ProgressTotal        int
	TotalCandidates      int
	PassedCount          int // Active cycle passed count
	FailedCount          int // Active cycle failed count
	InconclusiveCount    int // Active cycle inconclusive count
	ServableCount        int
	GenericServableCount int
}

// CandidateRowViewModel represents an opaque, pre-sorted candidate row for the table.
type CandidateRowViewModel struct {
	ID                     string // Opaque presentation identifier
	Protocol               string // vless, vmess, trojan, ss
	Endpoint               string // host:port
	Remark                 string // user remark/tag
	Status                 string // PASS, FAIL, INCON, PEND, BLOCKED, DENIED
	ScoreFormatted         string // e.g. "0.85" or "---"
	LatencyFormatted       string // e.g. "142ms" or "---"
	HistoryGlyphs          string // e.g. "[●●○×●●●●●●]"
	Servable               bool
	NetworkHealthy         bool
	TransportOK            bool
	TransportEvidenceKnown bool
	TransportLatency       time.Duration
	HasPassed              bool
}

// SampleViewModel represents a single historical observation in bounded history.
type SampleViewModel struct {
	Index      int
	Age        string
	Status     string
	Category   string
	StatusCode int
	Latency    time.Duration
	Attempts   int
}

// CandidateDetailViewModel contains complete inspection data for the selected candidate.
type CandidateDetailViewModel struct {
	ID                     string // Opaque presentation identifier
	MaskedLink             string // e.g. vless://[REDACTED]@host:port?...
	Protocol               string
	Endpoint               string
	Host                   string
	Port                   string
	Path                   string
	SNI                    string
	Remark                 string
	Status                 string
	Category               string
	Reason                 string
	StatusCode             int
	Score                  float64
	ScoreFormatted         string
	Servable               bool
	ServabilityGate        string // Failure reason if unservable
	NetworkHealthy         bool
	TransportEvidenceKnown bool
	TransportOK            bool
	TransportLatency       time.Duration
	HasPassed              bool
	ProvenLatencyFormatted string // e.g. "120ms" or "---" if unproven
	AbsentCycles           int
	TestedAt               time.Time
	Latency                time.Duration // Duration of latest probe
	Attempts               int
	Warnings               []string
	Samples                []SampleViewModel
}

// LogViewModel contains log viewer display state.
type LogViewModel struct {
	Lines        []string
	ScrollOffset int
	MinLevel     slog.Level
	TotalCount   int
	FollowMode   bool
}

// SnapshotViewModel bundles presentation viewmodels assembled from authoritative reads.
type SnapshotViewModel struct {
	Header HeaderViewModel
	Rows   []CandidateRowViewModel
	Logs   LogViewModel
}
