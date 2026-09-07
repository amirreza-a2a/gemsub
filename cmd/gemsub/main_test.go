package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/store"
	"gemsub/internal/tui/country"
)

func TestHeadlessIsolation_ZeroTUIComponents(t *testing.T) {
	origLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	tmpDir := t.TempDir()
	cfg := &config.Config{
		Headless:  true,
		StateFile: filepath.Join(tmpDir, "state.json"),
	}

	var logBuf bytes.Buffer
	rt, cleanup := setupRuntime(cfg, &logBuf)
	defer cleanup()

	// 1. No Bubble Tea program constructed
	if rt.Program != nil {
		t.Errorf("expected rt.Program == nil in headless mode, got %v", rt.Program)
	}

	// 2. No TUI adapter constructed
	if rt.Adapter != nil {
		t.Errorf("expected rt.Adapter == nil in headless mode, got %v", rt.Adapter)
	}

	// 3. No TUI model constructed
	if rt.TUIModel != nil {
		t.Errorf("expected rt.TUIModel == nil in headless mode, got %v", rt.TUIModel)
	}

	// 4. No TUI subscriber registered on EventBus
	if rt.Bus == nil {
		t.Fatal("expected rt.Bus != nil")
	}
	if subs := rt.Bus.SubscriberCount(); subs != 0 {
		t.Errorf("expected 0 subscribers on EventBus in headless mode, got %d", subs)
	}

	// 5. No RingLogHandler constructed
	if rt.RingHandler != nil {
		t.Errorf("expected rt.RingHandler == nil in headless mode, got %v", rt.RingHandler)
	}

	// 6. Standard slog TextHandler used (writes directly to writer, not in-memory ring buffer)
	slog.Info("headless verification record")
	if !bytes.Contains(logBuf.Bytes(), []byte("headless verification record")) {
		t.Errorf("expected log output written directly to logWriter via TextHandler, got: %s", logBuf.String())
	}
}

func TestNonHeadlessInitialization(t *testing.T) {
	origLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	tmpDir := t.TempDir()
	cfg := &config.Config{
		Headless:  false,
		StateFile: filepath.Join(tmpDir, "state.json"),
	}

	var logBuf bytes.Buffer
	rt, cleanup := setupRuntime(cfg, &logBuf)
	defer cleanup()

	// 1. RingLogHandler constructed
	if rt.RingHandler == nil {
		t.Error("expected rt.RingHandler != nil in non-headless mode")
	}

	// 2. TUI adapter constructed and subscribed
	if rt.Adapter == nil {
		t.Error("expected rt.Adapter != nil in non-headless mode")
	}

	// 3. TUI subscriber registered on EventBus before scheduler execution
	if subs := rt.Bus.SubscriberCount(); subs != 1 {
		t.Errorf("expected 1 subscriber on EventBus in non-headless mode, got %d", subs)
	}

	// 4. TUIModel and Program constructed
	if rt.TUIModel == nil {
		t.Error("expected rt.TUIModel != nil in non-headless mode")
	}
	if rt.Program == nil {
		t.Error("expected rt.Program != nil in non-headless mode")
	}
}

// TestShutdownPersistence verifies that the cleanup() callback returned by setupRuntime
// persists in-memory Store state to disk upon application termination.
// Note: This test verifies cleanup persistence directly rather than full OS-signal delivery.
func TestShutdownPersistence(t *testing.T) {
	origLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	cfg := &config.Config{
		Headless:  false,
		StateFile: stateFile,
	}

	var logBuf bytes.Buffer
	rt, cleanup := setupRuntime(cfg, &logBuf)

	// Simulate candidate state mutation in store before shutdown
	rt.Store.PutWithTransition(store.Result{
		Link:     "vless://test@1.1.1.1:443#ShutdownNode",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})

	// Invoke cleanup to simulate application shutdown sequence
	cleanup()

	// Verify that state file on disk contains the transition
	stNew := store.New(stateFile, 2)
	if err := stNew.Load(); err != nil {
		t.Fatalf("failed to load state saved on shutdown: %v", err)
	}
	rec, ok := stNew.GetRecord("vless://test@1.1.1.1:443#ShutdownNode")
	if !ok || rec == nil {
		t.Fatal("expected candidate record to be persisted to disk upon shutdown cleanup")
	}
	if rec.Latest.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed in persisted record, got %s", rec.Latest.Status)
	}
}

// TestLifecycle_ContextCancellationShutdown verifies the full runLifecycle coordination:
// - Starting scheduler and subserver workers
// - Receiving top-level context cancellation
// - Graceful shutdown and worker drain via sync.WaitGroup
// - Invocation of defer cleanup() ensuring final Store.Save() is written to disk
func TestLifecycle_ContextCancellationShutdown(t *testing.T) {
	origLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	// Pre-populate initial store state on disk
	stInitial := store.New(stateFile, 2)
	stInitial.PutWithTransition(store.Result{
		Link:     "vless://test@1.1.1.1:443#LifecycleNode",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})
	if err := stInitial.Save(); err != nil {
		t.Fatalf("failed to seed initial state file: %v", err)
	}

	cfg := &config.Config{
		Headless:      true,
		StateFile:     stateFile,
		FetchInterval: time.Hour,
		Serve: config.ServeConfig{
			Listen: "127.0.0.1:0",
			Path:   "/sub",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	var logBuf bytes.Buffer

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- runLifecycle(ctx, cfg, &logBuf)
	}()

	// Allow workers to start
	time.Sleep(50 * time.Millisecond)

	// Trigger lifecycle shutdown via context cancellation
	cancel()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("runLifecycle returned error on context cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runLifecycle shutdown after context cancellation")
	}

	// Verify that Store was persisted to disk by cleanup() during teardown
	stReloaded := store.New(stateFile, 2)
	if err := stReloaded.Load(); err != nil {
		t.Fatalf("failed to reload persisted state file: %v", err)
	}
	rec, ok := stReloaded.GetRecord("vless://test@1.1.1.1:443#LifecycleNode")
	if !ok || rec == nil {
		t.Fatal("expected candidate record to remain persisted after coordinated lifecycle shutdown")
	}
	if rec.Latest.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed, got %s", rec.Latest.Status)
	}
}

func TestFlagMode_Initialization(t *testing.T) {
	origLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	tmpDir := t.TempDir()

	tests := []struct {
		configMode string
		wantMode   country.Mode
	}{
		{"", country.ModeAuto},
		{"auto", country.ModeAuto},
		{"unicode", country.ModeUnicode},
		{"ascii", country.ModeASCII},
	}

	for _, tt := range tests {
		t.Run("Mode_"+tt.configMode, func(t *testing.T) {
			cfg := &config.Config{
				Headless:  false,
				StateFile: filepath.Join(tmpDir, "state_"+tt.configMode+".json"),
				FlagMode:  tt.configMode,
			}
			var logBuf bytes.Buffer
			rt, cleanup := setupRuntime(cfg, &logBuf)
			defer cleanup()

			if rt.Adapter == nil {
				t.Fatal("expected rt.Adapter != nil")
			}
			if rt.Adapter.FlagMode() != tt.wantMode {
				t.Errorf("expected adapter flagMode %v, got %v", tt.wantMode, rt.Adapter.FlagMode())
			}
		})
	}
}
