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
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"time"

	"golang.org/x/time/rate"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
	"gemsub/internal/tester/gemini"
	"gemsub/internal/tester/transport"
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
	slog.Debug("tester: probing candidate", "link", cand.Link, "tag", cand.Outbound.Tag)
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
				slog.Debug("tester: probe completed", "link", result.Link, "status", result.Status, "attempts", result.Attempts, "latency", result.Latency)
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
		slog.Debug("tester: retrying probe", "link", cand.Link, "attempt", attempt+1, "backoff", backoff, "category", lastClassResult.Category)
		select {
		case <-ctx.Done():
			result.Status = store.StatusInconclusive
			result.Category = store.ErrTimeout
			result.Reason = "probe context cancelled during retry backoff"
			result.Latency = time.Since(start)
			slog.Debug("tester: probe completed", "link", result.Link, "status", result.Status, "attempts", result.Attempts, "latency", result.Latency)
			return result
		case <-time.After(backoff):
			// Proceed to next attempt
		}
	}

	result.Status = lastClassResult.Status
	result.Category = lastClassResult.Category
	result.StatusCode = lastClassResult.StatusCode
	result.Reason = lastClassResult.Reason
	result.TransportOK = lastClassResult.TransportOK
	result.TransportLatency = lastClassResult.TransportLatency
	result.TransportEvidenceKnown = lastClassResult.TransportEvidenceKnown

	// If retries were exhausted on a retryable error, mark as Inconclusive
	if lastClassResult.Retryable && result.Attempts > maxRetries {
		result.Status = store.StatusInconclusive
		result.Reason = fmt.Sprintf("retries exhausted: %s", lastClassResult.Category)
	}

	result.Latency = time.Since(start)
	slog.Debug("tester: probe completed", "link", result.Link, "status", result.Status, "attempts", result.Attempts, "latency", result.Latency)
	return result
}

func executeAttempt(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (ClassificationResult, bool, *time.Duration) {
	attemptCtx, cancelAttempt := context.WithTimeout(ctx, cfg.Timeout)
	defer cancelAttempt()

	dialFn, closeBox, err := buildDialer(attemptCtx, cand, cfg.DialTimeout)
	if err != nil {
		classErr := ClassifyDialError(fmt.Errorf("build outbound: %w", err))
		return classErr, classErr.Retryable, nil
	}
	defer closeBox()

	// --- Stage 1: Transport Health ---
	healthTimeout := cfg.HealthTimeout
	if healthTimeout <= 0 {
		healthTimeout = 4 * time.Second
	}
	healthCtx, cancelHealth := context.WithTimeout(attemptCtx, healthTimeout)
	tr := transport.Probe(healthCtx, dialFn, transport.Config{
		HealthURL:     cfg.HealthURL,
		HealthTimeout: healthTimeout,
	})
	cancelHealth()

	// If the attempt context (or outer ctx) was cancelled or timed out during Stage 1
	if attemptCtx.Err() != nil {
		reason := "timeout"
		if errors.Is(attemptCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			reason = "context canceled"
		}
		return ClassificationResult{
			Status:                 store.StatusInconclusive,
			Category:               store.ErrTimeout,
			Reason:                 reason,
			Retryable:              false,
			TransportEvidenceKnown: true,
			TransportOK:            false,
			TransportLatency:       tr.Latency,
		}, false, nil
	}

	if !tr.OK {
		var classResult ClassificationResult
		if tr.StatusCode == 429 {
			classResult = ClassifyDialError(fmt.Errorf("unexpected HTTP response status: 429"))
		} else if tr.StatusCode == 503 {
			classResult = ClassifyDialError(fmt.Errorf("unexpected HTTP response status: 503"))
		} else if tr.StatusCode == 403 {
			classResult = ClassificationResult{
				Status:     store.StatusFailed,
				Category:   store.ErrProxyError,
				StatusCode: 403,
				Reason:     "transport health check received HTTP 403 (forbidden)",
				Retryable:  false,
			}
		} else if tr.Error != nil {
			classResult = ClassifyDialError(tr.Error)
		} else {
			classResult = ClassificationResult{
				Status:   store.StatusFailed,
				Category: tr.Category,
				Reason:   "transport health check failed",
			}
		}

		if tr.Category == store.ErrTimeout {
			classResult.Status = store.StatusFailed
			classResult.Category = store.ErrTimeout
			classResult.Reason = "timeout"
			classResult.Retryable = false
		}

		if tr.StatusCode != 0 {
			classResult.StatusCode = tr.StatusCode
		}
		classResult.TransportEvidenceKnown = true
		classResult.TransportOK = false
		classResult.TransportLatency = tr.Latency

		return classResult, classResult.Retryable, tr.RetryAfter
	}

	// --- Stage 2: Gemini Application ---
	geminiResult := gemini.Probe(attemptCtx, dialFn, gemini.Config{
		URL:          cfg.TargetURL,
		BlockPhrases: cfg.BlockPhrases,
		Timeout:      cfg.Timeout,
		DialTimeout:  cfg.DialTimeout,
	})

	var classResult ClassificationResult
	if geminiResult.Err != nil {
		classResult = ClassifyDialError(geminiResult.Err)
		if ctx.Err() != nil || errors.Is(geminiResult.Err, context.DeadlineExceeded) || errors.Is(geminiResult.Err, context.Canceled) || classResult.Category == store.ErrTimeout {
			classResult.Status = store.StatusInconclusive
			classResult.Category = store.ErrTimeout
			if ctx.Err() == context.Canceled || errors.Is(geminiResult.Err, context.Canceled) {
				classResult.Reason = "context canceled"
			} else {
				classResult.Reason = "timeout"
			}
		}
	} else {
		classResult = ClassificationResult{
			Status:     geminiResult.Status,
			Category:   geminiResult.Category,
			StatusCode: geminiResult.StatusCode,
			Reason:     geminiResult.Reason,
			Retryable:  geminiResult.Retryable,
		}
	}

	// Stage 1 proved transport health. Preserve explicit transport evidence.
	classResult.TransportEvidenceKnown = true
	classResult.TransportOK = true
	classResult.TransportLatency = tr.Latency

	return classResult, classResult.Retryable, geminiResult.RetryAfter
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
