package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/store"
	"gemsub/internal/tui/country"
	"gemsub/internal/version"
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

func TestVersionOutput(t *testing.T) {
	info := version.Info()
	if !bytes.Contains([]byte(info), []byte("gemsub")) {
		t.Errorf("expected version output to contain app name, got: %s", info)
	}
	if !bytes.Contains([]byte(info), []byte("commit:")) {
		t.Errorf("expected version output to contain 'commit:', got: %s", info)
	}
	if !bytes.Contains([]byte(info), []byte("built:")) {
		t.Errorf("expected version output to contain 'built:', got: %s", info)
	}
}

func TestProductionRuntime_ConfigUpdated_ReachesAdapter(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	stateFile := filepath.Join(tmpDir, "state.json")

	baseCfg := &config.Config{
		Sources:          config.NewSources("https://example.com/sub"),
		StateFile:        stateFile,
		FetchIntervalRaw: "1h",
		FlagMode:         "auto",
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
		},
	}
	if err := baseCfg.Validate(); err != nil {
		t.Fatalf("baseCfg.Validate: %v", err)
	}

	configSvc, err := config.NewService(cfgPath, baseCfg, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	var logBuf bytes.Buffer
	rt, cleanup := setupRuntime(baseCfg, &logBuf, configSvc)
	defer cleanup()

	if rt.Adapter == nil {
		t.Fatal("expected rt.Adapter to be non-nil in non-headless runtime")
	}
	if rt.Adapter.FlagMode() != country.ModeAuto {
		t.Fatalf("expected initial FlagMode Auto, got %v", rt.Adapter.FlagMode())
	}

	// Mutate FlagMode through ConfigService (simulating control plane / settings update)
	err = configSvc.Update(func(c *config.Config) error {
		c.FlagMode = "ascii"
		return nil
	})
	if err != nil {
		t.Fatalf("configSvc.Update: %v", err)
	}

	// Verify that ConfigUpdated reached Adapter through the production bus wiring
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if rt.Adapter.FlagMode() == country.ModeASCII {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if rt.Adapter.FlagMode() != country.ModeASCII {
		t.Fatalf("expected FlagMode updated to ASCII via production bus, got %v", rt.Adapter.FlagMode())
	}
}

func TestProductionRuntime_SchedulerControlWiring(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(""))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	cfg := &config.Config{
		Headless:         true,
		StateFile:        filepath.Join(tmpDir, "state.json"),
		Sources:          config.NewSources(srv.URL),
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	var logBuf bytes.Buffer
	rt, cleanup := setupRuntime(cfg, &logBuf)
	defer cleanup()

	if rt.SchedulerCtrl == nil {
		t.Fatal("expected rt.SchedulerCtrl to be non-nil")
	}

	// 1. Initially stopped
	if rt.SchedulerCtrl.IsRunning() {
		t.Error("expected SchedulerCtrl to be stopped initially")
	}
	st := rt.SchedulerCtrl.Status()
	if st.Running {
		t.Error("expected Status.Running=false initially")
	}

	// 2. Start
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := rt.SchedulerCtrl.Start(ctx); err != nil {
		t.Fatalf("SchedulerCtrl.Start failed: %v", err)
	}
	if !rt.SchedulerCtrl.IsRunning() {
		t.Error("expected SchedulerCtrl to be running after Start")
	}

	// 3. Duplicate Start rejected
	if err := rt.SchedulerCtrl.Start(ctx); err == nil {
		t.Error("expected error on duplicate Start, got nil")
	}

	// 4. Stop
	if err := rt.SchedulerCtrl.Stop(); err != nil {
		t.Fatalf("SchedulerCtrl.Stop failed: %v", err)
	}
	if rt.SchedulerCtrl.IsRunning() {
		t.Error("expected SchedulerCtrl to be stopped after Stop")
	}

	// 5. Duplicate Stop idempotent
	if err := rt.SchedulerCtrl.Stop(); err != nil {
		t.Errorf("duplicate Stop returned error: %v", err)
	}
}

func TestProductionRuntime_PublishingServiceWiring(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(""))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	pubRepo := filepath.Join(tmpDir, "pubrepo")
	cfgPath := filepath.Join(tmpDir, "config.json")

	cfg := &config.Config{
		Headless:         true,
		StateFile:        filepath.Join(tmpDir, "state.json"),
		Sources:          config.NewSources(srv.URL),
		FetchIntervalRaw: "1h",
		Publishing: config.PublishingConfig{
			Enabled:    false,
			Repository: pubRepo,
			Branch:     "main",
			RemoteURL:  "https://ghp_testToken@github.com/user/repo.git",
		},
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	cfgSvc, err := config.NewService(cfgPath, cfg, nil)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}

	var logBuf bytes.Buffer
	rt, cleanup := setupRuntime(cfg, &logBuf, cfgSvc)
	defer cleanup()

	if rt.PublishSvc == nil {
		t.Fatal("expected rt.PublishSvc to be non-nil")
	}

	// 1. Initial config matches
	c := rt.PublishSvc.Config()
	if c.Repository != pubRepo || c.Branch != "main" {
		t.Errorf("unexpected PublishSvc config: %+v", c)
	}

	// 2. Status hides credentials
	st := rt.PublishSvc.Status()
	if strings.Contains(st.RemoteURL, "ghp_testToken") {
		t.Errorf("secret token leaked in Status.RemoteURL: %q", st.RemoteURL)
	}
	if st.RemoteURL != "https://***@github.com/user/repo.git" {
		t.Errorf("expected masked RemoteURL, got: %q", st.RemoteURL)
	}

	// 3. Mutation propagates and persists via ConfigService
	if err := rt.PublishSvc.SetBranch("release"); err != nil {
		t.Fatalf("PublishSvc.SetBranch failed: %v", err)
	}
	if rt.PublishSvc.Config().Branch != "release" {
		t.Errorf("expected Branch=release, got %s", rt.PublishSvc.Config().Branch)
	}
	if cfgSvc.Get().Publishing.Branch != "release" {
		t.Errorf("expected ConfigService to reflect Branch=release, got %s", cfgSvc.Get().Publishing.Branch)
	}
}
