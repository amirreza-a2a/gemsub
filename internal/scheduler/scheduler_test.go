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
	"gemsub/internal/tester"
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

func setupTestSchedulerWithSources(t *testing.T, sourceURLs []string, probeLimit int) (*scheduler.Scheduler, *store.Store, *config.Config) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.Config{
		Sources:          sourceURLs,
		FetchIntervalRaw: "1h",
		ProbeLimit:       probeLimit,
		Test: config.TestConfig{
			TargetURL:    "https://gemini.google.com/",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  1,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	sched := scheduler.New(cfg, st)
	return sched, st, cfg
}

func TestScheduler_Rotation_ProbeLimitZeroAndLarger(t *testing.T) {
	links := []string{
		"vless://user@host1.com:443#A",
		"vless://user@host2.com:443#B",
		"vless://user@host3.com:443#C",
	}
	sourcePayload := fmt.Sprintf("%s\n%s\n%s\n", links[0], links[1], links[2])
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sourcePayload))
	}))
	defer sourceSrv.Close()

	// 1. ProbeLimit = 0: All 3 candidates probed
	sched0, _, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 0)
	var probed0 []string
	var mu0 sync.Mutex
	runner0 := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		mu0.Lock()
		probed0 = append(probed0, cand.Link)
		mu0.Unlock()
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	}
	sched0.RunCycleForTest(context.Background(), runner0)
	if len(probed0) != 3 {
		t.Fatalf("expected 3 candidates probed when ProbeLimit=0, got %d", len(probed0))
	}

	// 2. ProbeLimit = 10 (larger than candidate count 3): All 3 candidates probed
	sched10, _, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 10)
	var probed10 []string
	var mu10 sync.Mutex
	runner10 := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		mu10.Lock()
		probed10 = append(probed10, cand.Link)
		mu10.Unlock()
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	}
	sched10.RunCycleForTest(context.Background(), runner10)
	if len(probed10) != 3 {
		t.Fatalf("expected 3 candidates probed when ProbeLimit=10, got %d", len(probed10))
	}
}

func TestScheduler_Rotation_FairRotation_6Candidates_K2(t *testing.T) {
	links := []string{
		"vless://user@host1.com:443#A",
		"vless://user@host2.com:443#B",
		"vless://user@host3.com:443#C",
		"vless://user@host4.com:443#D",
		"vless://user@host5.com:443#E",
		"vless://user@host6.com:443#F",
	}
	sourcePayload := ""
	for _, l := range links {
		sourcePayload += l + "\n"
	}
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sourcePayload))
	}))
	defer sourceSrv.Close()

	sched, _, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 2)

	baseTime := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	cycleCounter := 0

	for cycle := 1; cycle <= 4; cycle++ {
		cycleCounter++
		var cycleProbed []string
		var mu sync.Mutex

		runner := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
			mu.Lock()
			cycleProbed = append(cycleProbed, cand.Link)
			mu.Unlock()
			return store.Result{
				Link:     cand.Link,
				Status:   store.StatusPassed,
				TestedAt: baseTime.Add(time.Duration(cycleCounter) * time.Minute),
			}
		}

		sched.RunCycleForTest(context.Background(), runner)

		if len(cycleProbed) != 2 {
			t.Fatalf("cycle %d: expected 2 probes, got %d", cycle, len(cycleProbed))
		}

		switch cycle {
		case 1:
			if cycleProbed[0] != links[0] || cycleProbed[1] != links[1] {
				t.Errorf("cycle 1: expected [A, B], got %v", cycleProbed)
			}
		case 2:
			if cycleProbed[0] != links[2] || cycleProbed[1] != links[3] {
				t.Errorf("cycle 2: expected [C, D], got %v", cycleProbed)
			}
		case 3:
			if cycleProbed[0] != links[4] || cycleProbed[1] != links[5] {
				t.Errorf("cycle 3: expected [E, F], got %v", cycleProbed)
			}
		case 4:
			// Full round completed; epoch 2 starts with A, B
			if cycleProbed[0] != links[0] || cycleProbed[1] != links[1] {
				t.Errorf("cycle 4: expected [A, B], got %v", cycleProbed)
			}
		}
	}
}

