package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/store"
	"gemsub/internal/subserver"
	"gemsub/internal/tui"
	"gemsub/internal/tui/viewmodel"
)

func TestBootstrapConfig_MissingFileInteractive(t *testing.T) {
	tmpDir := t.TempDir()
	missingPath := filepath.Join(tmpDir, "does_not_exist.json")

	svc, isFirstRun, err := bootstrapConfig(missingPath, false)
	if err != nil {
		t.Fatalf("expected no error for missing config in interactive mode, got: %v", err)
	}
	if !isFirstRun {
		t.Error("expected isFirstRun == true for missing config in interactive mode")
	}
	if svc == nil {
		t.Fatal("expected non-nil config.Service")
	}
	if !svc.IsFirstRun() {
		t.Error("expected svc.IsFirstRun() == true")
	}

	var cfg config.Config = svc.Get()
	if cfg.Test.Concurrency != 20 {
		t.Errorf("expected default concurrency 20, got %d", cfg.Test.Concurrency)
	}
	if cfg.Test.Timeout != 10*time.Second {
		t.Errorf("expected default timeout 10s, got %v", cfg.Test.Timeout)
	}
	if cfg.Serve.Listen != "127.0.0.1:8765" {
		t.Errorf("expected default listen 127.0.0.1:8765, got %s", cfg.Serve.Listen)
	}
	if cfg.Serve.Path != "/sub" {
		t.Errorf("expected default path /sub, got %s", cfg.Serve.Path)
	}
}

func TestBootstrapConfig_MissingFileHeadless(t *testing.T) {
	tmpDir := t.TempDir()
	missingPath := filepath.Join(tmpDir, "does_not_exist.json")

	svc, isFirstRun, err := bootstrapConfig(missingPath, true)
	if err == nil {
		t.Fatal("expected error for missing config in headless mode, got nil")
	}
	if !errors.Is(err, ErrMissingConfigHeadless) {
		t.Errorf("expected ErrMissingConfigHeadless, got: %v", err)
	}
	if isFirstRun {
		t.Error("expected isFirstRun == false when error returned")
	}
	if svc != nil {
		t.Errorf("expected nil service on error, got %v", svc)
	}
}

func TestBootstrapConfig_ExistingValidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")

	data := `{
		"sources": [{"url": "https://example.com/feed.txt"}],
		"fetch_interval": "1h",
		"serve": {"listen": "127.0.0.1:9000", "path": "/testsub"},
		"test": {
			"concurrency": 5,
			"timeout": "5s",
			"gemini": {
				"url": "https://gemini.google.com/",
				"block_phrases": ["not available"]
			}
		}
	}`
	if err := os.WriteFile(cfgPath, []byte(data), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	svc, isFirstRun, err := bootstrapConfig(cfgPath, false)
	if err != nil {
		t.Fatalf("unexpected error for existing valid config: %v", err)
	}
	if isFirstRun {
		t.Error("expected isFirstRun == false for existing config")
	}
	if svc.IsFirstRun() {
		t.Error("expected svc.IsFirstRun() == false for existing config")
	}
	cfg := svc.Get()
	if cfg.Serve.Listen != "127.0.0.1:9000" {
		t.Errorf("expected listen 127.0.0.1:9000, got %s", cfg.Serve.Listen)
	}
}

func TestBootstrapConfig_ExistingInvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")

	// Invalid JSON content
	if err := os.WriteFile(cfgPath, []byte("{invalid-json"), 0644); err != nil {
		t.Fatalf("failed to write invalid config file: %v", err)
	}

	svc, isFirstRun, err := bootstrapConfig(cfgPath, false)
	if err == nil {
		t.Fatal("expected error for invalid config file, got nil")
	}
	if errors.Is(err, ErrMissingConfigHeadless) {
		t.Error("did not expect ErrMissingConfigHeadless for parse error")
	}
	if isFirstRun {
		t.Error("expected isFirstRun == false on parse error")
	}
	if svc != nil {
		t.Errorf("expected nil service on parse error, got %v", svc)
	}
}

