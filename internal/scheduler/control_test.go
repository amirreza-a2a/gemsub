package scheduler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/parser"
	"gemsub/internal/scheduler"
	"gemsub/internal/store"
)

func setupTestScheduler(t *testing.T) (*scheduler.Scheduler, *store.Store, *events.EventBus, string) {
	t.Helper()
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://node1@127.0.0.1:443?type=tcp&security=none#node1\n"))
	}))
	t.Cleanup(sourceSrv.Close)

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          config.NewSources(sourceSrv.URL),
		FetchIntervalRaw: "1h",
		FetchInterval:    1 * time.Hour,
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  1,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	bus := events.New()
	t.Cleanup(func() { bus.Close() })

	sched := scheduler.New(cfg, st, bus)
	return sched, st, bus, sourceSrv.URL
}

func TestControlService_StartAndStop(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	var cycleCount int64
	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		atomic.AddInt64(&cycleCount, 1)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)

	if ctrl.IsRunning() {
		t.Error("expected scheduler not to be running before Start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Start
	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !ctrl.IsRunning() {
		t.Error("expected scheduler to be running after Start")
	}

	// Wait for initial cycle
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&cycleCount) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&cycleCount) < 1 {
		t.Errorf("expected at least 1 cycle after Start, got %d", atomic.LoadInt64(&cycleCount))
	}

	// 2. Stop
	if err := ctrl.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if ctrl.IsRunning() {
		t.Error("expected scheduler not to be running after Stop")
	}

	// 3. Duplicate Stop must be idempotent (no error)
	if err := ctrl.Stop(); err != nil {
		t.Errorf("duplicate Stop returned error: %v", err)
	}
}

func TestControlService_DuplicateStartRejected(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("initial Start failed: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	// Duplicate Start while already running must fail
	err := ctrl.Start(ctx)
	if !errors.Is(err, scheduler.ErrSchedulerAlreadyRunning) {
		t.Fatalf("expected ErrSchedulerAlreadyRunning on duplicate Start, got %v", err)
	}
}

func TestControlService_RestartAfterStop(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	var cycleCount int64
	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		atomic.AddInt64(&cycleCount, 1)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)

	// 1st run
	ctx1, cancel1 := context.WithCancel(context.Background())
	if err := ctrl.Start(ctx1); err != nil {
		t.Fatalf("first Start failed: %v", err)
	}

	// Wait for 1st cycle
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&cycleCount) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&cycleCount) < 1 {
		t.Fatalf("expected cycle in 1st run, got %d", atomic.LoadInt64(&cycleCount))
	}

	cancel1()
	if err := ctrl.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if ctrl.IsRunning() {
		t.Fatal("expected scheduler to be stopped")
	}

	// 2nd run (restart)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if err := ctrl.Start(ctx2); err != nil {
		t.Fatalf("second Start (restart) failed: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	// Wait for 2nd run initial cycle
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&cycleCount) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&cycleCount) < 2 {
		t.Fatalf("expected cycle in 2nd run after restart, got %d", atomic.LoadInt64(&cycleCount))
	}
}

func TestControlService_TriggerWhileStopped(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)
	ctrl := scheduler.NewControlService(sched)

	// Trigger while stopped must return ErrSchedulerNotRunning
	err := ctrl.Trigger()
	if !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
		t.Fatalf("expected ErrSchedulerNotRunning, got %v", err)
	}
}

func TestControlService_TriggerWhileRunning(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	var cycleCount int64
	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		atomic.AddInt64(&cycleCount, 1)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	// Wait for startup cycle
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&cycleCount) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&cycleCount) != 1 {
		t.Fatalf("expected 1 initial cycle, got %d", atomic.LoadInt64(&cycleCount))
	}

	// Trigger immediate cycle (FetchInterval is 1h, so it will only run if triggered)
	if err := ctrl.Trigger(); err != nil {
		t.Fatalf("Trigger failed: %v", err)
	}

	// Wait for triggered cycle
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&cycleCount) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&cycleCount) != 2 {
		t.Fatalf("expected 2nd cycle from Trigger, got %d", atomic.LoadInt64(&cycleCount))
	}
}

