package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventPublisher is an interface for broadcasting application events.
// It is satisfied by *events.EventBus.
type EventPublisher interface {
	Publish(event any)
}

// ConfigUpdated is emitted via EventPublisher when configuration has been successfully
// updated and persisted.
type ConfigUpdated struct {
	Old Config
	New Config
}

// Service provides thread-safe access, validation, mutation, and atomic
// persistence for gemsub's configuration.
type Service struct {
	mu         sync.RWMutex
	path       string
	cfg        *Config
	initialCfg *Config
	bus        EventPublisher
	isFirstRun bool
}

// ConfigService is an alias for Service.
type ConfigService = Service

// NewService creates and initializes a configuration Service.
// If cfg is provided, it is validated and deep-cloned.
// If cfg is nil and path is non-empty, the configuration is loaded from path.
func NewService(path string, cfg *Config, bus EventPublisher) (*Service, error) {
	if cfg != nil {
		cloned := cfg.Clone()
		if err := cloned.Validate(); err != nil {
			return nil, fmt.Errorf("invalid config: %w", err)
		}
		return &Service{
			path:       path,
			cfg:        cloned,
			initialCfg: cloned.Clone(),
			bus:        bus,
		}, nil
	}

	if path == "" {
		return nil, fmt.Errorf("config path or instance required")
	}

	loaded, err := Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	return &Service{
		path:       path,
		cfg:        loaded,
		initialCfg: loaded.Clone(),
		bus:        bus,
	}, nil
}

// DefaultConfig returns a valid default configuration template suitable for first-run onboarding.
func DefaultConfig() *Config {
	defaultRetries := 2
	return &Config{
		Sources:          []SourceItem{},
		FetchIntervalRaw: "3h",
		FetchInterval:    3 * time.Hour,
		Test: TestConfig{
			HealthURL:        "https://www.gstatic.com/generate_204",
			HealthTimeoutRaw: "4s",
			HealthTimeout:    4 * time.Second,
			Gemini: GeminiConfig{
				URL: "https://gemini.google.com/",
				BlockPhrases: []string{
					"isn't currently supported in your country",
					"not available in your country",
					"not available in your region",
				},
			},
			TargetURL: "https://gemini.google.com/",
			BlockPhrases: []string{
				"isn't currently supported in your country",
				"not available in your country",
				"not available in your region",
			},
			TimeoutRaw:            "10s",
			Timeout:               10 * time.Second,
			DialTimeoutRaw:        "4s",
			DialTimeout:           4 * time.Second,
			Concurrency:           20,
			MaxRetriesRaw:         &defaultRetries,
			MaxRetries:            2,
			RetryBackoffRaw:       "1s",
			RetryBackoff:          1 * time.Second,
			MaxInconclusiveCycles: 2,
		},
		Serve: ServeConfig{
			Listen: "127.0.0.1:8765",
			Path:   "/sub",
			Format: "base64",
		},
		Publishing: PublishingConfig{
			Enabled: false,
			Branch:  "main",
		},
		StateFile: "./gemsub_state.json",
	}
}

// NewDefaultService creates a configuration Service pre-populated with a default configuration
// template for first-run onboarding. It records that it is in first-run mode and does not persist
// to disk until Save() is explicitly called.
func NewDefaultService(path string, bus EventPublisher) *Service {
	cfg := DefaultConfig()
	return &Service{
		path:       path,
		cfg:        cfg,
		initialCfg: cfg.Clone(),
		bus:        bus,
		isFirstRun: true,
	}
}

// IsFirstRun reports whether the configuration service is in initial first-run onboarding mode
// (meaning config has not yet been persisted to disk).
func (s *Service) IsFirstRun() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isFirstRun
}

// PendingRestartFields returns any canonical field paths modified since initialization
// that require an application restart to take effect in the runtime environment.
func (s *Service) PendingRestartFields() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.initialCfg == nil || s.cfg == nil {
		return nil
	}
	return RequiresRestart(*s.initialCfg, *s.cfg)
}