func TestScheduler_Rotation_NewCandidatePriority(t *testing.T) {
	var currentLinks []string
	var linksMu sync.Mutex

	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		linksMu.Lock()
		payload := ""
		for _, l := range currentLinks {
			payload += l + "\n"
		}
		linksMu.Unlock()
		_, _ = w.Write([]byte(payload))
	}))
	defer sourceSrv.Close()

	candA := "vless://user@host1.com:443#A"
	candB := "vless://user@host2.com:443#B"
	candC := "vless://user@host3.com:443#C"
	candD := "vless://user@host4.com:443#D"
	candX := "vless://user@host9.com:443#X"

	linksMu.Lock()
	currentLinks = []string{candA, candB, candC, candD}
	linksMu.Unlock()

	sched, _, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 2)
	baseTime := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	// Cycle 1: Probes A, B
	var probedCycle1 []string
	runner1 := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		probedCycle1 = append(probedCycle1, cand.Link)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: baseTime.Add(1 * time.Minute)}
	}
	sched.RunCycleForTest(context.Background(), runner1)
	if len(probedCycle1) != 2 || probedCycle1[0] != candA || probedCycle1[1] != candB {
		t.Fatalf("cycle 1: expected [A, B], got %v", probedCycle1)
	}

	// Cycle 2: Introduce new candidate X. Population: A, B, C, D, X.
	// Untested candidates: C, D, X.
	// With ProbeLimit 2, 2 of the untested candidates must be selected; tested A and B must NOT be selected.
	linksMu.Lock()
	currentLinks = []string{candA, candB, candC, candD, candX}
	linksMu.Unlock()

	var probedCycle2 []string
	runner2 := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		probedCycle2 = append(probedCycle2, cand.Link)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: baseTime.Add(2 * time.Minute)}
	}
	sched.RunCycleForTest(context.Background(), runner2)
	if len(probedCycle2) != 2 {
		t.Fatalf("cycle 2: expected 2 probes, got %d", len(probedCycle2))
	}
	for _, p := range probedCycle2 {
		if p == candA || p == candB {
			t.Errorf("cycle 2: candidate %s was tested in cycle 1 but selected ahead of untested candidates", p)
		}
	}

	// Cycle 3: The remaining untested candidate from {C, D, X} MUST be selected!
	var probedCycle3 []string
	runner3 := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		probedCycle3 = append(probedCycle3, cand.Link)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: baseTime.Add(3 * time.Minute)}
	}
	sched.RunCycleForTest(context.Background(), runner3)
	if len(probedCycle3) != 2 {
		t.Fatalf("cycle 3: expected 2 probes, got %d", len(probedCycle3))
	}

	allTestedSoFar := append(probedCycle1, probedCycle2...)
	allTestedSoFar = append(allTestedSoFar, probedCycle3...)
	testedSet := make(map[string]bool)
	for _, p := range allTestedSoFar {
		testedSet[p] = true
	}

	// Every candidate A, B, C, D, X must have been tested across the 3 cycles
	for _, c := range []string{candA, candB, candC, candD, candX} {
		if !testedSet[c] {
			t.Errorf("candidate %s was not tested across 3 cycles", c)
		}
	}
}

func TestScheduler_Rotation_CandidateRemoval(t *testing.T) {
	var currentLinks []string
	var linksMu sync.Mutex

	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		linksMu.Lock()
		payload := ""
		for _, l := range currentLinks {
			payload += l + "\n"
		}
		linksMu.Unlock()
		_, _ = w.Write([]byte(payload))
	}))
	defer sourceSrv.Close()

	candA := "vless://user@host1.com:443#A"
	candB := "vless://user@host2.com:443#B"
	candC := "vless://user@host3.com:443#C"
	candD := "vless://user@host4.com:443#D"

	linksMu.Lock()
	currentLinks = []string{candA, candB, candC, candD}
	linksMu.Unlock()

	sched, _, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 2)
	baseTime := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	// Cycle 1: Probes A, B
	sched.RunCycleForTest(context.Background(), func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: baseTime.Add(1 * time.Minute)}
	})

	// Remove C. Current links: A, B, D.
	linksMu.Lock()
	currentLinks = []string{candA, candB, candD}
	linksMu.Unlock()

	// Cycle 2: D is untested (time 0) -> first. A is oldest tested (1m) -> second.
	var probedCycle2 []string
	sched.RunCycleForTest(context.Background(), func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		probedCycle2 = append(probedCycle2, cand.Link)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: baseTime.Add(2 * time.Minute)}
	})

	if len(probedCycle2) != 2 || probedCycle2[0] != candD || probedCycle2[1] != candA {
		t.Fatalf("cycle 2: expected [D, A], got %v", probedCycle2)
	}
}