func TestControlService_TriggerDuringActiveCycle(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	cycleRunning := make(chan struct{})
	allowCycleFinish := make(chan struct{})
	var cycleCount int64

	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		count := atomic.AddInt64(&cycleCount, 1)
		if count == 1 {
			close(cycleRunning)
			<-allowCycleFinish
		}
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() {
		select {
		case <-allowCycleFinish:
		default:
			close(allowCycleFinish)
		}
		_ = ctrl.Stop()
	}()

	// Wait until cycle 1 is actively executing in runner
	<-cycleRunning

	// Trigger while cycle 1 is in progress
	if err := ctrl.Trigger(); err != nil {
		t.Fatalf("Trigger during active cycle failed: %v", err)
	}

	// Multiple triggers during active cycle coalesce safely
	if err := ctrl.Trigger(); err != nil {
		t.Fatalf("second Trigger during active cycle failed: %v", err)
	}

	// Allow cycle 1 to finish
	close(allowCycleFinish)

	// Cycle 2 should execute as a result of the queued trigger
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&cycleCount) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&cycleCount) < 2 {
		t.Fatalf("expected cycle 2 from queued trigger, got %d", atomic.LoadInt64(&cycleCount))
	}
}

func TestControlService_StatusTelemetry(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	cycleStarted := make(chan struct{})
	allowCycleEnd := make(chan struct{})

	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		select {
		case <-cycleStarted:
		default:
			close(cycleStarted)
		}
		<-allowCycleEnd
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)

	// Status before start
	initSt := ctrl.Status()
	if initSt.Running {
		t.Error("expected Running=false before Start")
	}
	if initSt.CycleActive {
		t.Error("expected CycleActive=false before Start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() {
		select {
		case <-allowCycleEnd:
		default:
			close(allowCycleEnd)
		}
		_ = ctrl.Stop()
	}()

	// Wait for cycle to become active
	<-cycleStarted

	activeSt := ctrl.Status()
	if !activeSt.Running {
		t.Error("expected Running=true while running")
	}
	if !activeSt.CycleActive {
		t.Error("expected CycleActive=true during active cycle")
	}
	if activeSt.LastCycleStart.IsZero() {
		t.Error("expected non-zero LastCycleStart")
	}

	// Allow cycle to finish
	close(allowCycleEnd)

	// Wait for cycle to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := ctrl.Status()
		if !st.CycleActive && !st.LastCycleEnd.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	doneSt := ctrl.Status()
	if !doneSt.Running {
		t.Error("expected Running=true after cycle completes")
	}
	if doneSt.CycleActive {
		t.Error("expected CycleActive=false after cycle completes")
	}
	if doneSt.LastCycleEnd.IsZero() {
		t.Error("expected non-zero LastCycleEnd")
	}
	if doneSt.LastDuration <= 0 {
		t.Errorf("expected positive LastDuration, got %v", doneSt.LastDuration)
	}
}

func TestControlService_ConcurrentOperationsAreRaceFree(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = ctrl.Start(ctx)
				_ = ctrl.IsRunning()
				_ = ctrl.Status()
				_ = ctrl.Trigger()
				_ = ctrl.Stop()
			}
		}()
	}
	wg.Wait()
}

func TestControlService_StartWithAlreadyCancelledContext(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	var cycleStarted atomic.Bool
	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		cycleStarted.Store(true)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately before Start

	err := ctrl.Start(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error on Start with cancelled context, got: %v", err)
	}

	// Deterministic rejection guarantees scheduler is not running and no cycles run
	if ctrl.IsRunning() {
		t.Error("expected scheduler not to be running after cancelled Start")
	}
	if cycleStarted.Load() {
		t.Error("expected zero cycles to execute when Start is rejected with cancelled context")
	}

	// Ensure Stop() is safe and idempotent afterwards
	if err := ctrl.Stop(); err != nil {
		t.Errorf("Stop after rejected start returned error: %v", err)
	}
}

func TestControlService_StopDuringActiveCycle(t *testing.T) {
	sched, _, bus, _ := setupTestScheduler(t)

	cycleStarted := make(chan struct{})
	cycleFinished := make(chan events.CycleFinished, 1)

	subCh := bus.Subscribe()
	defer bus.Unsubscribe(subCh)

	go func() {
		for raw := range subCh {
			if cf, ok := raw.(events.CycleFinished); ok {
				select {
				case cycleFinished <- cf:
				default:
				}
			}
		}
	}()

	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		select {
		case <-cycleStarted:
		default:
			close(cycleStarted)
		}
		// Block in runner until context is cancelled
		<-ctx.Done()
		return store.Result{Link: cand.Link, Status: store.StatusFailed, Reason: "cancelled", TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for cycle to actively begin executing
	<-cycleStarted

	st := ctrl.Status()
	if !st.Running {
		t.Error("expected Running=true during active cycle")
	}
	if !st.CycleActive {
		t.Error("expected CycleActive=true during active cycle")
	}

	// Stop during active cycle
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- ctrl.Stop()
	}()

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop during active cycle returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not complete in timely fashion during active cycle")
	}

	if ctrl.IsRunning() {
		t.Error("expected Running=false after Stop")
	}

	select {
	case cf := <-cycleFinished:
		if !cf.Cancelled {
			t.Error("expected CycleFinished.Cancelled=true when stopped during cycle")
		}
	case <-time.After(1 * time.Second):
		t.Error("expected CycleFinished event after stop during cycle")
	}

	finalSt := ctrl.Status()
	if finalSt.CycleActive {
		t.Error("expected CycleActive=false after stop completed")
	}
	if !finalSt.LastCancelled {
		t.Error("expected LastCancelled=true in telemetry")
	}
}

