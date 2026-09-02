package tester_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
	"gemsub/internal/tester"
)

func TestRunPool_DeterministicCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// 10 candidates
	candidates := make([]parser.Candidate, 10)
	for i := range candidates {
		candidates[i] = parser.Candidate{Link: "ss://dummy"}
	}

	cfg := &config.TestConfig{
		Concurrency:  2,
		RateLimitRPS: 0,
	}

	var dispatchedCount int64
	var completedCount int64
	workerStarted := make(chan struct{}, 2)

	runner := func(workerCtx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		atomic.AddInt64(&dispatchedCount, 1)
		select {
		case workerStarted <- struct{}{}:
		default:
		}

		// Wait until worker context is cancelled
		<-workerCtx.Done()
		atomic.AddInt64(&completedCount, 1)

		return store.Result{
			Link:   cand.Link,
			Status: store.StatusInconclusive,
			Reason: "cancelled",
		}
	}

	done := make(chan bool)
	go func() {
		ok := tester.RunPoolWithRunner(ctx, candidates, cfg, runner, func(r store.Result) {})
		done <- ok
	}()

	// Wait until at least 2 workers have started and are actively in-flight
	<-workerStarted
	<-workerStarted

	// Now cancel the pool context while workers are in flight
	cancel()

	select {
	case ok := <-done:
		if ok {
			t.Errorf("expected RunPool to return false on cancellation, got true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPool hung after cancellation")
	}

	// Verify that only the concurrency limit (2) was ever dispatched, not all 10
	dispatched := atomic.LoadInt64(&dispatchedCount)
	if dispatched > 2 {
		t.Errorf("expected at most 2 candidates to be dispatched before cancel stopped scheduling, got %d", dispatched)
	}

	// Verify that all dispatched workers completed
	completed := atomic.LoadInt64(&completedCount)
	if completed != dispatched {
		t.Errorf("expected all %d dispatched workers to finish, but only %d completed", dispatched, completed)
	}
}
