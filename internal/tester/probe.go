// Package tester probes a single candidate outbound in-process: no
// subprocess, no temp config file, no local listening port. Each
// probe spins up a minimal sing-box Box containing just that one
// outbound, dials the real target through it, and tears the Box
// down again.
//
// NOTE: like internal/parser, this depends on github.com/sagernet/
// sing-box and github.com/sagernet/sing, which this sandbox can't
// fetch. The Box lifecycle calls below (box.New / Start / Outbound()
// / Close()) and the OutboundManager lookup method are based on the
// documented API at https://pkg.go.dev/github.com/sagernet/sing-box
// but have not been compiled against the real module — verify the
// OutboundManager method name (it may be Outbound(tag) or Get(tag)
// depending on version) after `go mod tidy` on your machine.
package tester

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
)

// AttemptFunc executes a single probe attempt against a candidate.
type AttemptFunc func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (ClassificationResult, bool, *time.Duration)

// Probe runs one candidate through a fresh Box with probe-attempt rate limiting
// and intelligent retries, returning the store.Result to record.
func Probe(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
	return ProbeWithExecutor(ctx, cand, cfg, limiter, executeAttempt)
}

// ProbeWithExecutor runs the probe retry loop using a custom attempt executor.
func ProbeWithExecutor(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter, exec AttemptFunc) store.Result {
	start := time.Now()
	result := store.Result{
		Link:     cand.Link,
		TestedAt: start,
		Warnings: cand.Warnings,
	}

	var lastClassResult ClassificationResult

	maxRetries := cfg.MaxRetries

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 1. Rate limiting applied to the ENTIRE probe attempt before outbound/Box dial
		if limiter != nil {
			if err := limiter.Wait(ctx); err != nil {
				result.Status = store.StatusInconclusive
				result.Category = store.ErrTimeout
				result.Reason = "probe context cancelled while waiting for attempt rate limiter token"
				result.Attempts = attempt + 1
				result.Latency = time.Since(start)
				return result
			}
		}

		attemptResult, retryable, retryAfter := exec(ctx, cand, cfg)
		lastClassResult = attemptResult
		result.Attempts = attempt + 1

		if !retryable || attempt >= maxRetries {
			break
		}

		// Calculate backoff with jitter and honor Retry-After
		backoff := computeBackoff(cfg.RetryBackoff, attempt, retryAfter)
		select {
		case <-ctx.Done():
			result.Status = store.StatusInconclusive
			result.Category = store.ErrTimeout
			result.Reason = "probe context cancelled during retry backoff"
			result.Latency = time.Since(start)
			return result
		case <-time.After(backoff):
			// Proceed to next attempt
		}
	}

	result.Status = lastClassResult.Status
	result.Category = lastClassResult.Category
	result.StatusCode = lastClassResult.StatusCode
	result.Reason = lastClassResult.Reason

	// If retries were exhausted on a retryable error, mark as Inconclusive
	if lastClassResult.Retryable && result.Attempts > maxRetries {
		result.Status = store.StatusInconclusive
		result.Reason = fmt.Sprintf("retries exhausted: %s", lastClassResult.Category)
	}

	result.Latency = time.Since(start)
	return result
}

func executeAttempt(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (ClassificationResult, bool, *time.Duration) {
	probeCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	dialFn, closeBox, err := buildDialer(probeCtx, cand, cfg.DialTimeout)
	if err != nil {
		classErr := ClassifyDialError(fmt.Errorf("build outbound: %w", err))
		return classErr, classErr.Retryable, nil
	}
	defer closeBox()

	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			DialContext:         dialFn,
			TLSHandshakeTimeout: cfg.DialTimeout,
		},
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, cfg.TargetURL, nil)
	if err != nil {
		classErr := ClassifyDialError(fmt.Errorf("build request: %w", err))
		return classErr, classErr.Retryable, nil
	}

	// Browser headers for realistic probing
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-CH-UA", `"Chromium";v="131", "Not_A Brand";v="24"`)

	resp, err := client.Do(req)
	if err != nil {
		classErr := ClassifyDialError(err)
		return classErr, classErr.Retryable, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap
	if err != nil {
		classErr := ClassifyDialError(fmt.Errorf("read body: %w", err))
		return classErr, classErr.Retryable, nil
	}

	classResp := ClassifyResponse(resp, body, cfg.BlockPhrases)
	var retryAfter *time.Duration
	if classResp.Retryable {
		retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return classResp, classResp.Retryable, retryAfter
}

// buildDialer spins up a minimal Box containing just this one
// outbound (plus a direct outbound for anything the protocol itself
// needs internally, e.g. DNS) and returns a DialContext-shaped func
// bound to it, plus a cleanup func that tears the Box down.
func buildDialer(ctx context.Context, cand parser.Candidate, dialTimeout time.Duration) (func(context.Context, string, string) (net.Conn, error), func(), error) {
	boxCtx := include.Context(ctx)

	instance, err := box.New(box.Options{
		Context: boxCtx,
		Options: option.Options{
			Log:       &option.LogOptions{Disabled: true},
			Route:     &option.RouteOptions{AutoDetectInterface: false},
			Outbounds: []option.Outbound{cand.Outbound, {Type: "direct", Tag: "direct"}},
		},
	})
	if err != nil {
		return nil, func() {}, fmt.Errorf("create box: %w", err)
	}

	if err := instance.Start(); err != nil {
		instance.Close()
		return nil, func() {}, fmt.Errorf("start box: %w", err)
	}

	closeFn := func() { instance.Close() }

	ob, loaded := instance.Outbound().Outbound(cand.Outbound.Tag)
	if !loaded {
		closeFn()
		return nil, func() {}, fmt.Errorf("outbound %q not registered", cand.Outbound.Tag)
	}

	dialFn := func(dialCtx context.Context, network, addr string) (net.Conn, error) {
		timeout := dialTimeout
		if timeout <= 0 {
			timeout = 4 * time.Second
		}
		connCtx, cancel := context.WithTimeout(dialCtx, timeout)
		defer cancel()

		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		dest := M.ParseSocksaddrHostPort(host, parsePort(port))
		return ob.DialContext(connCtx, network, dest)
	}

	return dialFn, closeFn, nil
}

func parsePort(s string) uint16 {
	var p uint16
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		p = p*10 + uint16(c-'0')
	}
	return p
}

// maxBackoffCap is the maximum delay between retry attempts.
const maxBackoffCap = 10 * time.Second

func computeBackoff(base time.Duration, attempt int, retryAfter *time.Duration) time.Duration {
	if base <= 0 {
		base = 1 * time.Second
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 10 {
		attempt = 10 // Prevent shift overflow; 1<<10 * 1s is already > 10s maxBackoffCap
	}

	// Exponential backoff: base * 2^attempt
	multiplier := 1 << attempt
	b := time.Duration(multiplier) * base
	if b > maxBackoffCap {
		b = maxBackoffCap
	}

	// Apply ±20% jitter
	jitter := float64(b) * (0.8 + 0.4*rand.Float64())
	delay := time.Duration(jitter)

	// Honor Retry-After: effective_delay = max(exponential_backoff_with_jitter, parsed_retry_after)
	if retryAfter != nil && *retryAfter > delay {
		delay = *retryAfter
	}

	// Final cap
	if delay > maxBackoffCap {
		delay = maxBackoffCap
	}
	return delay
}

func parseRetryAfter(header string) *time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if sec, err := strconv.Atoi(header); err == nil && sec >= 0 {
		d := time.Duration(sec) * time.Second
		return &d
	}
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return &d
	}
	return nil
}