func TestControlService_ParentContextCancellationCleansUpRunningState(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)
	ctx, cancel := context.WithCancel(context.Background())

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !ctrl.IsRunning() {
		t.Fatal("expected Running=true after Start")
	}

	// Cancel parent context directly (without calling Stop)
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !ctrl.IsRunning() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ctrl.IsRunning() {
		t.Error("expected Running=false after parent context cancelled")
	}

	// Subsequent Start with a new context must succeed
	newCtx, newCancel := context.WithCancel(context.Background())
	defer newCancel()
	if err := ctrl.Start(newCtx); err != nil {
		t.Fatalf("Start after parent cancellation failed: %v", err)
	}
	if !ctrl.IsRunning() {
		t.Error("expected Running=true after restarted with new context")
	}
	_ = ctrl.Stop()
}

func waitForControlEvent[T any](t *testing.T, subCh <-chan any, timeout time.Duration) T {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case evt, ok := <-subCh:
			if !ok {
				t.Fatalf("subCh closed while waiting for %T", *new(T))
			}
			if target, match := evt.(T); match {
				return target
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %T", *new(T))
		}
	}
}

func assertNoControlEvent[T any](t *testing.T, subCh <-chan any, timeout time.Duration, msg string) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case evt, ok := <-subCh:
			if !ok {
				return
			}
			if _, match := evt.(T); match {
				t.Fatal(msg)
			}
		case <-timer.C:
			return
		}
	}
}

func TestControlService_PauseResumeLifecycleAndEvents(t *testing.T) {
	sched, _, bus, _ := setupTestScheduler(t)
	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})
	subCh := bus.Subscribe()
	defer bus.Unsubscribe(subCh)

	ctrl := scheduler.NewControlService(sched)

	// 1. Prior to Start, state is StateIdle
	initSt := ctrl.Status()
	if initSt.State != scheduler.StateIdle {
		t.Errorf("initial state = %v, want StateIdle", initSt.State)
	}
	if initSt.Running || initSt.Paused {
		t.Errorf("initial Running=%v Paused=%v, want false/false", initSt.Running, initSt.Paused)
	}
	if !initSt.NextCycleEstimate.IsZero() {
		t.Errorf("initial NextCycleEstimate = %v, want zero", initSt.NextCycleEstimate)
	}

	// Calling Pause or Resume before Start returns ErrSchedulerNotRunning
	if err := ctrl.Pause(); !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
		t.Errorf("Pause before Start err = %v, want ErrSchedulerNotRunning", err)
	}
	if err := ctrl.Resume(); !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
		t.Errorf("Resume before Start err = %v, want ErrSchedulerNotRunning", err)
	}

	// 2. Start scheduler
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	// State should become StateRunning
	stAfterStart := ctrl.Status()
	if stAfterStart.State != scheduler.StateRunning {
		t.Errorf("state after Start = %v, want StateRunning", stAfterStart.State)
	}
	if !stAfterStart.Running || stAfterStart.Paused {
		t.Errorf("Running=%v Paused=%v, want true/false", stAfterStart.Running, stAfterStart.Paused)
	}

	// 3. Pause scheduler
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}

	// Verify event emission: wait deterministically for SchedulerPaused, ignoring unrelated cycle events
	waitForControlEvent[events.SchedulerPaused](t, subCh, 1*time.Second)

	// State should be StatePaused
	stPaused := ctrl.Status()
	if stPaused.State != scheduler.StatePaused {
		t.Errorf("state after Pause = %v, want StatePaused", stPaused.State)
	}
	if !stPaused.Paused {
		t.Errorf("Paused=false, want true")
	}
	if !stPaused.NextCycleEstimate.IsZero() {
		t.Errorf("NextCycleEstimate when paused = %v, want zero", stPaused.NextCycleEstimate)
	}

	// Calling Pause again returns ErrSchedulerAlreadyPaused and no duplicate event
	if err := ctrl.Pause(); !errors.Is(err, scheduler.ErrSchedulerAlreadyPaused) {
		t.Errorf("second Pause err = %v, want ErrSchedulerAlreadyPaused", err)
	}
	assertNoControlEvent[events.SchedulerPaused](t, subCh, 50*time.Millisecond, "unexpected duplicate events.SchedulerPaused emitted on second Pause")

	// 4. Resume scheduler
	if err := ctrl.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	// Verify resume event: wait deterministically for SchedulerResumed, ignoring unrelated cycle events
	waitForControlEvent[events.SchedulerResumed](t, subCh, 1*time.Second)

	stResumed := ctrl.Status()
	if stResumed.State != scheduler.StateRunning {
		t.Errorf("state after Resume = %v, want StateRunning", stResumed.State)
	}
	if stResumed.Paused {
		t.Errorf("Paused=true, want false after Resume")
	}
	if stResumed.NextCycleEstimate.IsZero() {
		t.Errorf("NextCycleEstimate after Resume is zero, want valid future time")
	}

	// Calling Resume again returns ErrSchedulerNotPaused and no duplicate event
	if err := ctrl.Resume(); !errors.Is(err, scheduler.ErrSchedulerNotPaused) {
		t.Errorf("second Resume err = %v, want ErrSchedulerNotPaused", err)
	}
	assertNoControlEvent[events.SchedulerResumed](t, subCh, 50*time.Millisecond, "unexpected duplicate events.SchedulerResumed emitted on second Resume")
}

