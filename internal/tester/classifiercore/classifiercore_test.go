package classifiercore_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/classifiercore"
)

func TestConstants(t *testing.T) {
	if classifiercore.MaxInspectBytes != 1<<20 {
		t.Errorf("expected MaxInspectBytes to be 1MB (1<<20), got %d", classifiercore.MaxInspectBytes)
	}
}

func TestClassifyCommonHTTPStatus_ExplicitCodes(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		header        http.Header
		wantStatus    store.Status
		wantCat       store.ErrorCategory
		wantCode      int
		wantReason    string
		wantRetry     bool
		wantRetryDur  *time.Duration
		wantMatched   bool
	}{
		{
			name:         "403 Forbidden",
			statusCode:   http.StatusForbidden,
			wantStatus:   store.StatusFailed,
			wantCat:      store.ErrTargetDenied,
			wantCode:     http.StatusForbidden,
			wantReason:   "target HTTP 403 Forbidden",
			wantRetry:    false,
			wantMatched:  true,
		},
		{
			name:         "429 Too Many Requests without header",
			statusCode:   http.StatusTooManyRequests,
			wantStatus:   store.StatusInconclusive,
			wantCat:      store.ErrTargetRateLimited,
			wantCode:     http.StatusTooManyRequests,
			wantReason:   "target HTTP 429 Too Many Requests",
			wantRetry:    true,
			wantRetryDur: nil,
			wantMatched:  true,
		},
		{
			name:         "429 Too Many Requests with integer delay",
			statusCode:   http.StatusTooManyRequests,
			header:       http.Header{"Retry-After": []string{"120"}},
			wantStatus:   store.StatusInconclusive,
			wantCat:      store.ErrTargetRateLimited,
			wantCode:     http.StatusTooManyRequests,
			wantReason:   "target HTTP 429 Too Many Requests",
			wantRetry:    true,
			wantRetryDur: func() *time.Duration { d := 120 * time.Second; return &d }(),
			wantMatched:  true,
		},
		{
			name:         "503 Service Unavailable",
			statusCode:   http.StatusServiceUnavailable,
			wantStatus:   store.StatusInconclusive,
			wantCat:      store.ErrTargetError,
			wantCode:     http.StatusServiceUnavailable,
			wantReason:   "target HTTP 503 Service Unavailable",
			wantRetry:    true,
			wantMatched:  true,
		},
		{
			name:         "500 Internal Server Error",
			statusCode:   http.StatusInternalServerError,
			wantStatus:   store.StatusInconclusive,
			wantCat:      store.ErrTargetError,
			wantCode:     http.StatusInternalServerError,
			wantReason:   "target HTTP 500",
			wantRetry:    false,
			wantMatched:  true,
		},
		{
			name:         "502 Bad Gateway",
			statusCode:   http.StatusBadGateway,
			wantStatus:   store.StatusInconclusive,
			wantCat:      store.ErrTargetError,
			wantCode:     http.StatusBadGateway,
			wantReason:   "target HTTP 502",
			wantRetry:    false,
			wantMatched:  true,
		},
		{
			name:         "504 Gateway Timeout",
			statusCode:   http.StatusGatewayTimeout,
			wantStatus:   store.StatusInconclusive,
			wantCat:      store.ErrTargetError,
			wantCode:     http.StatusGatewayTimeout,
			wantReason:   "target HTTP 504",
			wantRetry:    false,
			wantMatched:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, matched := classifiercore.ClassifyCommonHTTPStatus(tt.statusCode, tt.header)
			if matched != tt.wantMatched {
				t.Fatalf("matched: got %v, want %v", matched, tt.wantMatched)
			}
			if res.Status != tt.wantStatus {
				t.Errorf("status: got %s, want %s", res.Status, tt.wantStatus)
			}
			if res.Category != tt.wantCat {
				t.Errorf("category: got %s, want %s", res.Category, tt.wantCat)
			}
			if res.StatusCode != tt.wantCode {
				t.Errorf("statusCode: got %d, want %d", res.StatusCode, tt.wantCode)
			}
			if res.Reason != tt.wantReason {
				t.Errorf("reason: got %q, want %q", res.Reason, tt.wantReason)
			}
			if res.Retryable != tt.wantRetry {
				t.Errorf("retryable: got %v, want %v", res.Retryable, tt.wantRetry)
			}
			if (res.RetryAfter == nil) != (tt.wantRetryDur == nil) {
				t.Errorf("retryAfter nil mismatch: got %v, want %v", res.RetryAfter, tt.wantRetryDur)
			} else if res.RetryAfter != nil && *res.RetryAfter != *tt.wantRetryDur {
				t.Errorf("retryAfter value: got %v, want %v", *res.RetryAfter, *tt.wantRetryDur)
			}
		})
	}
}

func TestClassifyCommonHTTPStatus_UnmatchedFallthrough(t *testing.T) {
	unmatchedCodes := []int{
		http.StatusOK,                  // 200
		http.StatusCreated,             // 201
		http.StatusBadRequest,          // 400
		http.StatusNotFound,            // 404
		http.StatusTeapot,              // 418
		http.StatusNotImplemented,             // 501
		http.StatusHTTPVersionNotSupported,    // 505
		599,                                   // 599
	}

	for _, code := range unmatchedCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			_, matched := classifiercore.ClassifyCommonHTTPStatus(code, nil)
			if matched {
				t.Errorf("code %d: expected matched == false, but got true", code)
			}
		})
	}
}

