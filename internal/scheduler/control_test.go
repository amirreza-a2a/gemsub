package scheduler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