func TestControlService_TriggerWhilePaused_Rejection(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)
	ctrl := scheduler.NewControlService(sched)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}

	// Trigger while paused must return ErrSchedulerPaused
	err := ctrl.Trigger()
	if !errors.Is(err, scheduler.ErrSchedulerPaused) {
		t.Fatalf("Trigger while paused err = %v, want ErrSchedulerPaused", err)
	}

	// TriggerNow while paused must return false
	if ok := ctrl.TriggerNow(); ok {
		t.Fatalf("TriggerNow while paused returned true, want false")
	}

	// Resume and verify Trigger now succeeds
	if err := ctrl.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if err := ctrl.Trigger(); err != nil {
		t.Fatalf("Trigger after Resume failed: %v", err)
	}
	if ok := ctrl.TriggerNow(); !ok {
		t.Fatalf("TriggerNow after Resume returned false, want true (coalesced)")
	}
}

func TestControlService_PauseDoesNotInterruptActiveCycle(t *testing.T) {
	sched, _, bus, _ := setupTestScheduler(t)

	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})
	probeFinished := make(chan struct{})

	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		close(probeStarted)
		<-probeRelease
		close(probeFinished)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	cycleFinishedCh := make(chan events.CycleFinished, 1)
	subCh := bus.Subscribe()
	defer bus.Unsubscribe(subCh)
	go func() {
		for evt := range subCh {
			if cf, ok := evt.(events.CycleFinished); ok {
				cycleFinishedCh <- cf
			}
		}
	}()

	ctrl := scheduler.NewControlService(sched)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	// Wait for active cycle probe to start
	<-probeStarted

	if !ctrl.Status().CycleActive {
		t.Errorf("expected CycleActive=true while probe in flight")
	}

	// Call Pause while active cycle is in progress
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause during active cycle returned error: %v", err)
	}

	if !ctrl.IsPaused() {
		t.Errorf("expected IsPaused=true after Pause()")
	}

	// Release probe
	close(probeRelease)
	<-probeFinished

	// Verify cycle completes normally (not cancelled)
	select {
	case cf := <-cycleFinishedCh:
		if cf.Cancelled {
			t.Errorf("expected active cycle not to be cancelled by Pause, got Cancelled=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for active cycle to finish")
	}

	// Verify status after cycle completion: still Paused, CycleActive=false
	st := ctrl.Status()
	if st.State != scheduler.StatePaused {
		t.Errorf("expected state to remain StatePaused after cycle completed, got %v", st.State)
	}
	if st.CycleActive {
		t.Errorf("expected CycleActive=false after cycle finished")
	}
	if !st.NextCycleEstimate.IsZero() {
		t.Errorf("expected NextCycleEstimate=zero while paused, got %v", st.NextCycleEstimate)
	}
}

func TestControlService_ConcurrentPauseResumeTriggerRace(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)
	ctrl := scheduler.NewControlService(sched)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			_ = ctrl.Pause()
		}()
		go func() {
			defer wg.Done()
			_ = ctrl.Resume()
		}()
		go func() {
			defer wg.Done()
			_ = ctrl.Trigger()
		}()
		go func() {
			defer wg.Done()
			st := ctrl.Status()
			_ = st.State.String()
			_ = ctrl.IsPaused()
			_ = ctrl.IsRunning()
		}()
	}
	wg.Wait()
}

