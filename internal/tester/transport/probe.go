// Package transport provides an isolated, target-agnostic network transport health probe.
// It determines whether a proxy outbound can establish a TCP/TLS connection and complete
// an HTTP handshake, without inspecting or downloading application payloads.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/textutil"
)

// DefaultHealthURL is the canonical connectivity-check endpoint.
const DefaultHealthURL = "https://www.gstatic.com/generate_204"

// DefaultHealthTimeout is the default duration allocated to transport verification.
const DefaultHealthTimeout = 4 * time.Second

// DialFunc establishes a raw network connection through a proxy outbound.
// Matches the closure returned by sing-box Box dialer.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Config holds configuration parameters for the transport health probe.
type Config struct {
	HealthURL       string
	HealthTimeout   time.Duration
	TLSClientConfig *tls.Config       // Optional TLS configuration; if nil, default/system TLS verification is used.
	Transport       http.RoundTripper // Optional transport override (e.g. for testing); if nil, standard http.Transport with DialContext is used.
}

// Result represents the outcome of the transport health check.
type Result struct {
	OK         bool
	Latency    time.Duration
	StatusCode int
	Category   store.ErrorCategory
	Error      error
	RetryAfter *time.Duration
}

// Probe executes an isolated, target-agnostic HTTPS/HTTP transport health check
// through the supplied dialFn without reading, buffering, or inspecting response bodies.
func Probe(ctx context.Context, dialFn DialFunc, cfg Config) Result {
	healthURL := cfg.HealthURL
	if healthURL == "" {
		healthURL = DefaultHealthURL
	}
	timeout := cfg.HealthTimeout
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}

	if dialFn == nil && cfg.Transport == nil {
		return Result{
			OK:       false,
			Category: store.ErrConfig,
			Error:    errors.New("transport probe: dialFn must not be nil"),
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var clientTransport http.RoundTripper
	if cfg.Transport != nil {
		clientTransport = cfg.Transport
	} else {
		// Health probes must be completely isolated, independent network checks.
		// We explicitly disable keep-alives (DisableKeepAlives: true) and close idle connections
		// (tr.CloseIdleConnections()) so that probes never hold idle sockets open or leak connections
		// across proxy outbounds.
		tr := &http.Transport{
			DialContext:         dialFn,
			TLSClientConfig:     cfg.TLSClientConfig,
			TLSHandshakeTimeout: timeout,
			DisableKeepAlives:   true,
		}
		defer tr.CloseIdleConnections()
		clientTransport = tr
	}

	client := &http.Client{
		Transport: clientTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		return Result{
			OK:       false,
			Category: store.ErrConfig,
			Error:    fmt.Errorf("build health request: %w", err),
		}
	}

	// Minimal neutral request headers — no browser impersonation, no target-specific headers.
	req.Header.Set("User-Agent", "gemsub-health/1.0")
	req.Header.Set("Accept", "*/*")

	start := time.Now()
	resp, err := client.Do(req)
	latency := clampLatency(time.Since(start))

	if err != nil {
		cat := classifyDialError(err, probeCtx)
		return Result{
			OK:       false,
			Latency:  latency,
			Category: cat,
			Error:    err,
		}
	}
	defer resp.Body.Close()

	statusCode := resp.StatusCode

	// Redirects (3xx) indicate captive portal, proxy interception, or unexpected routing.
	if statusCode >= 300 && statusCode < 400 {
		return Result{
			OK:         false,
			Latency:    latency,
			StatusCode: statusCode,
			Category:   store.ErrProxyError,
			Error:      fmt.Errorf("transport probe received unexpected redirect status %d", statusCode),
		}
	}

	// HTTP 429 indicates rate limiting.
	if statusCode == http.StatusTooManyRequests {
		var retryAfter *time.Duration
		if resp.Header != nil {
			retryAfter = textutil.ParseRetryAfter(resp.Header.Get("Retry-After"))
		}
		return Result{
			OK:         false,
			Latency:    latency,
			StatusCode: statusCode,
			Category:   store.ErrProxyRateLimited,
			Error:      fmt.Errorf("transport probe received HTTP 429 (rate limited)"),
			RetryAfter: retryAfter,
		}
	}

	// Check expected success status.
	if isExpectedSuccess(statusCode, healthURL) {
		return Result{
			OK:         true,
			Latency:    latency,
			StatusCode: statusCode,
			Category:   store.ErrNone,
		}
	}

	// Any non-2xx status (or non-204 for generate_204 endpoints) is a transport failure.
	var retryAfter *time.Duration
	if resp.Header != nil && statusCode == http.StatusServiceUnavailable {
		retryAfter = textutil.ParseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return Result{
		OK:         false,
		Latency:    latency,
		StatusCode: statusCode,
		Category:   store.ErrProxyError,
		Error:      fmt.Errorf("transport probe received unexpected HTTP status %d", statusCode),
		RetryAfter: retryAfter,
	}
}

// isExpectedSuccess returns true if the status code represents verified transport success.
// For generate_204 endpoints, only 204 No Content is accepted as success.
// For custom endpoints (e.g. /healthz), standard 2xx statuses are accepted.
func isExpectedSuccess(statusCode int, healthURL string) bool {
	if isGenerate204URL(healthURL) {
		return statusCode == http.StatusNoContent
	}
	return statusCode >= 200 && statusCode < 300
}

func isGenerate204URL(rawURL string) bool {
	if rawURL == "" || rawURL == DefaultHealthURL {
		return true
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	cleanPath := path.Clean(parsed.Path)
	return cleanPath == "/generate_204" || cleanPath == "/gen_204"
}

// classifyDialError maps dial, TLS, and network transport errors to canonical store.ErrorCategory values.
//
// NOTE (Architecture / Remediation Note):
// This function intentionally duplicates dial error classification logic from internal/tester/probe.go
// as a temporary transitional measure. In Ticket 18, internal/tester/transport must remain an isolated,
// leaf transport package with zero dependencies on internal/tester to avoid circular imports.
// Ticket 19/21 will unify error classification across transport and application probing.
func classifyDialError(err error, ctx context.Context) store.ErrorCategory {
	if err == nil {
		return store.ErrNone
	}

	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return store.ErrTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return store.ErrTimeout
	}

	errStr := err.Error()
	lower := strings.ToLower(errStr)

	switch {
	case strings.Contains(errStr, "unexpected HTTP response status: 429") || strings.Contains(errStr, "status: 429"):
		return store.ErrProxyRateLimited
	case IsConnectionRefused(err):
		return store.ErrConnRefused
	case strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "client.timeout exceeded"):
		return store.ErrTimeout
	case strings.Contains(lower, "reality verification failed"):
		return store.ErrReality
	case strings.Contains(lower, "tls: handshake failure") || strings.Contains(lower, "remote error: tls") || strings.Contains(lower, "x509:"):
		return store.ErrTLS
	case strings.Contains(lower, "eof") || strings.Contains(lower, "reset by peer") || strings.Contains(lower, "broken pipe"):
		return store.ErrReset
	case strings.Contains(lower, "utls") || strings.Contains(lower, "fingerprint"):
		return store.ErrConfig
	default:
		return store.ErrProxyError
	}
}

// IsConnectionRefused reports whether err represents a TCP connection refused error,
// inspecting standard library error hierarchies (POSIX ECONNREFUSED and Windows
// Winsock WSAECONNREFUSED / 10061) as well as common synthetic error messages.
func IsConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == syscall.ECONNREFUSED || uintptr(errno) == 10061 {
			return true
		}
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "actively refused")
}

// clampLatency ensures a measured latency duration is strictly positive.
// On operating systems with coarse timer tick resolution (such as Windows with ~15.6ms timer interrupts),
// a sub-millisecond network request may measure as 0s. Since downstream store and history consumers treat
// TransportLatency == 0 as "unavailable/unrecorded", any completed probe must be recorded with a positive
// duration. If measured latency is <= 0, it falls back to time.Nanosecond (completed, but below measurable resolution).
func clampLatency(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Nanosecond
	}
	return d
}