// Get returns an immutable deep-cloned snapshot of current configuration.
func (s *Service) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.cfg == nil {
		return Config{}
	}
	return *s.cfg.Clone()
}

// Path returns the configured configuration file path.
func (s *Service) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

// SetPath updates the configuration file path.
func (s *Service) SetPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
}

// SetEventPublisher sets or updates the event publisher.
func (s *Service) SetEventPublisher(bus EventPublisher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = bus
}

// Update executes a transactional mutation on the configuration.
// It applies mutator to a candidate clone, validates the result,
// and atomically persists it to disk before updating in-memory state.
// If mutator returns an error, validation fails, or disk persistence fails,
// both in-memory state and on-disk files are left untouched.
// On success, ConfigUpdated is emitted via the configured EventPublisher
// outside the internal lock to avoid deadlock with synchronous subscribers.
func (s *Service) Update(mutator func(*Config) error) error {
	if mutator == nil {
		return fmt.Errorf("mutator function cannot be nil")
	}

	s.mu.Lock()

	candidate := s.cfg.Clone()
	if candidate == nil {
		candidate = &Config{}
	}

	if err := mutator(candidate); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("mutation failed: %w", err)
	}

	if err := candidate.Validate(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("invalid config: %w", err)
	}

	if s.path != "" {
		data, err := json.MarshalIndent(candidate, "", "  ")
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("marshal config: %w", err)
		}
		data = append(data, '\n')

		if err := AtomicWriteFile(s.path, data, 0o600); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("persist config: %w", err)
		}
	}

	var oldCfg Config
	if s.cfg != nil {
		oldCfg = *s.cfg.Clone()
	}
	s.cfg = candidate
	s.isFirstRun = false
	newCfg := *s.cfg.Clone()
	bus := s.bus

	s.mu.Unlock()

	if bus != nil {
		bus.Publish(ConfigUpdated{
			Old: oldCfg,
			New: newCfg,
		})
	}

	return nil
}

// Save atomically writes the current in-memory configuration to disk.
func (s *Service) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.path == "" {
		return fmt.Errorf("config path not set")
	}
	if s.cfg == nil {
		return fmt.Errorf("no configuration loaded")
	}

	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	if err := AtomicWriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("persist config: %w", err)
	}

	s.isFirstRun = false
	return nil
}

// Reload re-reads configuration from disk and updates in-memory state.
// If reading or validation fails, in-memory state is preserved unchanged.
// On success, ConfigUpdated is emitted via the configured EventPublisher
// outside the internal lock to avoid deadlock with synchronous subscribers.
func (s *Service) Reload() error {
	s.mu.Lock()

	if s.path == "" {
		s.mu.Unlock()
		return fmt.Errorf("config path not set")
	}

	loaded, err := Load(s.path)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("reload config: %w", err)
	}

	var oldCfg Config
	if s.cfg != nil {
		oldCfg = *s.cfg.Clone()
	}
	s.cfg = loaded
	newCfg := *s.cfg.Clone()
	bus := s.bus

	s.mu.Unlock()

	if bus != nil {
		bus.Publish(ConfigUpdated{
			Old: oldCfg,
			New: newCfg,
		})
	}

	return nil
}

// AtomicWriteFile writes data to a temporary file in the same directory as path,
// syncs file data to disk, and atomically renames it to path.
// This guarantees atomic replacement (readers never observe truncated or partial
// files during process crashes or concurrent access) consistent with the store
// persistence contract, but does not perform parent-directory fsync.
// If path already exists, its file permissions are preserved.
// If path is newly created, defaultPerm is applied.
func AtomicWriteFile(path string, data []byte, defaultPerm os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}

	perm := defaultPerm
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpFile.Name()

	var success bool
	defer func() {
		if !success {
			_ = tmpFile.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmpFile.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	success = true
	return nil
}
