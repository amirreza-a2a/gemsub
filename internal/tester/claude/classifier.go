// Package claude implements response classification for Anthropic's Claude web application.
// It detects regional availability blocks, Cloudflare bot challenges, rate limiting, and HTTP errors.
//
// This package is pure and side-effect-free with no dependencies on sing-box or networking transports.
package claude

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/textutil"
)

// maxInspectBytes bounds payload inspection to 1MB.
const maxInspectBytes = 1 << 20

// titleRegex matches the document title case-insensitively.
var titleRegex = regexp.MustCompile(`(?i)<title[^>]*>([\s\S]*?)</title>`)

// Result represents the outcome of evaluating an HTTP response for Claude availability.
type Result struct {
	Status     store.Status
	Category   store.ErrorCategory
	StatusCode int
	Reason     string
	Retryable  bool
	RetryAfter *time.Duration
}

// ClassifyResponse evaluates an HTTP response and its body for Claude availability.
func ClassifyResponse(resp *http.Response, body []byte) Result {
	// 1. Nil response guard
	if resp == nil {
		return Result{
			Status:   store.StatusFailed,
			Category: store.ErrTargetOther,
			Reason:   "nil response",
		}
	}

	statusCode := resp.StatusCode

	// 2. HTTP 429 Too Many Requests
	if statusCode == http.StatusTooManyRequests {
		res := Result{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetRateLimited,
			StatusCode: http.StatusTooManyRequests,
			Reason:     "target HTTP 429 Too Many Requests",
			Retryable:  true,
		}
		if resp.Header != nil {
			res.RetryAfter = textutil.ParseRetryAfter(resp.Header.Get("Retry-After"))
		}
		return res
	}

	// 3. HTTP 5xx Server Error
	if statusCode >= 500 && statusCode <= 599 {
		return Result{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetError,
			StatusCode: statusCode,
			Reason:     fmt.Sprintf("target HTTP %d", statusCode),
		}
	}

	// 4. HTTP 403 Forbidden
	if statusCode == http.StatusForbidden {
		return Result{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetDenied,
			StatusCode: http.StatusForbidden,
			Reason:     "target HTTP 403 Forbidden",
		}
	}

	// Bound inspection to at most maxInspectBytes (1MB)
	if len(body) > maxInspectBytes {
		body = body[:maxInspectBytes]
	}

	normalized := textutil.NormalizeBytes(body)

	// 5. Cloudflare / Interception challenge preemption
	if strings.Contains(normalized, "cf-chl-bypass") ||
		strings.Contains(normalized, "attention required! | cloudflare") ||
		strings.Contains(normalized, "just a moment...") {
		return Result{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetOther,
			StatusCode: statusCode,
			Reason:     "interception: Cloudflare challenge or captive portal page",
		}
	}

	// 6. Primary Claude region-block signal
	if resp.Request != nil && resp.Request.URL != nil {
		path := strings.ToLower(resp.Request.URL.Path)
		if strings.Contains(path, "app-unavailable-in-region") {
			return Result{
				Status:     store.StatusFailed,
				Category:   store.ErrRegionBlocked,
				StatusCode: statusCode,
				Reason:     "claude region restriction: redirected to app-unavailable-in-region",
			}
		}
	}

	// 7. Secondary region-block signals (HTML fallbacks)
	if hasSecondaryRegionBlock(body, normalized) {
		return Result{
			Status:     store.StatusFailed,
			Category:   store.ErrRegionBlocked,
			StatusCode: statusCode,
			Reason:     "claude region restriction: marker detected in payload",
		}
	}

	// 8. HTTP 200 default
	if statusCode == http.StatusOK {
		return Result{
			Status:     store.StatusPassed,
			Category:   store.ErrNone,
			StatusCode: http.StatusOK,
			Reason:     "ok",
		}
	}

	// 9. Unexpected statuses
	return Result{
		Status:     store.StatusFailed,
		Category:   store.ErrTargetOther,
		StatusCode: statusCode,
		Reason:     fmt.Sprintf("unexpected HTTP status %d", statusCode),
	}
}

func hasSecondaryRegionBlock(rawBody []byte, normalized string) bool {
	// Fallback 1: Canonical link containing app-unavailable-in-region
	if hasCanonicalLinkRegionBlock(rawBody) {
		return true
	}

	// Fallback 2: <title> containing app unavailable in region
	if matches := titleRegex.FindSubmatch(rawBody); len(matches) > 1 {
		titleText := strings.ToLower(string(matches[1]))
		if strings.Contains(titleText, "app unavailable in region") {
			return true
		}
	}

	// Fallback 3: Reference/link to anthropic.com/supported-countries combined with regional unavailability indicator
	if strings.Contains(normalized, "anthropic.com/supported-countries") {
		if strings.Contains(normalized, "unavailable in region") ||
			strings.Contains(normalized, "only available in certain regions") ||
			strings.Contains(normalized, "isn't available here") ||
			strings.Contains(normalized, "not available in your region") ||
			strings.Contains(normalized, "app unavailable") {
			return true
		}
	}

	return false
}

func hasCanonicalLinkRegionBlock(body []byte) bool {
	lower := bytes.ToLower(body)
	start := 0
	for {
		idx := bytes.Index(lower[start:], []byte("<link"))
		if idx == -1 {
			break
		}
		tagStart := start + idx
		tagEnd := bytes.IndexByte(lower[tagStart:], '>')
		if tagEnd == -1 {
			break
		}
		tag := lower[tagStart : tagStart+tagEnd+1]
		if (bytes.Contains(tag, []byte(`rel="canonical"`)) || bytes.Contains(tag, []byte(`rel='canonical'`)) || bytes.Contains(tag, []byte(`rel=canonical`))) &&
			bytes.Contains(tag, []byte("app-unavailable-in-region")) {
			return true
		}
		start = tagStart + tagEnd + 1
	}
	return false
}
