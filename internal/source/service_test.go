package source_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/source"
)

func setupTestConfigService(t *testing.T, initialCfg *config.Config) (*config.Service, string, *events.EventBus) {
	t.Helper()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	bus := events.New()
	t.Cleanup(func() { bus.Close() })

	svc, err := config.NewService(cfgPath, initialCfg, bus)
	if err != nil {
		t.Fatalf("failed to create config service: %v", err)
	}
	return svc, cfgPath, bus
}

func defaultTestConfig() *config.Config {
	return &config.Config{
		Sources:          config.NewSources("https://example.com/sub1", "https://example.com/sub2"),
		FetchIntervalRaw: "1h",
		FetchInterval:    1 * time.Hour,
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  1,
		},
	}
}

func TestSourceService_ListAndGet(t *testing.T) {
	cfg := defaultTestConfig()
	svc, _, _ := setupTestConfigService(t, cfg)
	srcSvc := source.NewService(svc)

	sources := srcSvc.List()
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}

	firstID := sources[0].ID
	got, err := srcSvc.Get(firstID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.URL != "https://example.com/sub1" {
		t.Errorf("expected URL https://example.com/sub1, got %s", got.URL)
	}
	if !got.Enabled {
		t.Error("expected source to be enabled")
	}

	// Non-existent ID
	_, err = srcSvc.Get("non-existent-id")
	if !errors.Is(err, source.ErrSourceNotFound) {
		t.Errorf("expected ErrSourceNotFound, got %v", err)
	}
}

