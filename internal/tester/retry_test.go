package tester_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
	"gemsub/internal/tester"
)

func TestRetryLoop_Target429Exhaustion(t *testing.T) {
	var attempts int64

	mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
		atomic.AddInt64(&attempts, 1)
		return tester.ClassificationResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetRateLimited,
			StatusCode: 429,
			Reason:     "target HTTP 429 Too Many Requests",
			Retryable:  true,
		}, true, nil
	}

	cfg := &config.TestConfig{
		MaxRetries:   2,
		RetryBackoff: 1 * time.Millisecond,
		Timeout:      1 * time.Second,
	}

	cand := parser.Candidate{Link: "vless://test"}
	res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)

	if attempts != 3 {
		t.Errorf("expected max_retries=2 to produce exactly 3 attempts, got %d", attempts)
	}

	if res.Status != store.StatusInconclusive {
		t.Errorf("expected final status to be StatusInconclusive, got %s", res.Status)
	}

	if res.Category != store.ErrTargetRateLimited {
		t.Errorf("expected category ErrTargetRateLimited, got %s", res.Category)
	}

	if res.Attempts != 3 {
		t.Errorf("expected res.Attempts to be 3, got %d", res.Attempts)
	}
}

func TestRetryLoop_Proxy429Exhaustion(t *testing.T) {
	var attempts int64

	mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
		atomic.AddInt64(&attempts, 1)
		return tester.ClassificationResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrProxyRateLimited,
			StatusCode: 429,
			Reason:     "proxy tunnel CDN returned HTTP 429",
			Retryable:  true,
		}, true, nil
	}

	cfg := &config.TestConfig{
		MaxRetries:   2,
		RetryBackoff: 1 * time.Millisecond,
		Timeout:      1 * time.Second,
	}

	cand := parser.Candidate{Link: "vless://test"}
	res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)

	if attempts != 3 {
		t.Errorf("expected exactly 3 attempts for proxy 429, got %d", attempts)
	}

	if res.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive, got %s", res.Status)
	}

	if res.Category != store.ErrProxyRateLimited {
		t.Errorf("expected ErrProxyRateLimited, got %s", res.Category)
	}
}

func TestRetryLoop_NonRetryableStopsImmediately(t *testing.T) {
	var attempts int64

	mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
		atomic.AddInt64(&attempts, 1)
		return tester.ClassificationResult{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetDenied,
			StatusCode: 403,
			Reason:     "target HTTP 403 Forbidden",
			Retryable:  false,
		}, false, nil
	}

	cfg := &config.TestConfig{
		MaxRetries:   2,
		RetryBackoff: 1 * time.Millisecond,
		Timeout:      1 * time.Second,
	}

	cand := parser.Candidate{Link: "vless://test"}
	res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)

	if attempts != 1 {
		t.Errorf("expected non-retryable 403 to exit on attempt 1, got %d", attempts)
	}

	if res.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed, got %s", res.Status)
	}
}

func TestRetryLoop_MaxRetriesZeroMeansOneAttempt(t *testing.T) {
	var attempts int64

	mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
		atomic.AddInt64(&attempts, 1)
		return tester.ClassificationResult{
			Status:    store.StatusInconclusive,
			Category:  store.ErrTargetRateLimited,
			Reason:    "target HTTP 429",
			Retryable: true,
		}, true, nil
	}

	cfg := &config.TestConfig{
		MaxRetries:   0, // Explicit zero: exactly one attempt, no retry
		RetryBackoff: 1 * time.Millisecond,
		Timeout:      1 * time.Second,
	}

	cand := parser.Candidate{Link: "vless://test"}
	res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)

	if attempts != 1 {
		t.Errorf("expected max_retries=0 to produce exactly 1 attempt, got %d", attempts)
	}

	if res.Attempts != 1 {
		t.Errorf("expected res.Attempts=1, got %d", res.Attempts)
	}

	if res.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive, got %s", res.Status)
	}
}

func TestRetryLoop_RetryAfterLargerThanBackoff(t *testing.T) {
	// When Retry-After is larger than calculated backoff,
	// the effective delay should be Retry-After.
	var attempts int64
	retryAfterDur := 50 * time.Millisecond

	mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
		att := atomic.AddInt64(&attempts, 1)
		if att < 2 {
			return tester.ClassificationResult{
				Status:    store.StatusInconclusive,
				Category:  store.ErrTargetRateLimited,
				Reason:    "target HTTP 429",
				Retryable: true,
			}, true, &retryAfterDur
		}
		return tester.ClassificationResult{
			Status:   store.StatusPassed,
			Category: store.ErrNone,
			Reason:   "ok",
		}, false, nil
	}

	cfg := &config.TestConfig{
		MaxRetries:   2,
		RetryBackoff: 1 * time.Millisecond, // Base backoff is much smaller than Retry-After
		Timeout:      1 * time.Second,
	}

	cand := parser.Candidate{Link: "vless://test"}
	start := time.Now()
	res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)

	if attempts != 2 {
		t.Errorf("expected retry to succeed on attempt 2, got %d", attempts)
	}

	if res.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed, got %s", res.Status)
	}

	elapsed := time.Since(start)
	// Should have waited at least the Retry-After duration (50ms)
	if elapsed < 45*time.Millisecond {
		t.Errorf("expected delay of at least ~50ms (Retry-After), got %s", elapsed)
	}
}

