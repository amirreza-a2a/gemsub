package tester_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/time/rate"

	"gemsub/internal/store"
	"gemsub/internal/tester"
)

// --- NormalizeText / NormalizeBytes backward-compatibility tests ---
// These verify that the thin wrappers in tester still delegate correctly
// to textutil. The authoritative tests live in textutil/normalize_test.go.

func TestNormalizeText(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			input:    "Gemini isn\u2019t currently supported in your country.",
			expected: "gemini isn't currently supported in your country.",
		},
		{
			input:    "Gemini isn&rsquo;t currently supported in your country.",
			expected: "gemini isn't currently supported in your country.",
		},
		{
			input:    "Gemini&#8217;s   new   \t\n  feature",
			expected: "gemini's new feature",
		},
		{
			input:    "\u201cNot available in your region\u201d",
			expected: "\"not available in your region\"",
		},
		{
			input:    "   Leading and trailing whitespace   \n\t  ",
			expected: "leading and trailing whitespace",
		},
		{
			input:    "Hello &amp; welcome &lt;to&gt; &quot;Gemini&#39;s&quot; world&#x2019;s best &nbsp; tool",
			expected: "hello & welcome <to> \"gemini's\" world's best tool",
		},
		{
			input:    "<style>.css { color: red; }</style>Content <!-- comment --> remains",
			expected: "content remains",
		},
	}

	for _, c := range cases {
		got := tester.NormalizeText(c.input)
		if got != c.expected {
			t.Errorf("NormalizeText(%q) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestNormalizeBytes(t *testing.T) {
	input := []byte("<style>body{}</style>Gemini isn&rsquo;t supported &amp; available <!-- comment --> in Iran.")
	expected := "gemini isn't supported & available in iran."
	got := tester.NormalizeBytes(input)
	if got != expected {
		t.Errorf("NormalizeBytes() = %q; want %q", got, expected)
	}
}

// --- ClassifyDialError tests ---

func TestClassifyDialError(t *testing.T) {
	res429 := tester.ClassifyDialError(errors.New("request failed: unexpected HTTP response status: 429"))
	if res429.Category != store.ErrProxyRateLimited || !res429.Retryable || res429.Status != store.StatusInconclusive {
		t.Errorf("expected retryable proxy rate limited, got %+v", res429)
	}

	res503 := tester.ClassifyDialError(errors.New("request failed: unexpected HTTP response status: 503"))
	if res503.Category != store.ErrProxyError || !res503.Retryable || res503.Status != store.StatusInconclusive {
		t.Errorf("expected retryable proxy 503 error, got %+v", res503)
	}

	res403 := tester.ClassifyDialError(errors.New("request failed: unexpected HTTP response status: 403"))
	if res403.Category != store.ErrProxyError || res403.Retryable || res403.Status != store.StatusFailed {
		t.Errorf("expected non-retryable proxy 403 error, got %+v", res403)
	}

	resRefused := tester.ClassifyDialError(errors.New("dial tcp: connection refused"))
	if resRefused.Category != store.ErrConnRefused || resRefused.Retryable || resRefused.Status != store.StatusFailed {
		t.Errorf("expected non-retryable conn refused, got %+v", resRefused)
	}

	resTimeout := tester.ClassifyDialError(errors.New("dial tcp: i/o timeout"))
	if resTimeout.Category != store.ErrTimeout || resTimeout.Retryable || resTimeout.Status != store.StatusFailed {
		t.Errorf("expected non-retryable timeout, got %+v", resTimeout)
	}

	resDeadline := tester.ClassifyDialError(errors.New("context deadline exceeded"))
	if resDeadline.Category != store.ErrTimeout || resDeadline.Retryable || resDeadline.Status != store.StatusFailed {
		t.Errorf("expected genuine deadline exceeded to fail with ErrTimeout, got %+v", resDeadline)
	}

	resReality := tester.ClassifyDialError(errors.New("reality verification failed"))
	if resReality.Category != store.ErrReality || resReality.Retryable || resReality.Status != store.StatusFailed {
		t.Errorf("expected non-retryable reality error, got %+v", resReality)
	}
}

func TestClassifyDialError_ContextCanceled(t *testing.T) {
	testCases := []struct {
		name string
		err  error
	}{
		{"standard context.Canceled", context.Canceled},
		{"wrapped url.Error context.Canceled", &url.Error{Op: "Get", URL: "https://gemini.google.com/app", Err: context.Canceled}},
		{"fmt wrapped context.Canceled", fmt.Errorf("read response body: %w", context.Canceled)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			res := tester.ClassifyDialError(tc.err)

			if res.Category == store.ErrProxyError {
				t.Errorf("context cancellation must NOT be misclassified as ErrProxyError: got %+v", res)
			}
			if res.Category != store.ErrTimeout {
				t.Errorf("expected category ErrTimeout, got %s", res.Category)
			}
			if res.Status != store.StatusInconclusive {
				t.Errorf("expected StatusInconclusive, got %s", res.Status)
			}
			if res.Reason != "context canceled" {
				t.Errorf("expected reason 'context canceled', got %q", res.Reason)
			}
			if res.Retryable {
				t.Errorf("cancelled probe should not be retryable")
			}
		})
	}
}

func TestClassifyDialError_ConnectionRefused_CrossPlatform(t *testing.T) {
	connRefusedCases := []struct {
		name string
		err  error
	}{
		{
			name: "POSIX syscall.ECONNREFUSED",
			err:  syscall.ECONNREFUSED,
		},
		{
			name: "wrapped POSIX syscall.ECONNREFUSED in net.OpError",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
			},
		},
		{
			name: "fmt.Errorf wrapped ECONNREFUSED",
			err:  fmt.Errorf("dial failed: %w", syscall.ECONNREFUSED),
		},
		{
			name: "Winsock WSAECONNREFUSED errno 10061",
			err:  syscall.Errno(10061),
		},
		{
			name: "wrapped Winsock WSAECONNREFUSED in net.OpError connectex",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connectex", Err: syscall.Errno(10061)},
			},
		},
		{
			name: "wrapped connectex in url.Error",
			err: &url.Error{
				Op:  "Get",
				URL: "http://127.0.0.1:49761/",
				Err: &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: &os.SyscallError{Syscall: "connectex", Err: syscall.Errno(10061)},
				},
			},
		},
		{
			name: "Windows English text with actively refused",
			err:  errors.New("dial tcp 127.0.0.1:49761: connectex: No connection could be made because the target machine actively refused it."),
		},
		{
			name: "Windows localized message wrapping errno 10061",
			err:  fmt.Errorf("Es konnte keine Verbindung hergestellt werden: %w", syscall.Errno(10061)),
		},
		{
			name: "standard text connection refused",
			err:  errors.New("dial tcp 127.0.0.1:80: connection refused"),
		},
	}

	for _, tc := range connRefusedCases {
		t.Run(tc.name, func(t *testing.T) {
			res := tester.ClassifyDialError(tc.err)
			if res.Category != store.ErrConnRefused {
				t.Errorf("expected ErrConnRefused, got %s (reason: %q)", res.Category, res.Reason)
			}
			if res.Status != store.StatusFailed {
				t.Errorf("expected StatusFailed, got %s", res.Status)
			}
			if res.Retryable {
				t.Errorf("expected Retryable=false")
			}
		})
	}

	negativeCases := []struct {
		name string
		err  error
	}{
		{"i/o timeout", errors.New("dial tcp: i/o timeout")},
		{"context deadline exceeded", errors.New("context deadline exceeded")},
		{"unexpected HTTP 503", errors.New("unexpected HTTP response status: 503")},
		{"tls handshake failure", errors.New("tls: handshake failure")},
		{"EOF", errors.New("unexpected EOF")},
		{"unrelated errno ENOENT", syscall.Errno(2)},
	}

	for _, tc := range negativeCases {
		t.Run("Negative_"+tc.name, func(t *testing.T) {
			res := tester.ClassifyDialError(tc.err)
			if res.Category == store.ErrConnRefused {
				t.Errorf("unrelated error %q should NOT be classified as ErrConnRefused", tc.name)
			}
		})
	}
}

