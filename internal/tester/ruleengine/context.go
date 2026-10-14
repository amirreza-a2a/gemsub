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

	"gemsub/internal/tester/classifiercore"
	"gemsub/internal/tester/textutil"
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

// BuildEvaluationContext constructs an immutable EvaluationContext from an HTTP response,
// payload body, and measured latency.
//
// Normalization of the body, extraction of the HTML title, extraction of the canonical link,
// and parsing of Retry-After headers are performed exactly once during construction,
// ensuring the pipeline executes zero re-normalization during rule evaluation.
// Payloads exceeding classifiercore.MaxInspectBytes (1MB) are capped.
func BuildEvaluationContext(resp *http.Response, body []byte, measuredLatency time.Duration) *EvaluationContext {
	if len(body) > classifiercore.MaxInspectBytes {
		body = body[:classifiercore.MaxInspectBytes]
	}

	ctx := &EvaluationContext{
		RawBody:         body,
		NormalizedBody:  textutil.NormalizeBytes(body),
		MeasuredLatency: classifiercore.ClampLatency(measuredLatency),
	}

	ctx.Title, ctx.TitleFound = classifiercore.ExtractTitle(body)
	ctx.CanonicalLink, ctx.CanonicalLinkFound = classifiercore.ExtractCanonicalLink(body)

	if resp != nil {
		ctx.StatusCode = resp.StatusCode
		ctx.Headers = resp.Header
		ctx.Cookies = resp.Cookies()
		if resp.Request != nil {
			ctx.FinalURL = resp.Request.URL
		}
		if resp.Header != nil {
			ctx.RetryAfter = textutil.ParseRetryAfter(resp.Header.Get("Retry-After"))
		}
	}

	return ctx
}