func TestScheduler_Rotation_FeedReorderingInvariance(t *testing.T) {
	var currentLinks []string
	var linksMu sync.Mutex

	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		linksMu.Lock()
		payload := ""
		for _, l := range currentLinks {
			payload += l + "\n"
		}
		linksMu.Unlock()
		_, _ = w.Write([]byte(payload))
	}))
	defer sourceSrv.Close()

	candA := "vless://user@host1.com:443#A"
	candB := "vless://user@host2.com:443#B"
	candC := "vless://user@host3.com:443#C"
	candD := "vless://user@host4.com:443#D"

	linksMu.Lock()
	currentLinks = []string{candA, candB, candC, candD}
	linksMu.Unlock()

	sched, _, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 2)
	baseTime := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	// Cycle 1: Probes A, B
	sched.RunCycleForTest(context.Background(), func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: baseTime.Add(1 * time.Minute)}
	})

	// Cycle 2: Reverse feed order to [D, C, B, A]
	linksMu.Lock()
	currentLinks = []string{candD, candC, candB, candA}
	linksMu.Unlock()

	var probedCycle2 []string
	sched.RunCycleForTest(context.Background(), func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		probedCycle2 = append(probedCycle2, cand.Link)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: baseTime.Add(2 * time.Minute)}
	})

	// C and D are untested, so they must be chosen regardless of feed reordering!
	if len(probedCycle2) != 2 || probedCycle2[0] != candC || probedCycle2[1] != candD {
		t.Fatalf("cycle 2: expected [C, D], got %v", probedCycle2)
	}
}

func collectCandidatesLoaded(subCh <-chan any, timeout time.Duration) []string {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case evt, ok := <-subCh:
			if !ok {
				return nil
			}
			if cl, ok := evt.(events.CandidatesLoaded); ok {
				var links []string
				for _, c := range cl.Candidates {
					links = append(links, c.Link)
				}
				return links
			}
		case <-timer.C:
			return nil
		}
	}
}

func TestScheduler_Rotation_InFlightCancellationPreservesUntestedPriority(t *testing.T) {
	candA := "vless://user@host1.com:443#A"
	candB := "vless://user@host2.com:443#B"
	candC := "vless://user@host3.com:443#C"
	candD := "vless://user@host4.com:443#D"

	sourcePayload := fmt.Sprintf("%s\n%s\n%s\n%s\n", candA, candB, candC, candD)
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sourcePayload))
	}))
	defer sourceSrv.Close()

	// Concurrency = 1 so execution is strictly sequential
	sched, _, cfg := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 2)
	cfg.Test.Concurrency = 1

	baseTime := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	subCh1 := sched.EventBus().Subscribe(20)
	defer sched.EventBus().Unsubscribe(subCh1)

	ctx1, cancel1 := context.WithCancel(context.Background())
	candBStarted := make(chan struct{})
	var candBStartedOnce sync.Once

	runner1 := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		if cand.Link == candA {
			// Candidate A completes probe cleanly before any cancellation
			return store.Result{
				Link:     cand.Link,
				Status:   store.StatusPassed,
				TestedAt: baseTime.Add(1 * time.Minute),
			}
		}
		// Candidate B: genuine in-flight cancellation via synchronization primitive
		candBStartedOnce.Do(func() {
			close(candBStarted)
		})
		// Block deterministically until context is genuinely cancelled
		<-ctx.Done()
		return store.Result{
			Link:     cand.Link,
			Status:   store.StatusInconclusive,
			Category: store.ErrTimeout,
			Reason:   ctx.Err().Error(),
		}
	}

	// Coordinator goroutine triggers cancellation once candidate B has started
	go func() {
		<-candBStarted
		cancel1()
	}()

	sched.RunCycleForTest(ctx1, runner1)

	// Verify Invariant: candidate selected != candidate rotation-accounted
	selectedCycle1 := collectCandidatesLoaded(subCh1, 100*time.Millisecond)
	if len(selectedCycle1) != 2 || selectedCycle1[0] != candA || selectedCycle1[1] != candB {
		t.Fatalf("cycle 1: expected selected candidates [A, B], got %v", selectedCycle1)
	}

	// Cycle 2: Fresh context.
	// Candidate B was selected in Cycle 1 but cancelled mid-execution, so it was never rotation-accounted (timestamp 0).
	// Candidate A completed and has timestamp 1m.
	// Candidates C and D were never selected (timestamp 0).
	// Cycle 2 MUST select 2 untested candidates from {B, C, D}, and MUST NOT select A!
	subCh2 := sched.EventBus().Subscribe(20)
	defer sched.EventBus().Unsubscribe(subCh2)

	var probedCycle2 []string
	runner2 := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		probedCycle2 = append(probedCycle2, cand.Link)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: baseTime.Add(2 * time.Minute)}
	}

	sched.RunCycleForTest(context.Background(), runner2)

	selectedCycle2 := collectCandidatesLoaded(subCh2, 100*time.Millisecond)
	if len(selectedCycle2) != 2 {
		t.Fatalf("cycle 2: expected 2 selected candidates, got %d", len(selectedCycle2))
	}
	for _, sel := range selectedCycle2 {
		if sel == candA {
			t.Errorf("cycle 2: candidate A was completed in cycle 1 and must not be selected ahead of uncompleted B/C/D")
		}
	}
	foundB := false
	for _, sel := range selectedCycle2 {
		if sel == candB {
			foundB = true
			break
		}
	}
	if !foundB {
		t.Errorf("cycle 2: candidate B was cancelled in cycle 1 and must be prioritized in cycle 2; selected %v", selectedCycle2)
	}
}