// --- Rate limiter test ---

func TestRateLimiter_BurstControlled(t *testing.T) {
	const rateLimitBurst = 2
	limiter := rate.NewLimiter(rate.Limit(0.001), rateLimitBurst)

	if !limiter.Allow() {
		t.Errorf("expected 1st token to be allowed")
	}
	if !limiter.Allow() {
		t.Errorf("expected 2nd token to be allowed")
	}
	if limiter.Allow() {
		t.Errorf("expected 3rd token to be rejected by burst limit of %d", rateLimitBurst)
	}
	if limiter.Allow() {
		t.Errorf("expected 4th token to be rejected by burst limit of %d", rateLimitBurst)
	}
}

// --- Normalize backward-compatibility benchmarks ---

func BenchmarkNormalizeText_1MB(b *testing.B) {
	snippet := "<div class=\"content\">Gemini isn\u2019t supported &amp; &#8217;available&#8217; in region \u201cXYZ\u201d.\n\t  Lots   of    spaces   and\tnewlines.\n</div>\n"
	var sb strings.Builder
	for sb.Len() < 1<<20 {
		sb.WriteString(snippet)
	}
	payload := sb.String()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = tester.NormalizeText(payload)
	}
}

func BenchmarkNormalizeBytes_1MB(b *testing.B) {
	snippet := "<div class=\"content\">Gemini isn\u2019t supported &amp; &#8217;available&#8217; in region \u201cXYZ\u201d.\n\t  Lots   of    spaces   and\tnewlines.\n</div>\n"
	var sb strings.Builder
	for sb.Len() < 1<<20 {
		sb.WriteString(snippet)
	}
	payload := []byte(sb.String())

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = tester.NormalizeBytes(payload)
	}
}
