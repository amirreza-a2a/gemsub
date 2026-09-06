package scheduler_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/publisher"
	"gemsub/internal/scheduler"
	"gemsub/internal/store"
)

type mockGitRunner struct {
	calls   [][]string
	runFunc func(ctx context.Context, dir string, args ...string) (string, error)
}

func (m *mockGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	m.calls = append(m.calls, args)
	if m.runFunc != nil {
		return m.runFunc(ctx, dir, args...)
	}
	return "", nil
}

func TestScheduler_PublishOnSuccessfulCycle(t *testing.T) {
	// Mock HTTP source server returning empty list (successful fetch, 0 candidates)
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(""))
	}))
	defer sourceSrv.Close()

	// Mock Target server
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok response"))
	}))
	defer targetSrv.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    targetSrv.URL,
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  2,
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: tmpDir,
			Branch:     "main",
			RemoteURL:  "git@github.com:example/gemsub-subscriptions.git",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	var publishCount int32
	mockGit := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 {
				switch args[0] {
				case "rev-parse":
					return "true\n", nil
				case "config":
					return "git@github.com:example/gemsub-subscriptions.git\n", nil
				case "add":
					atomic.AddInt32(&publishCount, 1)
					return "", nil
				case "diff":
					return "all.txt\n", nil
				case "commit", "push":
					return "", nil
				}
			}
			return "", nil
		},
	}

	pub := publisher.NewWithGit(&cfg.Publishing, st, mockGit)
	sched := scheduler.New(cfg, st)
	sched.SetPublisher(pub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Trigger 1 cycle by running with a canceled context after initial cycle completes
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	sched.Run(ctx)

	if atomic.LoadInt32(&publishCount) == 0 {
		t.Errorf("expected publisher to be invoked after successful cycle")
	}
}

func TestScheduler_CancelledCycleDoesNotPublish(t *testing.T) {
	// Source server that hangs to trigger cancellation during cycle
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		_, _ = w.Write([]byte("vless://user@127.0.0.1:443\n"))
	}))
	defer sourceSrv.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    "https://gemini.google.com/",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  2,
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: tmpDir,
			Branch:     "main",
			RemoteURL:  "git@github.com:example/gemsub-subscriptions.git",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	var publishCount int32
	mockGit := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "add" {
				atomic.AddInt32(&publishCount, 1)
			}
			return "", fmt.Errorf("unexpected git call: %v", args)
		},
	}

	pub := publisher.NewWithGit(&cfg.Publishing, st, mockGit)
	sched := scheduler.New(cfg, st)
	sched.SetPublisher(pub)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately before / during cycle
	cancel()

	sched.Run(ctx)

	if atomic.LoadInt32(&publishCount) != 0 {
		t.Errorf("publisher must NOT be called on cancelled cycle")
	}
}

func TestScheduler_FetchFailurePreservesStoreAndSkipsPublishing(t *testing.T) {
	// Source server that returns HTTP 500 to trigger fetch errors
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sourceSrv.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Pre-populate store with a previously verified passing link
	const existingLink = "vless://user@10.0.0.1:443"
	st.PutWithTransition(store.Result{
		Link:     existingLink,
		Status:   store.StatusPassed,
		Reason:   "ok",
		TestedAt: time.Now(),
	})
	st.FinishCycle()

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    "https://gemini.google.com/",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  2,
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: tmpDir,
			Branch:     "main",
			RemoteURL:  "git@github.com:example/gemsub-subscriptions.git",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	var publishCount int32
	mockGit := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "add" {
				atomic.AddInt32(&publishCount, 1)
			}
			return "", nil
		},
	}

	pub := publisher.NewWithGit(&cfg.Publishing, st, mockGit)
	sched := scheduler.New(cfg, st)
	sched.SetPublisher(pub)

	ctx, cancel := context.WithCancel(context.Background())
	// Stop after first cycle runs
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	sched.Run(ctx)

	// 1. Publishing must not be called
	if atomic.LoadInt32(&publishCount) != 0 {
		t.Errorf("publisher must NOT be called when fetch fails")
	}

	// 2. Existing passing link must be preserved in store
	passing := st.Passing()
	if len(passing) != 1 || passing[0] != existingLink {
		t.Errorf("expected existing passing link %q to be preserved, got %v", existingLink, passing)
	}

	stats := st.Stats()
	if stats.Passed != 1 || stats.Servable != 1 {
		t.Errorf("expected stats to preserve 1 passed and 1 servable, got %+v", stats)
	}
}