func TestScheduler_Rotation_StorePresencePreserved(t *testing.T) {
	links := []string{
		"vless://user@host1.com:443#A",
		"vless://user@host2.com:443#B",
		"vless://user@host3.com:443#C",
		"vless://user@host4.com:443#D",
		"vless://user@host5.com:443#E",
	}
	sourcePayload := ""
	for _, l := range links {
		sourcePayload += l + "\n"
	}
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sourcePayload))
	}))
	defer sourceSrv.Close()

	// ProbeLimit = 2: Only 2 candidates probed; 3 candidates skipped
	sched, st, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 2)

	// Pre-seed all 5 candidates in Store as previously passing
	for _, l := range links {
		st.PutWithTransition(store.Result{
			Link:     l,
			Status:   store.StatusPassed,
			TestedAt: time.Now().Add(-10 * time.Minute),
		})
	}
	st.FinishCycle()

	if st.Stats().Total != 5 {
		t.Fatalf("pre-condition: expected 5 candidates seeded, got %d", st.Stats().Total)
	}

	runner := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	}

	sched.RunCycleForTest(context.Background(), runner)

	// Verify all 5 candidates still exist in Store and skipped candidates have AbsentCycles == 0
	stats := st.Stats()
	if stats.Total != 5 {
		t.Fatalf("expected 5 total candidates preserved in Store, got %d", stats.Total)
	}

	for _, l := range links {
		rec, ok := st.GetRecord(l)
		if !ok {
			t.Fatalf("expected candidate %s in Store", l)
		}
		if rec.AbsentCycles != 0 {
			t.Errorf("expected AbsentCycles=0 for %s, got %d", l, rec.AbsentCycles)
		}
		if rec.Latest.Status != store.StatusPassed {
			t.Errorf("expected candidate %s to retain StatusPassed, got %v", l, rec.Latest.Status)
		}
	}
	if len(st.Passing()) != 5 {
		t.Fatalf("expected all 5 candidates to remain in Passing projection, got %d", len(st.Passing()))
	}
}

func TestScheduler_Rotation_Determinism(t *testing.T) {
	var links []string
	sourcePayload := ""
	for i := 0; i < 20; i++ {
		l := fmt.Sprintf("vless://user@host%d.com:443#Node%02d", i, i)
		links = append(links, l)
		sourcePayload += l + "\n"
	}
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sourcePayload))
	}))
	defer sourceSrv.Close()

	sched1, _, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 5)
	sched2, _, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 5)

	var probed1, probed2 []string
	runner1 := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		probed1 = append(probed1, cand.Link)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	}
	runner2 := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		probed2 = append(probed2, cand.Link)
		return store.Result{Link: cand.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	}

	sched1.RunCycleForTest(context.Background(), runner1)
	sched2.RunCycleForTest(context.Background(), runner2)

	if len(probed1) != len(probed2) {
		t.Fatalf("probed length mismatch: %d vs %d", len(probed1), len(probed2))
	}
	for i := range probed1 {
		if probed1[i] != probed2[i] {
			t.Errorf("determinism mismatch at %d: %s vs %s", i, probed1[i], probed2[i])
		}
	}
}