func TestControlService_ConcurrentPauseVsStop(t *testing.T) {
	for iter := 0; iter < 25; iter++ {
		sched, _, bus, _ := setupTestScheduler(t)
		ctrl := scheduler.NewControlService(sched)

		subCh := bus.Subscribe()

		ctx, cancel := context.WithCancel(context.Background())
		if err := ctrl.Start(ctx); err != nil {
			t.Fatalf("iter %d: Start: %v", iter, err)
		}

		var pauseErr error
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			pauseErr = ctrl.Pause()
		}()

		go func() {
			defer wg.Done()
			_ = ctrl.Stop()
			cancel()
		}()

		wg.Wait()

		// If Pause failed, it must be ErrSchedulerNotRunning
		if pauseErr != nil && !errors.Is(pauseErr, scheduler.ErrSchedulerNotRunning) && !errors.Is(pauseErr, scheduler.ErrSchedulerAlreadyPaused) {
			t.Errorf("iter %d: unexpected pauseErr: %v", iter, pauseErr)
		}

		// After Stop completes, any subsequent Pause must fail with ErrSchedulerNotRunning
		if err := ctrl.Pause(); !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
			t.Errorf("iter %d: Pause after Stop returned %v, want ErrSchedulerNotRunning", iter, err)
		}

		// Drain events: no SchedulerPaused event may be emitted after Stop has concluded
		bus.Unsubscribe(subCh)
		select {
		case evt, ok := <-subCh:
			if ok {
				if _, isPaused := evt.(events.SchedulerPaused); isPaused && pauseErr != nil {
					t.Errorf("iter %d: received SchedulerPaused event when Pause returned error %v", iter, pauseErr)
				}
			}
		default:
		}
	}
}

func TestControlService_ConcurrentResumeVsStop(t *testing.T) {
	for iter := 0; iter < 25; iter++ {
		sched, _, bus, _ := setupTestScheduler(t)
		ctrl := scheduler.NewControlService(sched)

		subCh := bus.Subscribe()

		ctx, cancel := context.WithCancel(context.Background())
		if err := ctrl.Start(ctx); err != nil {
			t.Fatalf("iter %d: Start: %v", iter, err)
		}

		// First pause so Resume is potentially valid
		if err := ctrl.Pause(); err != nil {
			t.Fatalf("iter %d: initial Pause failed: %v", iter, err)
		}

		// Drain the SchedulerPaused event
		for i := 0; i < 5; i++ {
			select {
			case <-subCh:
			default:
			}
		}

		var resumeErr error
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			resumeErr = ctrl.Resume()
		}()

		go func() {
			defer wg.Done()
			_ = ctrl.Stop()
			cancel()
		}()

		wg.Wait()

		// If Resume failed, it must be ErrSchedulerNotRunning
		if resumeErr != nil && !errors.Is(resumeErr, scheduler.ErrSchedulerNotRunning) && !errors.Is(resumeErr, scheduler.ErrSchedulerNotPaused) {
			t.Errorf("iter %d: unexpected resumeErr: %v", iter, resumeErr)
		}

		// After Stop completes, any subsequent Resume must fail with ErrSchedulerNotRunning
		if err := ctrl.Resume(); !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
			t.Errorf("iter %d: Resume after Stop returned %v, want ErrSchedulerNotRunning", iter, err)
		}

		// Unsubscribe
		bus.Unsubscribe(subCh)
	}
}

func TestControlService_TriggerDoesNotSurviveStop(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	var cycleInvocations int64
	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		atomic.AddInt64(&cycleInvocations, 1)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)

	ctx1, cancel1 := context.WithCancel(context.Background())
	if err := ctrl.Start(ctx1); err != nil {
		t.Fatalf("Start ctx1: %v", err)
	}

	// Wait for the initial cycle of ctx1 to finish
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&cycleInvocations) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&cycleInvocations) < 1 {
		t.Fatalf("initial cycle did not run")
	}

	// Queue a trigger and immediately stop
	_ = ctrl.Trigger()
	_ = ctrl.Stop()
	cancel1()

	// Initial cycles complete count
	countAfterStop := atomic.LoadInt64(&cycleInvocations)

	// Now start a fresh cycle session
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	if err := ctrl.Start(ctx2); err != nil {
		t.Fatalf("Start ctx2: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	// Wait for ctx2's initial startup cycle
	deadline = time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&cycleInvocations) <= countAfterStop && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	wantCountAfterStart := countAfterStop + 1
	if got := atomic.LoadInt64(&cycleInvocations); got != wantCountAfterStart {
		t.Fatalf("expected cycleCount=%d after Start, got %d", wantCountAfterStart, got)
	}

	// Ensure no queued trigger survived Stop to trigger an extra immediate cycle
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt64(&cycleInvocations); got != wantCountAfterStart {
		t.Fatalf("stale trigger from before Stop fired extra cycle: got count=%d, want %d", got, wantCountAfterStart)
	}
}

