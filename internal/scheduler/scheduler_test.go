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

