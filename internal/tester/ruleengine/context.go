// Package ruleengine implements the Stage 2 declarative rule engine primitives,
// signal extractors, match operators, and condition trees.
//
// It is a modular, pure evaluation layer with no dependencies on target-specific
// classifiers (gemini, claude) or sing-box transports.
package ruleengine

import (
	"net/http"
	"net/url"
	"time"
)

// EvaluationContext holds immutable facts extracted from a probe HTTP response
// and measured execution metrics.
type EvaluationContext struct {
	StatusCode         int
	FinalURL           *url.URL
	Headers            http.Header
	Cookies            []*http.Cookie
	Title              string
	TitleFound         bool
	RawBody            []byte
	NormalizedBody     string
	CanonicalLink      string
	CanonicalLinkFound bool
	RetryAfter         *time.Duration
	MeasuredLatency    time.Duration
}
