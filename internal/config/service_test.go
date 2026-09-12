package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/events"
)

func validTestConfig() *config.Config {
	maxRetries := 2
	return &config.Config{
		Sources:          config.NewSources("https://example.com/source1", "https://example.com/source2"),
		FetchIntervalRaw: "2h",
		Test: config.TestConfig{
			Gemini: config.GeminiConfig{
				URL:          "https://gemini.google.com/",
				BlockPhrases: []string{"phrase1", "phrase2"},
			},
			HealthURL:        "https://www.gstatic.com/generate_204",
			HealthTimeoutRaw: "4s",
			TimeoutRaw:       "10s",
			DialTimeoutRaw:   "4s",
			Concurrency:      20,
			RateLimitRPS:     10,
			MaxRetriesRaw:    &maxRetries,
			RetryBackoffRaw:  "1s",
		},
		Serve: config.ServeConfig{
			Listen: "127.0.0.1:8765",
			Path:   "/sub",
			Format: "base64",
		},
		Publishing: config.PublishingConfig{
			Enabled: false,
		},
		StateFile: "./gemsub_state.json",
		Headless:  false,
	}
}

func TestConfig_Clone(t *testing.T) {
	orig := validTestConfig()
	if err := orig.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	clone := orig.Clone()
	if clone == nil {
		t.Fatal("expected non-nil clone")
	}

	// 1. Mutate Sources on clone
	clone.Sources[0].URL = "https://mutated.com"
	if orig.Sources[0].URL == "https://mutated.com" {
		t.Error("mutating clone.Sources affected orig.Sources")
	}

	// 2. Mutate BlockPhrases on clone
	clone.Test.Gemini.BlockPhrases[0] = "mutated phrase"
	if orig.Test.Gemini.BlockPhrases[0] == "mutated phrase" {
		t.Error("mutating clone.Test.Gemini.BlockPhrases affected orig")
	}
	clone.Test.BlockPhrases[0] = "mutated phrase legacy"
	if orig.Test.BlockPhrases[0] == "mutated phrase legacy" {
		t.Error("mutating clone.Test.BlockPhrases affected orig")
	}

	// 3. Mutate MaxRetriesRaw pointer on clone
	*clone.Test.MaxRetriesRaw = 99
	if *orig.Test.MaxRetriesRaw == 99 {
		t.Error("mutating clone.Test.MaxRetriesRaw pointer value affected orig")
	}

	// 4. Test nil receiver
	var nilCfg *config.Config
	if nilCfg.Clone() != nil {
		t.Error("expected nil clone for nil receiver")
	}
}

func TestService_NewService_WithConfig(t *testing.T) {
	cfg := validTestConfig()
	svc, err := config.NewService("/tmp/cfg.json", cfg, nil)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	if svc.Path() != "/tmp/cfg.json" {
		t.Errorf("expected path '/tmp/cfg.json', got %q", svc.Path())
	}
	got := svc.Get()
	if got.FetchIntervalRaw != "2h" {
		t.Errorf("expected fetch interval '2h', got %q", got.FetchIntervalRaw)
	}
}

func TestService_NewService_WithInvalidConfig(t *testing.T) {
	cfg := validTestConfig()
	cfg.Sources = nil // invalid: sources cannot be empty
	_, err := config.NewService("/tmp/cfg.json", cfg, nil)
	if err == nil {
		t.Fatal("expected error with empty sources, got nil")
	}
}

func TestService_NewService_FromPath(t *testing.T) {
	dir := t.TempDir()
	cfgMap := baseConfig()
	path := writeTestConfig(t, dir, cfgMap)

	svc, err := config.NewService(path, nil, nil)
	if err != nil {
		t.Fatalf("NewService from file failed: %v", err)
	}
	got := svc.Get()
	if got.FetchIntervalRaw != "3h" {
		t.Errorf("expected '3h', got %q", got.FetchIntervalRaw)
	}
}