func TestClassifyCommonHTTPStatus_RetryAfterFormats(t *testing.T) {
	// Delta-seconds
	headerSeconds := http.Header{"Retry-After": []string{"30"}}
	res, matched := classifiercore.ClassifyCommonHTTPStatus(http.StatusTooManyRequests, headerSeconds)
	if !matched || res.RetryAfter == nil || *res.RetryAfter != 30*time.Second {
		t.Errorf("expected 30s RetryAfter, got %v", res.RetryAfter)
	}

	// Invalid value
	headerInvalid := http.Header{"Retry-After": []string{"not-a-number"}}
	resInv, matchedInv := classifiercore.ClassifyCommonHTTPStatus(http.StatusTooManyRequests, headerInvalid)
	if !matchedInv || resInv.RetryAfter != nil {
		t.Errorf("expected nil RetryAfter for invalid value, got %v", resInv.RetryAfter)
	}

	// Absent header
	resAbsent, matchedAbsent := classifiercore.ClassifyCommonHTTPStatus(http.StatusTooManyRequests, http.Header{})
	if !matchedAbsent || resAbsent.RetryAfter != nil {
		t.Errorf("expected nil RetryAfter for absent header, got %v", resAbsent.RetryAfter)
	}

	// HTTP-date (future)
	futureDate := time.Now().Add(60 * time.Second).UTC().Format(http.TimeFormat)
	headerDate := http.Header{"Retry-After": []string{futureDate}}
	resDate, matchedDate := classifiercore.ClassifyCommonHTTPStatus(http.StatusTooManyRequests, headerDate)
	if !matchedDate || resDate.RetryAfter == nil || *resDate.RetryAfter <= 0 || *resDate.RetryAfter > 65*time.Second {
		t.Errorf("expected ~60s RetryAfter for HTTP-date, got %v", resDate.RetryAfter)
	}
}

func TestExtractTitle(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantTitle string
		wantOK    bool
	}{
		{
			name:      "standard lowercase",
			body:      "<html><head><title>Example</title></head></html>",
			wantTitle: "Example",
			wantOK:    true,
		},
		{
			name:      "uppercase tag",
			body:      "<html><head><TITLE>Example</TITLE></head></html>",
			wantTitle: "Example",
			wantOK:    true,
		},
		{
			name:      "title with attributes",
			body:      `<html><head><title class="x" lang="en">Example</title></head></html>`,
			wantTitle: "Example",
			wantOK:    true,
		},
		{
			name:      "multiline title content",
			body:      "<html><head><title>\n  Example\n</title></head></html>",
			wantTitle: "\n  Example\n",
			wantOK:    true,
		},
		{
			name:      "no title present",
			body:      "<html><head></head><body>No title here</body></html>",
			wantTitle: "",
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTitle, ok := classifiercore.ExtractTitle([]byte(tt.body))
			if ok != tt.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tt.wantOK)
			}
			if gotTitle != tt.wantTitle {
				t.Errorf("title: got %q, want %q", gotTitle, tt.wantTitle)
			}
		})
	}
}

func TestDetectInterception(t *testing.T) {
	phrases := []string{
		"cf-chl-bypass",
		"attention required! | cloudflare",
		"just a moment...",
		"routeros",
		"mikrotik",
	}

	for _, phrase := range phrases {
		t.Run(phrase, func(t *testing.T) {
			body := "some preceding text " + phrase + " trailing text"
			cat, reason, ok := classifiercore.DetectInterception(strings.ToLower(body))
			if !ok {
				t.Fatalf("phrase %q: expected ok == true", phrase)
			}
			if cat != store.ErrTargetOther {
				t.Errorf("phrase %q: expected category %s, got %s", phrase, store.ErrTargetOther, cat)
			}
			if reason != classifiercore.InterceptionReason {
				t.Errorf("phrase %q: expected reason %q, got %q", phrase, classifiercore.InterceptionReason, reason)
			}
		})
	}

	t.Run("no interception phrase", func(t *testing.T) {
		cat, reason, ok := classifiercore.DetectInterception("clean html body with no block markers")
		if ok {
			t.Fatalf("expected ok == false, got true with reason %q", reason)
		}
		if cat != store.ErrNone {
			t.Errorf("expected ErrNone, got %s", cat)
		}
		if reason != "" {
			t.Errorf("expected empty reason, got %q", reason)
		}
	})
}

func TestClampLatency(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected time.Duration
	}{
		{100 * time.Millisecond, 100 * time.Millisecond},
		{1 * time.Nanosecond, 1 * time.Nanosecond},
		{0, 1 * time.Nanosecond},
		{-1 * time.Nanosecond, 1 * time.Nanosecond},
		{-1 * time.Millisecond, 1 * time.Nanosecond},
	}

	for _, tc := range tests {
		t.Run(tc.input.String(), func(t *testing.T) {
			got := classifiercore.ClampLatency(tc.input)
			if got != tc.expected {
				t.Errorf("ClampLatency(%v) = %v; want %v", tc.input, got, tc.expected)
			}
		})
	}
}