func TestSourceService_Add(t *testing.T) {
	cfg := defaultTestConfig()
	svc, cfgPath, bus := setupTestConfigService(t, cfg)
	srcSvc := source.NewService(svc)

	subCh := bus.Subscribe(5)
	defer bus.Unsubscribe(subCh)

	// 1. Add valid new source
	added, err := srcSvc.Add(source.SourceItem{
		URL:     "https://example.com/new-sub",
		Name:    "My New Sub",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if added.ID == "" {
		t.Error("expected non-empty generated ID")
	}
	if added.URL != "https://example.com/new-sub" {
		t.Errorf("expected URL https://example.com/new-sub, got %s", added.URL)
	}
	if added.Name != "My New Sub" {
		t.Errorf("expected Name 'My New Sub', got %s", added.Name)
	}
	if !added.Enabled {
		t.Error("expected Enabled to be true")
	}
	if added.AddedAt.IsZero() {
		t.Error("expected non-zero AddedAt")
	}

	// Verify persistence through ConfigService
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to reload persisted config: %v", err)
	}
	if len(reloaded.Sources) != 3 {
		t.Fatalf("expected 3 sources persisted on disk, got %d", len(reloaded.Sources))
	}

	// Verify ConfigUpdated event was published
	select {
	case evt := <-subCh:
		cu, ok := evt.(config.ConfigUpdated)
		if !ok {
			t.Fatalf("unexpected event type %T", evt)
		}
		if len(cu.New.Sources) != 3 {
			t.Errorf("expected 3 sources in ConfigUpdated.New, got %d", len(cu.New.Sources))
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for ConfigUpdated event")
	}

	// 2. Reject duplicate URL (exact and normalized)
	_, err = srcSvc.Add(source.SourceItem{
		URL: "https://example.com/new-sub",
	})
	if !errors.Is(err, source.ErrDuplicateSource) {
		t.Fatalf("expected ErrDuplicateSource, got %v", err)
	}

	_, err = srcSvc.Add(source.SourceItem{
		URL: "HTTPS://EXAMPLE.COM:443/new-sub",
	})
	if !errors.Is(err, source.ErrDuplicateSource) {
		t.Fatalf("expected ErrDuplicateSource for normalized URL, got %v", err)
	}

	// 3. Reject invalid URLs
	invalidURLs := []string{
		"",
		"ftp://example.com/sub",
		"not-a-url",
		"https:///sub",
	}
	for _, inv := range invalidURLs {
		_, err = srcSvc.Add(source.SourceItem{URL: inv})
		if !errors.Is(err, source.ErrInvalidSourceURL) {
			t.Errorf("URL %q: expected ErrInvalidSourceURL, got %v", inv, err)
		}
	}

	// 4. Reject duplicate explicit ID
	_, err = srcSvc.Add(source.SourceItem{
		ID:  added.ID,
		URL: "https://example.com/unique-url-with-clashing-id",
	})
	if !errors.Is(err, source.ErrDuplicateSourceID) {
		t.Fatalf("expected ErrDuplicateSourceID, got %v", err)
	}
}

func TestSourceService_Update(t *testing.T) {
	cfg := defaultTestConfig()
	svc, _, _ := setupTestConfigService(t, cfg)
	srcSvc := source.NewService(svc)

	sources := srcSvc.List()
	id := sources[0].ID

	// 1. Update name and enabled state
	updated, err := srcSvc.Update(id, func(item *source.SourceItem) error {
		item.Name = "Updated Name"
		item.Enabled = false
		return nil
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updated.Name != "Updated Name" {
		t.Errorf("expected name 'Updated Name', got %s", updated.Name)
	}
	if updated.Enabled != false {
		t.Error("expected enabled to be false")
	}

	// 2. ID cannot be changed via Update mutator
	_, err = srcSvc.Update(id, func(item *source.SourceItem) error {
		item.ID = "malicious-id"
		return nil
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	got, _ := srcSvc.Get(id)
	if got.ID != id {
		t.Errorf("ID was improperly mutated: got %s, want %s", got.ID, id)
	}

	// 3. Updating to conflicting URL fails
	secondID := sources[1].ID
	_, err = srcSvc.Update(secondID, func(item *source.SourceItem) error {
		item.URL = sources[0].URL // conflict with first source
		return nil
	})
	if !errors.Is(err, source.ErrDuplicateSource) {
		t.Fatalf("expected ErrDuplicateSource on conflicting update, got %v", err)
	}

	// 4. Updating non-existent ID fails
	_, err = srcSvc.Update("missing", func(item *source.SourceItem) error {
		return nil
	})
	if !errors.Is(err, source.ErrSourceNotFound) {
		t.Fatalf("expected ErrSourceNotFound, got %v", err)
	}
}

func TestSourceService_EnableDisable(t *testing.T) {
	cfg := defaultTestConfig()
	svc, _, _ := setupTestConfigService(t, cfg)
	srcSvc := source.NewService(svc)

	sources := srcSvc.List()
	id := sources[0].ID

	if err := srcSvc.Disable(id); err != nil {
		t.Fatalf("Disable failed: %v", err)
	}
	item, _ := srcSvc.Get(id)
	if item.Enabled {
		t.Error("expected Enabled to be false after Disable")
	}

	if err := srcSvc.Enable(id); err != nil {
		t.Fatalf("Enable failed: %v", err)
	}
	item, _ = srcSvc.Get(id)
	if !item.Enabled {
		t.Error("expected Enabled to be true after Enable")
	}
}

func TestSourceService_Remove(t *testing.T) {
	cfg := defaultTestConfig()
	svc, cfgPath, _ := setupTestConfigService(t, cfg)
	srcSvc := source.NewService(svc)

	sources := srcSvc.List()
	if len(sources) != 2 {
		t.Fatalf("expected 2 initial sources, got %d", len(sources))
	}

	// Remove 1 source
	if err := srcSvc.Remove(sources[0].ID); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	remaining := srcSvc.List()
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining source, got %d", len(remaining))
	}
	if remaining[0].ID != sources[1].ID {
		t.Errorf("expected remaining ID %s, got %s", sources[1].ID, remaining[0].ID)
	}

	// Verify disk persistence
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if len(reloaded.Sources) != 1 {
		t.Fatalf("expected 1 source on disk, got %d", len(reloaded.Sources))
	}

	// Attempting to remove the last source must fail
	err = srcSvc.Remove(remaining[0].ID)
	if !errors.Is(err, source.ErrCannotRemoveLast) {
		t.Fatalf("expected ErrCannotRemoveLast, got %v", err)
	}

	// Removing non-existent ID fails
	err = srcSvc.Remove("missing")
	if !errors.Is(err, source.ErrSourceNotFound) {
		t.Fatalf("expected ErrSourceNotFound, got %v", err)
	}
}

func TestSourceService_ConcurrentOperationsAreRaceFree(t *testing.T) {
	cfg := defaultTestConfig()
	svc, _, _ := setupTestConfigService(t, cfg)
	srcSvc := source.NewService(svc)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = srcSvc.List()
				src, err := srcSvc.AddURL(
					"https://worker" + string(rune('a'+workerID)) + ".com/sub/" + string(rune('0'+j)),
				)
				if err == nil {
					_ = srcSvc.SetEnabled(src.ID, j%2 == 0)
					_, _ = srcSvc.Get(src.ID)
				}
			}
		}()
	}
	wg.Wait()
}

func TestSource_BackwardCompatibility_LegacyStringArrayLoadsAsEnabled(t *testing.T) {
	legacyJSON := `{
		"sources": [
			"https://legacy1.example.com/sub",
			"https://legacy2.example.com/sub"
		],
		"fetch_interval": "1h",
		"test": {
			"target_url": "https://example.com",
			"block_phrases": ["blocked"],
			"timeout": "5s"
		}
	}`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load legacy config: %v", err)
	}

	if len(cfg.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(cfg.Sources))
	}

	for i, s := range cfg.Sources {
		if s.ID == "" {
			t.Errorf("source[%d]: expected non-empty generated ID", i)
		}
		if !s.Enabled {
			t.Errorf("source[%d]: expected legacy string source to be Enabled=true", i)
		}
		if s.AddedAt.IsZero() {
			t.Errorf("source[%d]: expected non-zero AddedAt", i)
		}
	}

	if cfg.Sources[0].URL != "https://legacy1.example.com/sub" {
		t.Errorf("unexpected URL for source 0: %s", cfg.Sources[0].URL)
	}
	if cfg.Sources[1].URL != "https://legacy2.example.com/sub" {
		t.Errorf("unexpected URL for source 1: %s", cfg.Sources[1].URL)
	}

	// EnabledSourceURLs includes both
	urls := cfg.EnabledSourceURLs()
	if len(urls) != 2 {
		t.Fatalf("expected 2 enabled URLs, got %d", len(urls))
	}

	// Verify stable deterministic ID across reloads
	cfgAgain, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("second Load failed: %v", err)
	}
	if cfgAgain.Sources[0].ID != cfg.Sources[0].ID {
		t.Errorf("IDs diverged across reloads: %s vs %s", cfgAgain.Sources[0].ID, cfg.Sources[0].ID)
	}
}