func TestControlService_PauseStopStartLifecycle(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)
	ctrl := scheduler.NewControlService(sched)

	// 1. Start -> RUNNING
	ctx1, cancel1 := context.WithCancel(context.Background())
	if err := ctrl.Start(ctx1); err != nil {
		t.Fatalf("Start: %v", err)
	}

	st1 := ctrl.Status()
	if st1.State != scheduler.StateRunning || !st1.Running || st1.Paused {
		t.Fatalf("initial state mismatch: %+v", st1)
	}

	// 2. Pause -> PAUSED
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !ctrl.IsPaused() {
		t.Fatalf("expected IsPaused() == true")
	}
	stPaused := ctrl.Status()
	if stPaused.State != scheduler.StatePaused || !stPaused.Paused || !stPaused.NextCycleEstimate.IsZero() {
		t.Fatalf("paused state mismatch: %+v", stPaused)
	}

	// 3. Stop -> IDLE (pause does NOT survive Stop)
	if err := ctrl.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	cancel1()

	if ctrl.IsRunning() {
		t.Errorf("expected IsRunning() == false after Stop")
	}
	if ctrl.IsPaused() {
		t.Errorf("expected IsPaused() == false after Stop (pause must not survive Stop)")
	}

	stStopped := ctrl.Status()
	if stStopped.State != scheduler.StateIdle {
		t.Errorf("stStopped.State = %v, want StateIdle", stStopped.State)
	}
	if stStopped.Running {
		t.Errorf("stStopped.Running = true, want false")
	}
	if stStopped.Paused {
		t.Errorf("stStopped.Paused = true, want false")
	}
	if !stStopped.NextCycleEstimate.IsZero() {
		t.Errorf("stStopped.NextCycleEstimate = %v, want zero", stStopped.NextCycleEstimate)
	}

	// 4. Next Start -> StateRunning (not StatePaused!)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if err := ctrl.Start(ctx2); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	st2 := ctrl.Status()
	if st2.State != scheduler.StateRunning {
		t.Errorf("st2.State = %v, want StateRunning (start must not begin in paused state)", st2.State)
	}
	if !st2.Running {
		t.Errorf("st2.Running = false, want true")
	}
	if st2.Paused {
		t.Errorf("st2.Paused = true, want false")
	}
	if st2.NextCycleEstimate.IsZero() {
		t.Errorf("st2.NextCycleEstimate is zero, want valid future timestamp")
	}
}

func TestControlService_RunningToStop_CleanIdleState(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)
	ctrl := scheduler.NewControlService(sched)

	ctx, cancel := context.WithCancel(context.Background())
	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := ctrl.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	cancel()

	st := ctrl.Status()
	if st.State != scheduler.StateIdle {
		t.Errorf("State = %v, want StateIdle", st.State)
	}
	if st.Running {
		t.Errorf("Running = true, want false")
	}
	if st.Paused {
		t.Errorf("Paused = true, want false")
	}
	if st.CycleActive {
		t.Errorf("CycleActive = true, want false")
	}
	if !st.NextCycleEstimate.IsZero() {
		t.Errorf("NextCycleEstimate = %v, want zero", st.NextCycleEstimate)
	}
}

func TestControlService_StatusSnapshotConsistency(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)
	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})
	ctrl := scheduler.NewControlService(sched)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	stopStress := make(chan struct{})
	var wg sync.WaitGroup

	// Reader goroutines asserting invariants on every snapshot
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopStress:
					return
				default:
					st := ctrl.Status()
					switch st.State {
					case scheduler.StateIdle:
						if st.Running {
							t.Errorf("incoherent snapshot: StateIdle with Running=true: %+v", st)
						}
						if st.Paused {
							t.Errorf("incoherent snapshot: StateIdle with Paused=true: %+v", st)
						}
						if st.CycleActive {
							t.Errorf("incoherent snapshot: StateIdle with CycleActive=true: %+v", st)
						}
						if !st.NextCycleEstimate.IsZero() {
							t.Errorf("incoherent snapshot: StateIdle with non-zero NextCycleEstimate: %+v", st)
						}
					case scheduler.StatePaused:
						if !st.Running {
							t.Errorf("incoherent snapshot: StatePaused with Running=false: %+v", st)
						}
						if !st.Paused {
							t.Errorf("incoherent snapshot: StatePaused with Paused=false: %+v", st)
						}
						if !st.NextCycleEstimate.IsZero() {
							t.Errorf("incoherent snapshot: StatePaused with non-zero NextCycleEstimate: %+v", st)
						}
					case scheduler.StateRunning:
						if !st.Running {
							t.Errorf("incoherent snapshot: StateRunning with Running=false: %+v", st)
						}
						if st.Paused {
							t.Errorf("incoherent snapshot: StateRunning with Paused=true: %+v", st)
						}
					}
				}
			}
		}()
	}

	// Mutator goroutines exercising lifecycle
	for i := 0; i < 50; i++ {
		_ = ctrl.Pause()
		_ = ctrl.Trigger()
		_ = ctrl.Resume()
		_ = ctrl.TriggerNow()
	}

	close(stopStress)
	wg.Wait()
}