func TestScheduler_EmitsLifecycleAndProgressEvents(t *testing.T) {
	// Mock HTTP source returning 2 candidates
	linksData := "vless://11111111-1111-1111-1111-111111111111@127.0.0.1:443?type=tcp&security=none#node1\nvless://22222222-2222-2222-2222-222222222222@127.0.0.1:443?type=tcp&security=none#node2\n"
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(linksData))
	}))
	defer sourceSrv.Close()

	// Target server
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer targetSrv.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    targetSrv.URL,
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
	subCh := bus.Subscribe(20)
	defer bus.Unsubscribe(subCh)

	sched := scheduler.New(cfg, st, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		// Stop after the cycle has finished
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	sched.Run(ctx)

	var cycleStartedCount, candidatesLoadedCount, probeCompletedCount, cycleFinishedCount int
	var receivedOrder []string

	drainTimeout := time.After(1 * time.Second)
	collecting := true

	for collecting {
		select {
		case evt, ok := <-subCh:
			if !ok {
				collecting = false
				break
			}
			switch e := evt.(type) {
			case events.CycleStarted:
				cycleStartedCount++
				receivedOrder = append(receivedOrder, "CycleStarted")
				if e.StartedAt.IsZero() {
					t.Errorf("expected non-zero StartedAt")
				}
			case events.CandidatesLoaded:
				candidatesLoadedCount++
				receivedOrder = append(receivedOrder, "CandidatesLoaded")
				if e.Total != 2 {
					t.Errorf("expected CandidatesLoaded Total=2, got %d", e.Total)
				}
			case events.ProbeCompleted:
				probeCompletedCount++
				receivedOrder = append(receivedOrder, "ProbeCompleted")
				if e.Total != 2 {
					t.Errorf("expected ProbeCompleted Total=2, got %d", e.Total)
				}
				if e.Completed < 1 || e.Completed > 2 {
					t.Errorf("invalid ProbeCompleted Completed=%d", e.Completed)
				}
			case events.CycleFinished:
				cycleFinishedCount++
				receivedOrder = append(receivedOrder, "CycleFinished")
				if e.Cancelled {
					t.Errorf("expected CycleFinished Cancelled=false")
				}
				if e.Total != 2 || e.Completed != 2 {
					t.Errorf("expected CycleFinished Total=2, Completed=2, got Total=%d, Completed=%d", e.Total, e.Completed)
				}
				collecting = false
			default:
				t.Fatalf("unexpected event type: %T", evt)
			}
		case <-drainTimeout:
			collecting = false
		}
	}

	if cycleStartedCount != 1 {
		t.Errorf("expected 1 CycleStarted, got %d", cycleStartedCount)
	}
	if candidatesLoadedCount != 1 {
		t.Errorf("expected 1 CandidatesLoaded, got %d", candidatesLoadedCount)
	}
	if probeCompletedCount != 2 {
		t.Errorf("expected 2 ProbeCompleted, got %d", probeCompletedCount)
	}
	if cycleFinishedCount != 1 {
		t.Errorf("expected 1 CycleFinished, got %d", cycleFinishedCount)
	}

	// Verify event order: CycleStarted, CandidatesLoaded, ProbeCompleted*, CycleFinished
	if len(receivedOrder) != 5 {
		t.Fatalf("expected 5 events in total, got %d: %v", len(receivedOrder), receivedOrder)
	}
	if receivedOrder[0] != "CycleStarted" {
		t.Errorf("event 0: expected CycleStarted, got %s", receivedOrder[0])
	}
	if receivedOrder[1] != "CandidatesLoaded" {
		t.Errorf("event 1: expected CandidatesLoaded, got %s", receivedOrder[1])
	}
	if receivedOrder[2] != "ProbeCompleted" || receivedOrder[3] != "ProbeCompleted" {
		t.Errorf("events 2 & 3: expected ProbeCompleted, got %s and %s", receivedOrder[2], receivedOrder[3])
	}
	if receivedOrder[4] != "CycleFinished" {
		t.Errorf("event 4: expected CycleFinished, got %s", receivedOrder[4])
	}
}

func TestScheduler_CancelledCycleEmitsCycleFinishedWithCancelledTrue(t *testing.T) {
	// Source hangs
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		_, _ = w.Write([]byte("vless://user@127.0.0.1:443\n"))
	}))
	defer sourceSrv.Close()

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    "https://gemini.google.com/",
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
	subCh := bus.Subscribe(10)
	defer bus.Unsubscribe(subCh)

	sched := scheduler.New(cfg, st, bus)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	sched.Run(ctx)

	var cycleFinished *events.CycleFinished
	drainTimeout := time.After(500 * time.Millisecond)

Loop:
	for {
		select {
		case evt, ok := <-subCh:
			if !ok {
				break Loop
			}
			if cf, ok := evt.(events.CycleFinished); ok {
				cycleFinished = &cf
				break Loop
			}
		case <-drainTimeout:
			break Loop
		}
	}

	if cycleFinished == nil {
		t.Fatal("expected CycleFinished event on cancelled cycle")
	}
	if !cycleFinished.Cancelled {
		t.Errorf("expected CycleFinished Cancelled=true, got false")
	}
}
