package tester_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"gemsub/internal/config"
	"gemsub/internal/events"
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
		ok := tester.RunPoolWithRunner(ctx, candidates, cfg, runner, nil, func(r store.Result) {})
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

func TestRunPool_CancelledProbesDoNotReachOnResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	candidates := []parser.Candidate{
		{Link: "vless://cand1"},
		{Link: "vless://cand2"},
		{Link: "vless://cand3"},
	}

	cfg := &config.TestConfig{
		Concurrency: 2,
	}

	workerStarted := make(chan struct{}, 2)
	runner := func(workerCtx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		select {
		case workerStarted <- struct{}{}:
		default:
		}

		<-workerCtx.Done()
		return store.Result{
			Link:     cand.Link,
			Status:   store.StatusInconclusive,
			Category: store.ErrTimeout,
			Reason:   "context canceled",
		}
	}

	var mu sync.Mutex
	var received []store.Result
	onResult := func(r store.Result) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, r)
	}

	done := make(chan bool)
	go func() {
		ok := tester.RunPoolWithRunner(ctx, candidates, cfg, runner, nil, onResult)
		done <- ok
	}()

	// Wait for workers to begin execution
	<-workerStarted
	<-workerStarted

	// Cancel while in flight
	cancel()

	select {
	case ok := <-done:
		if ok {
			t.Errorf("expected RunPool to return false on context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPool hung on cancellation")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 0 {
		t.Errorf("expected 0 results to reach onResult after cancellation, got %d: %+v", len(received), received)
	}
}

func TestRunPool_CancellationPreservesStoreState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	const passingLink = "vless://active-node@1.1.1.1:443"
	st.PutWithTransition(store.Result{
		Link:                    passingLink,
		Status:                  store.StatusPassed,
		Reason:                  "ok",
		PreviouslyPassed:        true,
		ConsecutiveInconclusive: 0,
	})

	candidates := []parser.Candidate{{Link: passingLink}}
	cfg := &config.TestConfig{
		Concurrency: 1,
	}

	workerStarted := make(chan struct{})
	runner := func(workerCtx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		close(workerStarted)
		<-workerCtx.Done()
		return store.Result{
			Link:     cand.Link,
			Status:   store.StatusInconclusive,
			Category: store.ErrTimeout,
			Reason:   "context canceled",
		}
	}

	done := make(chan bool)
	go func() {
		ok := tester.RunPoolWithRunner(ctx, candidates, cfg, runner, nil, func(r store.Result) {
			st.PutWithTransition(r)
		})
		done <- ok
	}()

	<-workerStarted
	cancel()

	<-done

	// Verify store state was NOT mutated
	all := st.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 result in store, got %d", len(all))
	}
	r := all[0]
	if r.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed, got %s", r.Status)
	}
	if !r.PreviouslyPassed {
		t.Errorf("expected PreviouslyPassed to remain true")
	}
	if r.ConsecutiveInconclusive != 0 {
		t.Errorf("expected ConsecutiveInconclusive to remain 0, got %d", r.ConsecutiveInconclusive)
	}
	if !st.IsServable(r) {
		t.Errorf("expected candidate to remain servable")
	}
}

func TestRunPool_GenuineProbeTimeoutReachesOnResult(t *testing.T) {
	// Parent context remains active
	ctx := context.Background()
	candidates := []parser.Candidate{{Link: "vless://dead-node"}}
	cfg := &config.TestConfig{
		Concurrency: 1,
	}

	runner := func(workerCtx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		// Simulates a probe where proxy timed out (DeadlineExceeded on probe attempt, but parent ctx alive)
		return store.Result{
			Link:     cand.Link,
			Status:   store.StatusFailed,
			Category: store.ErrTimeout,
			Reason:   "timeout",
		}
	}

	var received []store.Result
	ok := tester.RunPoolWithRunner(ctx, candidates, cfg, runner, nil, func(r store.Result) {
		received = append(received, r)
	})

	if !ok {
		t.Errorf("expected RunPool to return true when parent ctx is alive")
	}
	if len(received) != 1 {
		t.Fatalf("expected genuine timeout to reach onResult, got %d results", len(received))
	}
	if received[0].Status != store.StatusFailed || received[0].Category != store.ErrTimeout {
		t.Errorf("expected StatusFailed and ErrTimeout, got %+v", received[0])
	}
}