func TestControlService_StatusSnapshotConsistency_WithConcurrentStop(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	// Coordination channels for deterministic lifecycle control
	inCycle := make(chan struct{}, 1)
	blockRunner := make(chan struct{})

	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		select {
		case inCycle <- struct{}{}:
		default:
		}
		// Hold cycle active until test explicitly unblocks it
		<-blockRunner
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})
	ctrl := scheduler.NewControlService(sched)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 1. Wait for cycle to be actively executing
	<-inCycle

	stInitial := ctrl.Status()
	if !stInitial.Running || !stInitial.CycleActive {
		t.Fatalf("expected running scheduler with active cycle, got %+v", stInitial)
	}

	// 2. Verify pre-stopping contract: operations succeed according to normal state
	if err := ctrl.Pause(); err != nil {
		t.Errorf("pre-stop Pause failed: %v", err)
	}
	stPaused := ctrl.Status()
	if !stPaused.Paused || stPaused.State != scheduler.StatePaused {
		t.Errorf("expected StatePaused after pre-stop Pause, got %+v", stPaused)
	}
	// Calling Pause again returns normal state-dependent error
	if err := ctrl.Pause(); !errors.Is(err, scheduler.ErrSchedulerAlreadyPaused) {
		t.Errorf("second Pause: expected ErrSchedulerAlreadyPaused, got %v", err)
	}
	// Resume restores running state
	if err := ctrl.Resume(); err != nil {
		t.Errorf("pre-stop Resume failed: %v", err)
	}
	stResumed := ctrl.Status()
	if stResumed.Paused || stResumed.State != scheduler.StateRunning {
		t.Errorf("expected StateRunning after pre-stop Resume, got %+v", stResumed)
	}
	// Trigger succeeds
	if err := ctrl.Trigger(); err != nil {
		t.Errorf("pre-stop Trigger failed: %v", err)
	}

	// 3. Initiate Stop() concurrently. Since the runner is blocked on blockRunner,
	// Stop() sets stopping=true and blocks awaiting cycle completion.
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- ctrl.Stop()
	}()

	// Wait deterministically for the stopping transition to take effect
	for ctrl.IsRunning() {
		runtime.Gosched()
	}

	// 4. Concurrently verify that once stopping is true, NO control operation can succeed.
	// All operations must return ErrSchedulerNotRunning.
	var (
		successfulOps atomic.Int64
		totalOps      atomic.Int64
		wrongErrors   atomic.Int64
		stopStress    = make(chan struct{})
		wg            sync.WaitGroup
	)

	// Mutators calling Pause, Resume, Trigger during the stopping window
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopStress:
					return
				default:
					totalOps.Add(3)
					if err := ctrl.Pause(); err == nil {
						successfulOps.Add(1)
					} else if !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
						wrongErrors.Add(1)
					}
					if err := ctrl.Resume(); err == nil {
						successfulOps.Add(1)
					} else if !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
						wrongErrors.Add(1)
					}
					if err := ctrl.Trigger(); err == nil {
						successfulOps.Add(1)
					} else if !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
						wrongErrors.Add(1)
					}
				}
			}
		}()
	}

	// Readers asserting snapshot invariants during the stopping window
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopStress:
					return
				default:
					st := ctrl.Status()
					if st.State != scheduler.StateIdle {
						t.Errorf("incoherent snapshot during stopping: expected StateIdle, got %v", st.State)
					}
					if st.Running || st.Paused || st.CycleActive || !st.NextCycleEstimate.IsZero() {
						t.Errorf("incoherent snapshot during stopping: %+v", st)
					}
				}
			}
		}()
	}

	// Give mutators and readers sufficient iterations under the stopping window
	for totalOps.Load() < 120 {
		runtime.Gosched()
	}

	// 5. Unblock the runner so Stop() can complete
	close(blockRunner)

	// Wait for Stop() to return cleanly
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() timed out waiting for cycle completion")
	}

	// Stop mutator and reader workers
	close(stopStress)
	wg.Wait()

	// Assert that during stopping:
	// - At least some operations were attempted
	// - Zero operations succeeded
	// - Zero wrong errors were encountered
	if totalOps.Load() == 0 {
		t.Error("expected operations to be executed during stopping window")
	}
	if succ := successfulOps.Load(); succ > 0 {
		t.Errorf("contract violation: %d control operations succeeded after stopping began", succ)
	}
	if wrong := wrongErrors.Load(); wrong > 0 {
		t.Errorf("contract violation: %d control operations returned error other than ErrSchedulerNotRunning during stopping", wrong)
	}

	// 6. Verify post-Stop state invariants and error contract:
	stPost := ctrl.Status()
	if stPost.State != scheduler.StateIdle {
		t.Errorf("post-stop State = %v, want StateIdle", stPost.State)
	}
	if stPost.Running || stPost.Paused || stPost.CycleActive || !stPost.NextCycleEstimate.IsZero() {
		t.Errorf("post-stop snapshot not fully idle: %+v", stPost)
	}

	if err := ctrl.Pause(); !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
		t.Errorf("post-stop Pause: want ErrSchedulerNotRunning, got %v", err)
	}
	if err := ctrl.Resume(); !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
		t.Errorf("post-stop Resume: want ErrSchedulerNotRunning, got %v", err)
	}
	if err := ctrl.Trigger(); !errors.Is(err, scheduler.ErrSchedulerNotRunning) {
		t.Errorf("post-stop Trigger: want ErrSchedulerNotRunning, got %v", err)
	}

	// 7. Restart scheduler and verify:
	// - Pause did not survive Stop -> Start
	// - Trigger did not survive Stop -> Start
	// - NextCycleEstimate is recomputed and valid
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	// New mock runner for the second run that completes immediately
	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	if err := ctrl.Start(ctx2); err != nil {
		t.Fatalf("Start 2 failed: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	stRestart := ctrl.Status()
	if stRestart.Paused {
		t.Errorf("pause state survived across Stop -> Start: %+v", stRestart)
	}
	if stRestart.State != scheduler.StateRunning {
		t.Errorf("expected StateRunning on restart, got %v", stRestart.State)
	}
	if stRestart.NextCycleEstimate.IsZero() {
		t.Errorf("expected non-zero NextCycleEstimate on restart")
	}
}

func TestScheduler_IntervalChangeDuringCycleSynchronizesTickerAndEstimate(t *testing.T) {
	sched, _, _, _ := setupTestScheduler(t)

	newInterval := 2 * time.Hour

	inCycle := make(chan struct{}, 1)
	blockCycle := make(chan struct{})
	var closeOnce sync.Once
	safeUnblock := func() {
		closeOnce.Do(func() {
			close(blockCycle)
		})
	}
	defer safeUnblock()

	sched.SetRunnerForTest(func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		select {
		case inCycle <- struct{}{}:
		default:
		}
		<-blockCycle
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	})

	ctrl := scheduler.NewControlService(sched)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = ctrl.Stop() }()

	// Wait for startup cycle to enter runner
	<-inCycle

	// Step 3 & 4: While cycle is executing, UpdateConfig updates scheduler config and signals intervalCh
	cfg := sched.Config()
	cfg.FetchInterval = newInterval
	cfg.FetchIntervalRaw = "2h"
	sched.UpdateConfig(cfg)

	if sched.FetchInterval() != newInterval {
		t.Fatalf("FetchInterval = %v, want %v", sched.FetchInterval(), newInterval)
	}

	// Step 5: Unblock cycle completion
	cycleCompletedTime := time.Now()
	safeUnblock()

	// Wait for cycle to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := ctrl.Status()
		if !st.CycleActive && !st.LastCycleEnd.IsZero() {
			break
		}
		runtime.Gosched()
	}

	st := ctrl.Status()
	if st.CycleActive {
		t.Fatal("cycle still active after unblock")
	}

	// Step 6 & 7: Verify NextCycleEstimate is based on newInterval (2h), not initial 1h
	expectedEstimateMin := cycleCompletedTime.Add(newInterval - 5*time.Second)
	expectedEstimateMax := time.Now().Add(newInterval + 5*time.Second)

	if st.NextCycleEstimate.Before(expectedEstimateMin) || st.NextCycleEstimate.After(expectedEstimateMax) {
		t.Errorf("NextCycleEstimate = %v, expected between %v and %v (based on %v)",
			st.NextCycleEstimate, expectedEstimateMin, expectedEstimateMax, newInterval)
	}
}