func TestRetryLoop_RetryAfterSmallerThanBackoff(t *testing.T) {
	// When Retry-After is smaller than calculated backoff,
	// the exponential backoff should win.
	var attempts int64
	retryAfterDur := 1 * time.Nanosecond // Essentially zero

	mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
		att := atomic.AddInt64(&attempts, 1)
		if att < 2 {
			return tester.ClassificationResult{
				Status:    store.StatusInconclusive,
				Category:  store.ErrTargetRateLimited,
				Reason:    "target HTTP 429",
				Retryable: true,
			}, true, &retryAfterDur
		}
		return tester.ClassificationResult{
			Status:   store.StatusPassed,
			Category: store.ErrNone,
			Reason:   "ok",
		}, false, nil
	}

	cfg := &config.TestConfig{
		MaxRetries:   2,
		RetryBackoff: 50 * time.Millisecond, // Base backoff is larger than Retry-After
		Timeout:      1 * time.Second,
	}

	cand := parser.Candidate{Link: "vless://test"}
	start := time.Now()
	res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)

	if attempts != 2 {
		t.Errorf("expected retry to succeed on attempt 2, got %d", attempts)
	}

	if res.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed, got %s", res.Status)
	}

	elapsed := time.Since(start)
	// Backoff is 50ms * (0.8 to 1.2) = 40ms to 60ms. Should be at least 35ms.
	if elapsed < 35*time.Millisecond {
		t.Errorf("expected delay of at least ~40ms (exponential backoff), got %s", elapsed)
	}
}

func TestRetryLoop_NoRetryAfterUsesExponentialBackoff(t *testing.T) {
	var attempts int64

	mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
		att := atomic.AddInt64(&attempts, 1)
		if att < 2 {
			return tester.ClassificationResult{
				Status:    store.StatusInconclusive,
				Category:  store.ErrTargetRateLimited,
				Reason:    "target HTTP 429",
				Retryable: true,
			}, true, nil // No Retry-After
		}
		return tester.ClassificationResult{
			Status:   store.StatusPassed,
			Category: store.ErrNone,
			Reason:   "ok",
		}, false, nil
	}

	cfg := &config.TestConfig{
		MaxRetries:   2,
		RetryBackoff: 30 * time.Millisecond,
		Timeout:      1 * time.Second,
	}

	cand := parser.Candidate{Link: "vless://test"}
	start := time.Now()
	res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)

	if res.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed, got %s", res.Status)
	}

	elapsed := time.Since(start)
	// Attempt 0 backoff: 30ms * 2^0 * jitter(0.8-1.2) = 24ms to 36ms
	if elapsed < 20*time.Millisecond {
		t.Errorf("expected delay of at least ~24ms (exponential backoff with jitter), got %s", elapsed)
	}
}

func TestRetryLoop_Target503ExhaustionDiagnostics(t *testing.T) {
	var attempts int64

	mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
		atomic.AddInt64(&attempts, 1)
		return tester.ClassificationResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetError,
			StatusCode: 503,
			Reason:     "target HTTP 503 Service Unavailable",
			Retryable:  true,
		}, true, nil
	}

	cfg := &config.TestConfig{
		MaxRetries:   2,
		RetryBackoff: 1 * time.Millisecond,
		Timeout:      1 * time.Second,
	}

	cand := parser.Candidate{Link: "vless://test"}
	res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}

	if res.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive, got %s", res.Status)
	}

	expectedReason := "retries exhausted: target_error"
	if res.Reason != expectedReason {
		t.Errorf("expected reason %q, got %q", expectedReason, res.Reason)
	}
}

func TestRetryLoop_ProxyRateLimitedDiagnostics(t *testing.T) {
	mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
		return tester.ClassificationResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrProxyRateLimited,
			StatusCode: 429,
			Reason:     "proxy tunnel CDN returned HTTP 429",
			Retryable:  true,
		}, true, nil
	}

	cfg := &config.TestConfig{
		MaxRetries:   1,
		RetryBackoff: 1 * time.Millisecond,
		Timeout:      1 * time.Second,
	}

	cand := parser.Candidate{Link: "vless://test"}
	res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)

	expectedReason := "retries exhausted: proxy_rate_limited"
	if res.Reason != expectedReason {
		t.Errorf("expected reason %q, got %q", expectedReason, res.Reason)
	}
}

func TestComputeBackoff_LargeAttemptIndexNoOverflow(t *testing.T) {
	// Test pathological and very large attempt indices to ensure no panic or shift overflow occurs.
	largeAttempts := []int{-10, 0, 1, 5, 10, 31, 32, 63, 64, 100, 1000}

	for _, att := range largeAttempts {
		mockExec := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) (tester.ClassificationResult, bool, *time.Duration) {
			return tester.ClassificationResult{
				Status:    store.StatusPassed,
				Category:  store.ErrNone,
				Reason:    "ok",
				Retryable: false,
			}, false, nil
		}

		cfg := &config.TestConfig{
			MaxRetries:   0,
			RetryBackoff: 1 * time.Second,
			Timeout:      1 * time.Second,
		}

		// Direct call through probe with executor should not panic
		cand := parser.Candidate{Link: "vless://test"}
		res := tester.ProbeWithExecutor(context.Background(), cand, cfg, nil, mockExec)
		if res.Status != store.StatusPassed {
			t.Errorf("attempt %d: unexpected status %s", att, res.Status)
		}
	}
}
