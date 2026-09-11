package scheduler_test

import (
	"context"
	"fmt"
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
	"gemsub/internal/publisher"
	"gemsub/internal/scheduler"
	"gemsub/internal/store"
)

func TestScheduler_ConfigUpdated_DynamicFetchIntervalAndProbeLimit(t *testing.T) {
	// Source server returning 10 candidate links
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b []byte
		for i := 1; i <= 10; i++ {
			b = append(b, []byte(fmt.Sprintf("vless://node%d@127.0.0.1:443?type=tcp&security=none#node%d\n", i, i))...)
		}
		_, _ = w.Write(b)
	}))
	defer sourceSrv.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "1h",
		ProbeLimit:       2,
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  2,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	bus := events.New()
	defer bus.Close()

	sched := scheduler.New(cfg, st, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var cycleCount int64
	var probedCount int64
	var testRunner = func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		atomic.AddInt64(&probedCount, 1)
		return store.Result{
			Link:     cand.Link,
			Status:   store.StatusPassed,
			TestedAt: time.Now(),
		}
	}

	subCh := bus.Subscribe(20)
	defer bus.Unsubscribe(subCh)

	// Run scheduler in background with custom runner (avoiding upstream sing-box network monitor)
	go func() {
		sched.RunCycleForTest(ctx, testRunner)
		atomic.AddInt64(&cycleCount, 1)
	}()

	// Wait for first cycle to complete
	for {
		if atomic.LoadInt64(&cycleCount) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 1st cycle should have respected initial ProbeLimit=2
	if initialProbed := atomic.LoadInt64(&probedCount); initialProbed != 2 {
		t.Fatalf("expected initial cycle to probe 2 candidates, got %d", initialProbed)
	}

	// Publish ConfigUpdated with ProbeLimit=5 and FetchInterval="2h"
	oldCfg := sched.Config()
	newCfg := oldCfg
	newCfg.ProbeLimit = 5
	newCfg.FetchIntervalRaw = "2h"
	newCfg.FetchInterval = 2 * time.Hour

	bus.Publish(config.ConfigUpdated{
		Old: oldCfg,
		New: newCfg,
	})

	// Also directly verify UpdateConfig thread-safe method
	sched.UpdateConfig(newCfg)

	if sched.ProbeLimit() != 5 {
		t.Errorf("expected ProbeLimit updated to 5, got %d", sched.ProbeLimit())
	}
	if sched.FetchInterval() != 2*time.Hour {
		t.Errorf("expected FetchInterval updated to 2h, got %v", sched.FetchInterval())
	}

	// Run 2nd cycle and verify new ProbeLimit=5 takes effect
	atomic.StoreInt64(&probedCount, 0)
	sched.RunCycleForTest(ctx, testRunner)

	if secondProbed := atomic.LoadInt64(&probedCount); secondProbed != 5 {
		t.Fatalf("expected second cycle to probe 5 candidates under updated ProbeLimit, got %d", secondProbed)
	}
}

func TestScheduler_ConfigUpdated_DynamicSourceListUpdate(t *testing.T) {
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://nodeA@127.0.0.1:443?type=tcp&security=none#nodeA\n"))
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://nodeB@127.0.0.1:443?type=tcp&security=none#nodeB\n"))
	}))
	defer srv2.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          []string{srv1.URL},
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  2,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	sched := scheduler.New(cfg, st)

	ctx := context.Background()
	var linksTested []string
	var mu sync.Mutex
	var testRunner = func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		mu.Lock()
		linksTested = append(linksTested, cand.Link)
		mu.Unlock()
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	}

	// 1st cycle: only srv1
	sched.RunCycleForTest(ctx, testRunner)
	mu.Lock()
	if len(linksTested) != 1 || linksTested[0] != "vless://nodeA@127.0.0.1:443?type=tcp&security=none#nodeA" {
		t.Fatalf("unexpected links probed in first cycle: %v", linksTested)
	}
	linksTested = nil
	mu.Unlock()

	// Update sources to include both srv1 and srv2
	newCfg := sched.Config()
	newCfg.Sources = []string{srv1.URL, srv2.URL}
	sched.UpdateConfig(newCfg)

	// 2nd cycle: both srv1 and srv2
	sched.RunCycleForTest(ctx, testRunner)
	mu.Lock()
	if len(linksTested) != 2 {
		t.Fatalf("expected 2 links tested in second cycle after source update, got %d (%v)", len(linksTested), linksTested)
	}
	mu.Unlock()
}