func TestSource_StructuredRoundTrip(t *testing.T) {
	fixedTime := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	origCfg := &config.Config{
		Sources: []config.SourceItem{
			{
				ID:      "src_custom_1",
				URL:     "https://primary.example.com/sub",
				Name:    "Primary Provider",
				Enabled: true,
				AddedAt: fixedTime,
			},
			{
				ID:      "src_custom_2",
				URL:     "https://backup.example.com/sub",
				Name:    "Backup Provider",
				Enabled: false,
				AddedAt: fixedTime,
			},
		},
		FetchIntervalRaw: "2h",
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
		},
	}

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	data, err := json.MarshalIndent(origCfg, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent failed: %v", err)
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(loaded.Sources))
	}

	s1 := loaded.Sources[0]
	if s1.ID != "src_custom_1" || s1.URL != "https://primary.example.com/sub" || s1.Name != "Primary Provider" || !s1.Enabled {
		t.Errorf("source 1 fields mismatch: %+v", s1)
	}

	s2 := loaded.Sources[1]
	if s2.ID != "src_custom_2" || s2.URL != "https://backup.example.com/sub" || s2.Name != "Backup Provider" || s2.Enabled {
		t.Errorf("source 2 fields mismatch: %+v", s2)
	}

	// EnabledSourceURLs should ONLY return s1
	enabledURLs := loaded.EnabledSourceURLs()
	if len(enabledURLs) != 1 || enabledURLs[0] != "https://primary.example.com/sub" {
		t.Fatalf("expected only enabled source in EnabledSourceURLs, got: %v", enabledURLs)
	}
}
