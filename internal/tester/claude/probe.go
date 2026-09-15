package claude

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/transport"
)

// Config defines the parameters for the Claude application compatibility probe.
type Config struct {
	URL         string        // Default: "https://claude.ai/"
	Timeout     time.Duration // Claude application timeout (bounds the entire request)
	DialTimeout time.Duration // TLS handshake timeout (typically shorter than Timeout)
}

// ProbeResult contains the target compatibility outcome and probe latency.
type ProbeResult struct {
	Status     store.Status
	Category   store.ErrorCategory
	StatusCode int
	Reason     string
	Latency    time.Duration
	Retryable  bool
	RetryAfter *time.Duration
	Err        error
}

func clampLatency(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Millisecond
	}
	return d
}

// Probe executes the Claude application check using an established transport dialer.
// It sends browser-impersonation headers, downloads up to 1MB of response body,
// and classifies the response for Claude availability via ClassifyResponse.
func Probe(ctx context.Context, dialFn transport.DialFunc, cfg Config) ProbeResult {
	claudeURL := cfg.URL
	if claudeURL == "" {
		claudeURL = "https://claude.ai/"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tlsTimeout := cfg.DialTimeout
	if tlsTimeout <= 0 {
		tlsTimeout = timeout
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         dialFn,
			TLSHandshakeTimeout: tlsTimeout,
		},
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, claudeURL, nil)
	if err != nil {
		return ProbeResult{
			Status:   store.StatusFailed,
			Category: store.ErrConfig,
			Reason:   fmt.Sprintf("build claude request: %v", err),
			Err:      fmt.Errorf("build request: %w", err),
		}
	}

	// Browser impersonation headers for realistic Claude probing.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-CH-UA", `"Chromium";v="131", "Not_A Brand";v="24"`)

	start := time.Now()
	resp, err := client.Do(req)
	latency := clampLatency(time.Since(start))

	if err != nil {
		cat := store.ErrProxyError
		if probeCtx.Err() != nil {
			cat = store.ErrTimeout
		}
		return ProbeResult{
			Status:   store.StatusFailed,
			Category: cat,
			Reason:   err.Error(),
			Latency:  latency,
			Err:      err,
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInspectBytes))
	if err != nil {
		return ProbeResult{
			Status:     store.StatusFailed,
			Category:   store.ErrProxyError,
			StatusCode: resp.StatusCode,
			Reason:     fmt.Sprintf("read claude body: %v", err),
			Latency:    latency,
			Err:        fmt.Errorf("read body: %w", err),
		}
	}

	classRes := ClassifyResponse(resp, body)
	return ProbeResult{
		Status:     classRes.Status,
		Category:   classRes.Category,
		StatusCode: classRes.StatusCode,
		Reason:     classRes.Reason,
		Latency:    latency,
		Retryable:  classRes.Retryable,
		RetryAfter: classRes.RetryAfter,
	}
}