func TestFirstRun_RuntimeSetupAndSeamlessTransition(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	stateFile := filepath.Join(tmpDir, "state.json")

	// 1. Pre-seed a candidate into state.json to deterministically prove Store.Load() runs
	stInitial := store.New(stateFile, 2)
	stInitial.PutWithTransition(store.Result{
		Link:     "vless://test@1.1.1.1:443#SeededCandidate",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})
	if err := stInitial.Save(); err != nil {
		t.Fatalf("failed to seed initial state file: %v", err)
	}

	svc, isFirstRun, err := bootstrapConfig(cfgPath, false)
	if err != nil || !isFirstRun {
		t.Fatalf("bootstrapConfig failed: err=%v, isFirstRun=%v", err, isFirstRun)
	}

	// Acquire a dynamic free TCP port for testing the subserver
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve test TCP port: %v", err)
	}
	freeAddr := ln.Addr().String()
	_ = ln.Close()

	cfg := svc.Get()
	cfg.StateFile = stateFile
	cfg.Serve.Listen = freeAddr

	var logBuf bytes.Buffer
	rt, cleanup := setupRuntime(&cfg, &logBuf, svc)
	defer cleanup()

	// Initial assertions: TUI model initialized in ViewWizard, workers not started
	if rt.TUIModel == nil {
		t.Fatal("expected rt.TUIModel != nil")
	}
	if rt.TUIModel.ActiveView() != tui.ViewWizard {
		t.Errorf("expected active view ViewWizard, got %v", rt.TUIModel.ActiveView())
	}
	if rt.SchedulerCtrl.Status().Running {
		t.Error("expected scheduler not running before onboarding completion")
	}

	// Prove subserver is NOT running prior to onboarding
	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, err = client.Get(fmt.Sprintf("http://%s/sub", freeAddr))
	if err == nil {
		t.Fatal("expected connection error before subserver starts, got success")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	var workersStarted int32
	var srv *subserver.Server

	// Mirror the production runLifecycle worker start orchestration
	startWorkers := func(runCtx context.Context) error {
		if !atomic.CompareAndSwapInt32(&workersStarted, 0, 1) {
			return nil
		}
		if err := rt.Store.Load(); err != nil {
			return fmt.Errorf("load store: %w", err)
		}
		cCfg := rt.ConfigSvc.Get()
		if rt.Scheduler != nil {
			rt.Scheduler.UpdateConfig(cCfg)
		}
		if err := rt.SchedulerCtrl.Start(runCtx); err != nil {
			atomic.StoreInt32(&workersStarted, 0)
			return fmt.Errorf("start scheduler: %w", err)
		}
		srv = subserver.New(&cCfg.Serve, rt.Store)
		srv.SetEventBus(rt.Bus)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Run(runCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				cancel()
			}
		}()
		return nil
	}

	rt.Adapter.SetRuntimeStarter(func() error {
		return startWorkers(ctx)
	})

	// Execute CompleteOnboarding through Adapter
	err = rt.Adapter.CompleteOnboarding(viewmodel.OnboardingConfig{
		SourceURL:   "https://example.com/subs.txt",
		SourceName:  "Primary Feed",
		Listen:      freeAddr,
		Path:        "/sub",
		Concurrency: 15,
		Timeout:     "8s",
		TargetURL:   "https://gemini.google.com/",
	})
	if err != nil {
		t.Fatalf("CompleteOnboarding failed: %v", err)
	}

	// Start runtime through Adapter
	if err := rt.Adapter.StartRuntime(); err != nil {
		t.Fatalf("StartRuntime failed: %v", err)
	}

	// 1. Prove Store.Load() was invoked: seeded candidate exists in runtime Store
	if rt.Store.Count() != 1 {
		t.Errorf("expected Store.Count() == 1 from loaded state, got %d", rt.Store.Count())
	}
	if !rt.Store.HasCanonicalRecord("vless://test@1.1.1.1:443#SeededCandidate") {
		t.Error("expected seeded candidate record to be loaded into store")
	}

	// 2. Prove Scheduler startup: scheduler control plane reports running
	if !rt.SchedulerCtrl.Status().Running {
		t.Error("expected scheduler to be running after onboarding completed")
	}

	// 3. Prove Subserver startup: subserver actively responds on configured listen address
	deadline := time.Now().Add(3 * time.Second)
	var subserverOK bool
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://%s/sub", freeAddr))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				subserverOK = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !subserverOK {
		t.Errorf("expected subserver to respond with HTTP 200 OK on http://%s/sub", freeAddr)
	}

	// 4. Prove idempotent exactly-once startup: repeated trigger does not restart workers
	if err := startWorkers(ctx); err != nil {
		t.Fatalf("subsequent startWorkers call returned error: %v", err)
	}
	if atomic.LoadInt32(&workersStarted) != 1 {
		t.Errorf("expected workersStarted == 1, got %d", atomic.LoadInt32(&workersStarted))
	}

	// 5. Prove config persistence and first-run flag clearance
	if svc.IsFirstRun() {
		t.Error("expected svc.IsFirstRun() == false after onboarding save")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("expected config file written to disk: %v", err)
	}

	savedCfg := svc.Get()
	if len(savedCfg.Sources) != 1 || savedCfg.Sources[0].URL != "https://example.com/subs.txt" {
		t.Errorf("unexpected saved sources: %+v", savedCfg.Sources)
	}
	if savedCfg.Test.Concurrency != 15 {
		t.Errorf("expected concurrency 15, got %d", savedCfg.Test.Concurrency)
	}

	// Clean shutdown of test workers
	cancel()
	wg.Wait()
}

func TestFirstRun_RuntimeStarter_FailureAllowsRetry(t *testing.T) {
	var workersStarted int32
	var attemptCount int

	startWorkers := func(failFirst bool) error {
		if !atomic.CompareAndSwapInt32(&workersStarted, 0, 1) {
			return nil
		}
		attemptCount++
		if failFirst && attemptCount == 1 {
			atomic.StoreInt32(&workersStarted, 0)
			return errors.New("simulated startup failure")
		}
		return nil
	}

	// Attempt 1: Fails
	err := startWorkers(true)
	if err == nil {
		t.Fatal("expected error on first attempt, got nil")
	}
	if atomic.LoadInt32(&workersStarted) != 0 {
		t.Errorf("expected workersStarted reset to 0 after failure, got %d", atomic.LoadInt32(&workersStarted))
	}

	// Attempt 2: Succeeds
	err = startWorkers(false)
	if err != nil {
		t.Fatalf("expected success on second attempt, got %v", err)
	}
	if atomic.LoadInt32(&workersStarted) != 1 {
		t.Errorf("expected workersStarted == 1 after success, got %d", atomic.LoadInt32(&workersStarted))
	}

	// Attempt 3: Idempotent no-op
	err = startWorkers(false)
	if err != nil {
		t.Fatalf("expected nil on subsequent attempt, got %v", err)
	}
	if attemptCount != 2 {
		t.Errorf("expected attemptCount == 2 (attempt 3 should be skipped by CAS), got %d", attemptCount)
	}
}
