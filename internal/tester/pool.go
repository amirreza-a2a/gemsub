package tester

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"golang.org/x/time/rate"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/parser"
	"gemsub/internal/store"
)

// ProbeRunner is a function that executes a probe on a candidate.
type ProbeRunner func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result

// RunPool probes every candidate with at most cfg.Concurrency probes in flight at once,
// throttled by a shared probe-attempt rate limiter when configured.
// It returns true if all candidates were dispatched and finished, or false if the context was cancelled.
func RunPool(ctx context.Context, candidates []parser.Candidate, cfg *config.TestConfig, bus *events.EventBus, onResult func(store.Result)) bool {
	return RunPoolWithRunner(ctx, candidates, cfg, Probe, bus, onResult)
}

// RunPoolWithRunner runs the pool using a custom probe runner function.
func RunPoolWithRunner(ctx context.Context, candidates []parser.Candidate, cfg *config.TestConfig, runner ProbeRunner, bus *events.EventBus, onResult func(store.Result)) bool {
	slog.Debug("tester: starting probe pool", "candidates", len(candidates), "concurrency", cfg.Concurrency)
	var limiter *rate.Limiter
	if cfg.RateLimitRPS > 0 {
		// Conservative burst of 2 to smooth traffic and prevent CDN/proxy hammering.
		// The average rate is cfg.RateLimitRPS tokens/sec; burst controls the max
		// tokens that can be consumed without waiting.
		const rateLimitBurst = 2
		limiter = rate.NewLimiter(rate.Limit(cfg.RateLimitRPS), rateLimitBurst)
	}

	total := len(candidates)
	var completed, passed, failed, inconclusive int64

	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait() // Always wait for in-flight workers to complete before returning

	for _, cand := range candidates {
		select {
		case <-ctx.Done():
			return false
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(c parser.Candidate) {
			defer wg.Done()
			defer func() { <-sem }()

			result := runner(ctx, c, cfg, limiter)
			if ctx.Err() != nil {
				return
			}
			if onResult != nil {
				onResult(result)
			}

			switch result.Status {
			case store.StatusPassed:
				atomic.AddInt64(&passed, 1)
			case store.StatusFailed:
				atomic.AddInt64(&failed, 1)
			case store.StatusInconclusive:
				atomic.AddInt64(&inconclusive, 1)
			}
			comp := atomic.AddInt64(&completed, 1)

			if bus != nil {
				bus.Publish(events.ProbeCompleted{
					ProgressMetrics: events.ProgressMetrics{
						Completed:    int(comp),
						Total:        total,
						Passed:       int(atomic.LoadInt64(&passed)),
						Failed:       int(atomic.LoadInt64(&failed)),
						Inconclusive: int(atomic.LoadInt64(&inconclusive)),
					},
					Result: result,
				})
			}
		}(cand)
	}

	return true
}