func TestScheduler_Rotation_OnResultContract_CompletedProbeService(t *testing.T) {
	candA, _ := parser.Parse("vless://user@host1.com:443#A")
	candB, _ := parser.Parse("vless://user@host2.com:443#B")
	candC, _ := parser.Parse("vless://user@host3.com:443#C")
	candidates := []parser.Candidate{candA, candB, candC}

	cfg := &config.TestConfig{
		Concurrency: 1,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var onResultCalls []string
	var onResultMu sync.Mutex
	onResult := func(r store.Result) {
		onResultMu.Lock()
		onResultCalls = append(onResultCalls, r.Link)
		onResultMu.Unlock()
	}

	candBStarted := make(chan struct{})
	runner := func(ctx context.Context, c parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		if c.Link == candA.Link {
			// CandA finishes cleanly
			return store.Result{Link: c.Link, Status: store.StatusPassed, TestedAt: time.Now()}
		}
		if c.Link == candB.Link {
			close(candBStarted)
			<-ctx.Done()
			return store.Result{Link: c.Link, Status: store.StatusFailed}
		}
		return store.Result{Link: c.Link, Status: store.StatusPassed, TestedAt: time.Now()}
	}

	go func() {
		<-candBStarted
		cancel()
	}()

	completed := tester.RunPoolWithRunner(ctx, candidates, cfg, runner, nil, onResult)
	if completed {
		t.Errorf("expected RunPoolWithRunner to return false on cancellation, got true")
	}

	onResultMu.Lock()
	defer onResultMu.Unlock()
	// Only Candidate A completed cleanly; Candidate B was cancelled in-flight; Candidate C was never dispatched.
	if len(onResultCalls) != 1 || onResultCalls[0] != candA.Link {
		t.Fatalf("expected onResult to be invoked exactly once for candA, got %v", onResultCalls)
	}
}

func TestScheduler_Rotator_ConcurrentInitializationSafe(t *testing.T) {
	// Directly construct an uninitialized Scheduler (e.g. bypass New)
	s := &scheduler.Scheduler{}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	rotators := make([]any, goroutines)

	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			rotators[idx] = s.GetRotatorForTest()
		}()
	}
	wg.Wait()

	first := rotators[0]
	if first == nil {
		t.Fatal("expected non-nil rotator")
	}
	for i := 1; i < goroutines; i++ {
		if rotators[i] != first {
			t.Fatalf("goroutine %d got different rotator pointer: %p vs %p", i, rotators[i], first)
		}
	}
}

func TestScheduler_Rotation_Fairness_N7_K3_StorePreserved(t *testing.T) {
	var links []string
	for i := 1; i <= 7; i++ {
		links = append(links, fmt.Sprintf("vless://user@host%d.com:443#Node%d", i, i))
	}
	sourcePayload := ""
	for _, l := range links {
		sourcePayload += l + "\n"
	}
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sourcePayload))
	}))
	defer sourceSrv.Close()

	// ProbeLimit = 3, N = 7. Ceil(7/3) = 3 cycles per epoch.
	// We run 6 cycles (2 full epochs).
	sched, st, _ := setupTestSchedulerWithSources(t, []string{sourceSrv.URL}, 3)

	baseTime := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	cycleNum := 0

	probedCounts := make(map[string]int, len(links))

	for cycle := 1; cycle <= 6; cycle++ {
		cycleNum++
		var cycleProbed []string

		runner := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
			cycleProbed = append(cycleProbed, cand.Link)
			probedCounts[cand.Link]++
			return store.Result{
				Link:     cand.Link,
				Status:   store.StatusPassed,
				TestedAt: baseTime.Add(time.Duration(cycleNum) * time.Minute),
			}
		}

		sched.RunCycleForTest(context.Background(), runner)

		if len(cycleProbed) != 3 {
			t.Fatalf("cycle %d: expected 3 probed candidates, got %d", cycle, len(cycleProbed))
		}

		// Verify Store presence: all probed candidates have AbsentCycles == 0
		stats := st.Stats()
		if stats.Total < len(links) && cycle == 3 {
			if stats.Total != 7 {
				t.Errorf("cycle 3 (end of epoch 1): expected 7 total candidates in Store, got %d", stats.Total)
			}
		}
	}

	// At end of 6 cycles (2 epochs, 18 probe slots over 7 candidates):
	// Floor(18/7) = 2, Ceil(18/7) = 3.
	// Every candidate must be probed either 2 or 3 times!
	for _, l := range links {
		count := probedCounts[l]
		if count < 2 || count > 3 {
			t.Errorf("candidate %s probed %d times; expected 2 or 3 times", l, count)
		}
	}

	// In Store, all 7 candidates must be present with AbsentCycles == 0
	for _, l := range links {
		rec, ok := st.GetRecord(l)
		if !ok {
			t.Fatalf("expected candidate %s in Store", l)
		}
		if rec.AbsentCycles != 0 {
			t.Errorf("expected AbsentCycles=0 for %s, got %d", l, rec.AbsentCycles)
		}
	}
}