func TestService_NewService_NonexistentPath(t *testing.T) {
	_, err := config.NewService("/nonexistent/file/path.json", nil, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestService_NewService_EmptyPathAndNilConfig(t *testing.T) {
	_, err := config.NewService("", nil, nil)
	if err == nil {
		t.Fatal("expected error for empty path and nil config, got nil")
	}
}

func TestService_Get_Immutability(t *testing.T) {
	cfg := validTestConfig()
	svc, err := config.NewService("", cfg, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	snapshot := svc.Get()
	snapshot.Sources[0].URL = "https://mutated.com"
	*snapshot.Test.MaxRetriesRaw = 42

	fresh := svc.Get()
	if fresh.Sources[0].URL == "https://mutated.com" {
		t.Error("modifying Get() returned snapshot mutated internal Service state")
	}
	if *fresh.Test.MaxRetriesRaw == 42 {
		t.Error("modifying Get() MaxRetriesRaw mutated internal Service state")
	}
}

func TestService_SetPath(t *testing.T) {
	cfg := validTestConfig()
	svc, err := config.NewService("", cfg, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if svc.Path() != "" {
		t.Errorf("expected empty path, got %q", svc.Path())
	}
	svc.SetPath("/new/path.json")
	if svc.Path() != "/new/path.json" {
		t.Errorf("expected '/new/path.json', got %q", svc.Path())
	}
}

func TestService_Update_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := validTestConfig()
	bus := events.New()
	defer bus.Close()

	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	svc, err := config.NewService(path, cfg, bus)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	err = svc.Update(func(c *config.Config) error {
		c.FetchIntervalRaw = "4h"
		c.Sources = append(c.Sources, config.NewSource("https://example.com/extra"))
		return nil
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 1. Verify in-memory state
	got := svc.Get()
	if got.FetchIntervalRaw != "4h" {
		t.Errorf("expected FetchIntervalRaw '4h', got %q", got.FetchIntervalRaw)
	}
	if got.FetchInterval != 4*time.Hour {
		t.Errorf("expected FetchInterval 4h, got %v", got.FetchInterval)
	}
	if len(got.Sources) != 3 {
		t.Errorf("expected 3 sources, got %d", len(got.Sources))
	}

	// 2. Verify disk state
	diskCfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load from disk failed: %v", err)
	}
	if diskCfg.FetchIntervalRaw != "4h" {
		t.Errorf("expected disk config to have '4h', got %q", diskCfg.FetchIntervalRaw)
	}
	if len(diskCfg.Sources) != 3 {
		t.Errorf("expected 3 sources on disk, got %d", len(diskCfg.Sources))
	}

	// 3. Verify EventBus notification
	select {
	case evt := <-ch:
		updateEvt, ok := evt.(config.ConfigUpdated)
		if !ok {
			t.Fatalf("expected config.ConfigUpdated event, got %T", evt)
		}
		if updateEvt.Old.FetchIntervalRaw != "2h" {
			t.Errorf("expected old interval '2h', got %q", updateEvt.Old.FetchIntervalRaw)
		}
		if updateEvt.New.FetchIntervalRaw != "4h" {
			t.Errorf("expected new interval '4h', got %q", updateEvt.New.FetchIntervalRaw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ConfigUpdated event")
	}
}

func TestService_Update_RollbackOnMutatorError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := validTestConfig()
	bus := events.New()
	defer bus.Close()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	svc, err := config.NewService(path, cfg, bus)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Save initial config to disk first
	if err := svc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	mutatorErr := errors.New("aborted by mutator")
	err = svc.Update(func(c *config.Config) error {
		c.FetchIntervalRaw = "5h"
		return mutatorErr
	})
	if !errors.Is(err, mutatorErr) {
		t.Fatalf("expected error %v, got %v", mutatorErr, err)
	}

	// 1. Verify in-memory state unchanged
	if svc.Get().FetchIntervalRaw != "2h" {
		t.Errorf("in-memory state changed after mutator error: %q", svc.Get().FetchIntervalRaw)
	}

	// 2. Verify disk state unchanged
	diskCfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if diskCfg.FetchIntervalRaw != "2h" {
		t.Errorf("disk state changed after mutator error: %q", diskCfg.FetchIntervalRaw)
	}

	// 3. Verify no event emitted
	select {
	case evt := <-ch:
		t.Fatalf("unexpected event emitted on rollback: %v", evt)
	case <-time.After(50 * time.Millisecond):
		// OK: no event
	}
}

func TestService_Update_RollbackOnValidationError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := validTestConfig()
	bus := events.New()
	defer bus.Close()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	svc, err := config.NewService(path, cfg, bus)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Try invalid FetchInterval (must be >= 1m)
	err = svc.Update(func(c *config.Config) error {
		c.FetchIntervalRaw = "10s"
		return nil
	})
	if err == nil {
		t.Fatal("expected validation error for 10s fetch interval, got nil")
	}

	// In-memory unchanged
	if svc.Get().FetchIntervalRaw != "2h" {
		t.Errorf("in-memory state mutated: %q", svc.Get().FetchIntervalRaw)
	}

	// Disk unchanged
	diskCfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if diskCfg.FetchIntervalRaw != "2h" {
		t.Errorf("disk state mutated: %q", diskCfg.FetchIntervalRaw)
	}

	// No event
	select {
	case evt := <-ch:
		t.Fatalf("unexpected event on validation error: %v", evt)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestService_Update_RollbackOnPersistenceError(t *testing.T) {
	// Point service to an invalid path that cannot be written
	invalidPath := "/invalid-dir-that-does-not-exist/sub/config.json"

	cfg := validTestConfig()
	bus := events.New()
	defer bus.Close()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	svc, err := config.NewService(invalidPath, cfg, bus)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	err = svc.Update(func(c *config.Config) error {
		c.FetchIntervalRaw = "4h"
		return nil
	})
	if err == nil {
		t.Fatal("expected persistence error for invalid path, got nil")
	}

	// In-memory state must remain unchanged
	if svc.Get().FetchIntervalRaw != "2h" {
		t.Errorf("in-memory state mutated on persistence error: %q", svc.Get().FetchIntervalRaw)
	}

	// No event emitted
	select {
	case evt := <-ch:
		t.Fatalf("unexpected event on persistence error: %v", evt)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestService_Update_NilMutator(t *testing.T) {
	cfg := validTestConfig()
	svc, err := config.NewService("", cfg, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	err = svc.Update(nil)
	if err == nil {
		t.Fatal("expected error for nil mutator, got nil")
	}
}

func TestService_SaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := validTestConfig()
	bus := events.New()
	defer bus.Close()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	svc, err := config.NewService(path, cfg, bus)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Save to disk
	if err := svc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// External modification of disk file
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// replace "2h" with "5h"
	mutatedData := []byte(string(data))
	mutatedData = []byte(replaceString(string(mutatedData), `"fetch_interval": "2h"`, `"fetch_interval": "5h"`))
	if err := os.WriteFile(path, mutatedData, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// In-memory is still "2h" before Reload
	if svc.Get().FetchIntervalRaw != "2h" {
		t.Errorf("expected 2h before reload, got %q", svc.Get().FetchIntervalRaw)
	}

	// Reload
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// In-memory is now "5h"
	if svc.Get().FetchIntervalRaw != "5h" {
		t.Errorf("expected 5h after reload, got %q", svc.Get().FetchIntervalRaw)
	}

	// Reload should also have emitted ConfigUpdated
	select {
	case evt := <-ch:
		updateEvt, ok := evt.(config.ConfigUpdated)
		if !ok {
			t.Fatalf("expected ConfigUpdated event, got %T", evt)
		}
		if updateEvt.Old.FetchIntervalRaw != "2h" || updateEvt.New.FetchIntervalRaw != "5h" {
			t.Errorf("unexpected event payloads: old=%q new=%q", updateEvt.Old.FetchIntervalRaw, updateEvt.New.FetchIntervalRaw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ConfigUpdated on Reload")
	}
}

func TestService_Reload_CorruptedFileRollback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := validTestConfig()
	svc, err := config.NewService(path, cfg, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Corrupt file on disk
	if err := os.WriteFile(path, []byte("{ corrupt json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = svc.Reload()
	if err == nil {
		t.Fatal("expected error on reload of corrupt file, got nil")
	}

	// In-memory state preserved
	if svc.Get().FetchIntervalRaw != "2h" {
		t.Errorf("in-memory state corrupted after failed reload: %q", svc.Get().FetchIntervalRaw)
	}
}

func TestService_Permissions_NewFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping POSIX file permission mode assertion on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "new_config.json")

	cfg := validTestConfig()
	svc, err := config.NewService(path, cfg, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err := svc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	perm := fi.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("expected newly created file to have mode 0600, got %#o", perm)
	}
}

func TestService_Permissions_PreserveExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping POSIX file permission mode assertion on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "existing_config.json")

	// Pre-create file with 0644
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := validTestConfig()
	svc, err := config.NewService(path, cfg, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err := svc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	perm := fi.Mode().Perm()
	if perm != 0o644 {
		t.Errorf("expected preserved mode 0644, got %#o", perm)
	}

	// Now update through Update()
	err = svc.Update(func(c *config.Config) error {
		c.FetchIntervalRaw = "4h"
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	fi, err = os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after Update: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("expected mode 0644 preserved after Update(), got %#o", fi.Mode().Perm())
	}
}

func TestAtomicWriteFile_NoDanglingTempFiles(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.json")

	// 1. Successful write
	if err := config.AtomicWriteFile(targetPath, []byte(`{"key":"value"}`), 0o600); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "target.json" {
		t.Errorf("expected only target.json, got: %v", entries)
	}
}

func TestService_ConcurrentReadsAndUpdates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := validTestConfig()
	bus := events.New()
	defer bus.Close()

	svc, err := config.NewService(path, cfg, bus)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const numReaders = 10
	const numWriters = 5
	const iterations = 50

	var wg sync.WaitGroup
	start := make(chan struct{})

	// Readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				c := svc.Get()
				if len(c.Sources) == 0 {
					t.Errorf("reader observed empty sources")
				}
				if c.FetchIntervalRaw == "" {
					t.Errorf("reader observed empty fetch interval")
				}
			}
		}()
	}

	// Writers
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		writerID := i
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				_ = svc.Update(func(c *config.Config) error {
					if (writerID+j)%2 == 0 {
						c.FetchIntervalRaw = "2h"
					} else {
						c.FetchIntervalRaw = "3h"
					}
					return nil
				})
			}
		}()
	}

	close(start)
	wg.Wait()

	finalCfg := svc.Get()
	if finalCfg.FetchIntervalRaw != "2h" && finalCfg.FetchIntervalRaw != "3h" {
		t.Errorf("unexpected final FetchIntervalRaw: %q", finalCfg.FetchIntervalRaw)
	}
}

func replaceString(s, old, new string) string {
	for {
		idx := len(s)
		for i := 0; i <= len(s)-len(old); i++ {
			if s[i:i+len(old)] == old {
				idx = i
				break
			}
		}
		if idx > len(s)-len(old) {
			break
		}
		s = s[:idx] + new + s[idx+len(old):]
	}
	return s
}

type syncSubscriberPublisher struct {
	svc     *config.Service
	called  bool
	readCfg config.Config
}

func (p *syncSubscriberPublisher) Publish(event any) {
	p.called = true
	// Calling Get() acquires s.mu.RLock().
	// If Update() or Reload() held s.mu.Lock() when invoking Publish, this would deadlock!
	p.readCfg = p.svc.Get()
}

func TestService_PublishLockScope_NoDeadlockWithSynchronousSubscriber(t *testing.T) {
	cfg := validTestConfig()
	publisher := &syncSubscriberPublisher{}

	svc, err := config.NewService("", cfg, publisher)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	publisher.svc = svc

	// 1. Test Update()
	done := make(chan struct{})
	go func() {
		err := svc.Update(func(c *config.Config) error {
			c.FetchIntervalRaw = "4h"
			return nil
		})
		if err != nil {
			t.Errorf("Update failed: %v", err)
		}
		close(done)
	}()

	select {
	case <-done:
		// Succeeded without deadlock
	case <-time.After(2 * time.Second):
		t.Fatal("Update deadlocked while publishing event to synchronous subscriber")
	}

	if !publisher.called {
		t.Error("expected synchronous publisher to be called")
	}
	if publisher.readCfg.FetchIntervalRaw != "4h" {
		t.Errorf("expected synchronous subscriber to read updated interval '4h', got %q", publisher.readCfg.FetchIntervalRaw)
	}

	// 2. Test Reload() with synchronous publisher
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	svc.SetPath(path)
	if err := svc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	publisher.called = false
	reloadDone := make(chan struct{})
	go func() {
		if err := svc.Reload(); err != nil {
			t.Errorf("Reload failed: %v", err)
		}
		close(reloadDone)
	}()

	select {
	case <-reloadDone:
		// Succeeded without deadlock
	case <-time.After(2 * time.Second):
		t.Fatal("Reload deadlocked while publishing event to synchronous subscriber")
	}

	if !publisher.called {
		t.Error("expected synchronous publisher to be called during reload")
	}
}

func TestService_NewDefaultService_Lifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	svc := config.NewDefaultService(path, nil)
	if svc == nil {
		t.Fatal("expected non-nil service from NewDefaultService")
	}

	if !svc.IsFirstRun() {
		t.Error("expected IsFirstRun() == true initially")
	}

	if svc.Path() != path {
		t.Errorf("expected Path %q, got %q", path, svc.Path())
	}

	cfg := svc.Get()
	if cfg.Test.Concurrency != 20 {
		t.Errorf("expected default Concurrency 20, got %d", cfg.Test.Concurrency)
	}
	if cfg.Test.TimeoutRaw != "10s" {
		t.Errorf("expected default TimeoutRaw '10s', got %q", cfg.Test.TimeoutRaw)
	}
	if cfg.Serve.Listen != "127.0.0.1:8765" {
		t.Errorf("expected default Serve.Listen '127.0.0.1:8765', got %q", cfg.Serve.Listen)
	}
	if cfg.Serve.Path != "/sub" {
		t.Errorf("expected default Serve.Path '/sub', got %q", cfg.Serve.Path)
	}
	if cfg.Test.Gemini.URL != "https://gemini.google.com/" {
		t.Errorf("expected default Gemini.URL 'https://gemini.google.com/', got %q", cfg.Test.Gemini.URL)
	}

	// Add source to make it fully valid and save
	err := svc.Update(func(c *config.Config) error {
		c.Sources = []config.SourceItem{
			config.NewSource("https://example.com/feed.txt", "Example Feed"),
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if err := svc.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if svc.IsFirstRun() {
		t.Error("expected IsFirstRun() == false after Save()")
	}

	// Verify file exists on disk and is valid
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load saved config failed: %v", err)
	}
	if len(loaded.Sources) != 1 || loaded.Sources[0].URL != "https://example.com/feed.txt" {
		t.Errorf("unexpected loaded sources: %+v", loaded.Sources)
	}
}
