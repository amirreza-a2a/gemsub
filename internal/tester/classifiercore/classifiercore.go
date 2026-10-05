// Package classifiercore provides shared Stage 2 HTTP response classification primitives
// used across target application compatibility probes (such as Gemini and Claude).
//
// This package is pure and side-effect-free, depending only on net/http, time,
// internal/store, and internal/tester/textutil. It must never depend on target-specific
// packages (e.g. gemini, claude) or sing-box transports.
package classifiercore

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/textutil"
)

// MaxInspectBytes bounds payload inspection to 1MB.
const MaxInspectBytes = 1 << 20

// TitleRegex matches HTML document title case-insensitively.
var TitleRegex = regexp.MustCompile(`(?i)<title[^>]*>([\s\S]*?)</title>`)

// ExtractTitle extracts the title tag content from an HTML body if present.
func ExtractTitle(body []byte) (string, bool) {
	matches := TitleRegex.FindSubmatch(body)
	if len(matches) > 1 {
		return string(matches[1]), true
	}
	return "", false
}

// ClampLatency ensures a measured latency duration is strictly positive.
// On operating systems with coarse timer or monotonic-clock resolution,
// very fast completed requests can quantize to an elapsed measurement of 0s.
// Since downstream consumers treat Latency <= 0 as unmeasured/unavailable,
// any completed probe must be recorded with a positive duration (falling back
// to time.Nanosecond).
func ClampLatency(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Nanosecond
	}
	return d
}

// CommonStatusResult represents the classification outcome of an explicitly handled HTTP status code.
type CommonStatusResult struct {
	Status     store.Status
	Category   store.ErrorCategory
	StatusCode int
	Reason     string
	Retryable  bool
	RetryAfter *time.Duration
}

// ClassifyCommonHTTPStatus evaluates an HTTP status code and response headers against the
// common Stage 2 status codes shared across targets (429, 503, 500, 502, 504, 403).
// Returns matched == true if the status code was explicitly handled.
//
// Critical constraint: 5xx handling matches ONLY 500, 502, 504 (and 503 as retryable service unavailable).
// Status codes outside this explicit set (e.g. 501, 505, 599, 400, 404) return matched == false,
// allowing callers to apply their fallback unexpected-status handling.
func ClassifyCommonHTTPStatus(statusCode int, header http.Header) (CommonStatusResult, bool) {
	switch statusCode {
	case http.StatusTooManyRequests: // 429
		var retryAfter *time.Duration
		if header != nil {
			retryAfter = textutil.ParseRetryAfter(header.Get("Retry-After"))
		}
		return CommonStatusResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetRateLimited,
			StatusCode: http.StatusTooManyRequests,
			Reason:     "target HTTP 429 Too Many Requests",
			Retryable:  true,
			RetryAfter: retryAfter,
		}, true

	case http.StatusServiceUnavailable: // 503
		return CommonStatusResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetError,
			StatusCode: http.StatusServiceUnavailable,
			Reason:     "target HTTP 503 Service Unavailable",
			Retryable:  true,
		}, true

	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout: // 500, 502, 504
		return CommonStatusResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetError,
			StatusCode: statusCode,
			Reason:     fmt.Sprintf("target HTTP %d", statusCode),
			Retryable:  false,
		}, true

	case http.StatusForbidden: // 403
		return CommonStatusResult{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetDenied,
			StatusCode: http.StatusForbidden,
			Reason:     "target HTTP 403 Forbidden",
			Retryable:  false,
		}, true

	default:
		return CommonStatusResult{}, false
	}
}

// CanonicalInterceptionPhrases contains the five canonical phrases indicating captive portal or CDN interception.
var CanonicalInterceptionPhrases = []string{
	"cf-chl-bypass",
	"attention required! | cloudflare",
	"just a moment...",
	"routeros",
	"mikrotik",
}

// InterceptionReason is the canonical failure reason emitted when captive portal or CDN interception is detected.
const InterceptionReason = "interception: CDN challenge or captive portal page"

// DetectInterception inspects a normalized response body for captive portal, CDN challenge,
// or transparent proxy interception markers across all five canonical phrases.
func DetectInterception(normalizedBody string) (store.ErrorCategory, string, bool) {
	for _, phrase := range CanonicalInterceptionPhrases {
		if strings.Contains(normalizedBody, phrase) {
			return store.ErrTargetOther, InterceptionReason, true
		}
	}
	return store.ErrNone, "", false
}