func TestScheduler_InFlightCycleContinuesDuringConfigUpdate(t *testing.T) {
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://node1@127.0.0.1:443?type=tcp&security=none#node1\n"))
	}))
	defer sourceSrv.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  2,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	sched := scheduler.New(cfg, st)
	ctx := context.Background()

	cycleStarted := make(chan struct{})
	cycleCanFinish := make(chan struct{})
	cycleFinished := make(chan struct{})

	var runner = func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		close(cycleStarted)
		<-cycleCanFinish
		return store.Result{
			Link:     cand.Link,
			Status:   store.StatusPassed,
			TestedAt: time.Now(),
		}
	}

	go func() {
		sched.RunCycleForTest(ctx, runner)
		close(cycleFinished)
	}()

	// Wait until cycle is in-flight
	<-cycleStarted

	// While cycle is active in-flight, update configuration concurrently
	newCfg := sched.Config()
	newCfg.ProbeLimit = 42
	newCfg.FetchIntervalRaw = "3h"
	newCfg.FetchInterval = 3 * time.Hour

	updateDone := make(chan struct{})
	go func() {
		sched.UpdateConfig(newCfg)
		close(updateDone)
	}()

	select {
	case <-updateDone:
		// UpdateConfig must NOT block on in-flight cycle execution
	case <-time.After(500 * time.Millisecond):
		t.Fatal("UpdateConfig blocked waiting for in-flight cycle to finish")
	}

	// Verify updated configuration is set in scheduler state immediately
	if sched.ProbeLimit() != 42 {
		t.Errorf("expected ProbeLimit=42, got %d", sched.ProbeLimit())
	}

	// Allow cycle to complete
	close(cycleCanFinish)

	select {
	case <-cycleFinished:
		// In-flight cycle finished successfully without cancellation
	case <-time.After(1 * time.Second):
		t.Fatal("in-flight cycle timed out")
	}

	if st.Stats().Passed != 1 {
		t.Errorf("expected 1 passed candidate from completed cycle, got %d", st.Stats().Passed)
	}
}

func TestScheduler_ConcurrentConfigUpdatesAndCycles(t *testing.T) {
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://node1@127.0.0.1:443?type=tcp&security=none#node1\n"))
	}))
	defer sourceSrv.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  2,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	bus := events.New()
	defer bus.Close()
	sched := scheduler.New(cfg, st, bus)

	var runner = func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		time.Sleep(2 * time.Millisecond)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Cycle runner goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			sched.RunCycleForTest(ctx, runner)
		}
	}()

	// Concurrent configuration updater goroutines
	for i := 0; i < 4; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if ctx.Err() != nil {
					return
				}
				c := sched.Config()
				c.ProbeLimit = (workerID + j) % 10
				if (workerID+j)%2 == 0 {
					c.FetchIntervalRaw = "2h"
					c.FetchInterval = 2 * time.Hour
				} else {
					c.FetchIntervalRaw = "1h"
					c.FetchInterval = 1 * time.Hour
				}
				sched.UpdateConfig(c)
				bus.Publish(config.ConfigUpdated{Old: sched.Config(), New: c})
				_ = sched.Config()
				_ = sched.ProbeLimit()
				_ = sched.FetchInterval()
			}
		}()
	}

	wg.Wait()
}

func TestPublisher_ThreadSafeConfigUpdates(t *testing.T) {
	cfg := &config.PublishingConfig{
		Enabled:    false,
		Repository: "~/test-repo",
		Branch:     "main",
		RemoteURL:  "git@github.com:example/repo.git",
	}
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	pub := publisher.New(cfg, st)

	if pub.Config().Enabled {
		t.Error("expected initial Enabled=false")
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c := pub.Config()
				c.Enabled = (workerID+j)%2 == 0
				pub.UpdateConfig(c)
			}
		}()
	}
	wg.Wait()
}

func TestScheduler_Run_CanonicalIntervalResetViaIntervalCh(t *testing.T) {
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://node1@127.0.0.1:443?type=tcp&security=none#node1\n"))
	}))
	defer sourceSrv.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "10m",
		FetchInterval:    10 * time.Minute,
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
	defer bus.Close()
	sched := scheduler.New(cfg, st, bus)

	var cycleCount int64
	var runner = func(ctx context.Context, cand parser.Candidate, tc *config.TestConfig, limiter *rate.Limiter) store.Result {
		atomic.AddInt64(&cycleCount, 1)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	}
	sched.SetRunnerForTest(runner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sched.Run(ctx)

	// Wait for initial startup cycle to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&cycleCount) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&cycleCount) != 1 {
		t.Fatalf("expected 1 startup cycle, got %d", atomic.LoadInt64(&cycleCount))
	}

	// Publish ConfigUpdated changing interval from 10m to 50ms
	// Canonical path: ConfigUpdated -> UpdateConfig() -> intervalCh -> Run() owns ticker.Reset()
	newCfg := sched.Config()
	newCfg.FetchIntervalRaw = "50ms"
	newCfg.FetchInterval = 50 * time.Millisecond

	bus.Publish(config.ConfigUpdated{
		Old: sched.Config(),
		New: newCfg,
	})

	// Wait for second cycle to trigger from the new 50ms ticker (would take 10m without reset)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&cycleCount) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if count := atomic.LoadInt64(&cycleCount); count < 2 {
		t.Fatalf("expected at least 2 cycles within 2s after interval reset to 50ms, got %d", count)
	}
}
