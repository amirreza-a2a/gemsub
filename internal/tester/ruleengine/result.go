package ruleengine

import (
	"time"

	"gemsub/internal/store"
)

// Result represents the outcome of evaluating an HTTP response through the Stage 2 rule engine pipeline.
type Result struct {
	Status     store.Status
	Category   store.ErrorCategory
	StatusCode int
	Reason     string
	Latency    time.Duration
	Retryable  bool
	RetryAfter *time.Duration
}