func TestRunPool_PublishesProgressMetricsToEventBus(t *testing.T) {
	ctx := context.Background()
	candidates := []parser.Candidate{
		{Link: "vless://pass-node"},
		{Link: "vless://fail-node"},
		{Link: "vless://inconclusive-node"},
	}
	cfg := &config.TestConfig{
		Concurrency: 2,
	}

	runner := func(workerCtx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		switch cand.Link {
		case "vless://pass-node":
			return store.Result{
				Link:   cand.Link,
				Status: store.StatusPassed,
			}
		case "vless://fail-node":
			return store.Result{
				Link:     cand.Link,
				Status:   store.StatusFailed,
				Category: store.ErrRegionBlocked,
			}
		default:
			return store.Result{
				Link:   cand.Link,
				Status: store.StatusInconclusive,
			}
		}
	}

	bus := events.New()
	defer bus.Close()
	subCh := bus.Subscribe(10)
	defer bus.Unsubscribe(subCh)

	var onResultCount int32
	ok := tester.RunPoolWithRunner(ctx, candidates, cfg, runner, bus, func(r store.Result) {
		atomic.AddInt32(&onResultCount, 1)
	})

	if !ok {
		t.Fatalf("expected RunPool to succeed")
	}
	if atomic.LoadInt32(&onResultCount) != 3 {
		t.Fatalf("expected 3 onResult calls, got %d", atomic.LoadInt32(&onResultCount))
	}

	var eventsReceived []events.ProbeCompleted
	for i := 0; i < 3; i++ {
		select {
		case evt, ok := <-subCh:
			if !ok {
				t.Fatalf("event channel closed prematurely at index %d", i)
			}
			pc, ok := evt.(events.ProbeCompleted)
			if !ok {
				t.Fatalf("expected ProbeCompleted event, got %T", evt)
			}
			eventsReceived = append(eventsReceived, pc)
		case <-time.After(1 * time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}

	// Verify all 3 events have Total == 3
	for i, ev := range eventsReceived {
		if ev.Total != 3 {
			t.Errorf("event %d: expected Total=3, got %d", i, ev.Total)
		}
		if ev.Completed < 1 || ev.Completed > 3 {
			t.Errorf("event %d: invalid Completed=%d", i, ev.Completed)
		}
		if ev.Passed+ev.Failed+ev.Inconclusive != ev.Completed {
			t.Errorf("event %d: sum of (Passed=%d + Failed=%d + Inconclusive=%d) != Completed=%d",
				i, ev.Passed, ev.Failed, ev.Inconclusive, ev.Completed)
		}
	}

	// The event corresponding to all probes completed (Completed == 3) must reflect all outcomes.
	// Note: Because worker goroutines publish concurrently, events may be delivered interleaved.
	var finalEv *events.ProbeCompleted
	for i := range eventsReceived {
		if eventsReceived[i].Completed == 3 {
			finalEv = &eventsReceived[i]
			break
		}
	}

	if finalEv == nil {
		t.Fatalf("expected an event with Completed=3, received events: %+v", eventsReceived)
	}

	if finalEv.Passed != 1 || finalEv.Failed != 1 || finalEv.Inconclusive != 1 {
		t.Errorf("final event metrics mismatch: got %+v, want Passed=1, Failed=1, Inconclusive=1", finalEv.ProgressMetrics)
	}
}

func TestRunPool_CancelledProbesDoNotEmitProbeCompleted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	candidates := []parser.Candidate{
		{Link: "vless://cand1"},
		{Link: "vless://cand2"},
	}
	cfg := &config.TestConfig{
		Concurrency: 1,
	}

	started := make(chan struct{})
	runner := func(workerCtx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		close(started)
		<-workerCtx.Done()
		return store.Result{
			Link:   cand.Link,
			Status: store.StatusInconclusive,
			Reason: "context canceled",
		}
	}

	bus := events.New()
	defer bus.Close()
	subCh := bus.Subscribe(10)
	defer bus.Unsubscribe(subCh)

	done := make(chan bool)
	go func() {
		ok := tester.RunPoolWithRunner(ctx, candidates, cfg, runner, bus, func(r store.Result) {})
		done <- ok
	}()

	<-started
	cancel()
	<-done

	select {
	case evt := <-subCh:
		t.Fatalf("expected no ProbeCompleted events for cancelled probe, got %+v", evt)
	case <-time.After(100 * time.Millisecond):
		// Success
	}
}
