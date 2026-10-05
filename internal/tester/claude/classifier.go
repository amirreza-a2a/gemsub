// Package claude implements response classification for Anthropic's Claude web application.
// It detects regional availability blocks, Cloudflare bot challenges, rate limiting, and HTTP errors.
//
// This package is pure and side-effect-free with no dependencies on sing-box or networking transports.
package claude

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/classifiercore"
	"gemsub/internal/tester/textutil"
)

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
	var header http.Header
	if resp != nil {
		header = resp.Header
	}

	// 2. Common HTTP Status Codes (429, 503, 500, 502, 504, 403)
	if common, ok := classifiercore.ClassifyCommonHTTPStatus(statusCode, header); ok {
		return Result{
			Status:     common.Status,
			Category:   common.Category,
			StatusCode: common.StatusCode,
			Reason:     common.Reason,
			Retryable:  common.Retryable,
			RetryAfter: common.RetryAfter,
		}
	}

	// Bound inspection to at most MaxInspectBytes (1MB)
	if len(body) > classifiercore.MaxInspectBytes {
		body = body[:classifiercore.MaxInspectBytes]
	}

	normalized := textutil.NormalizeBytes(body)

	// 3. Cloudflare / Interception challenge preemption (including routeros and mikrotik)
	if cat, reason, ok := classifiercore.DetectInterception(normalized); ok {
		return Result{
			Status:     store.StatusFailed,
			Category:   cat,
			StatusCode: statusCode,
			Reason:     reason,
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
	if title, ok := classifiercore.ExtractTitle(rawBody); ok {
		if strings.Contains(strings.ToLower(title), "app unavailable in region") {
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
