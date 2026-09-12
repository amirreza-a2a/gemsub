package source

import (
	"errors"
	"fmt"
	"time"

	"gemsub/internal/config"
)

var (
	// ErrSourceNotFound is returned when operating on a non-existent source ID.
	ErrSourceNotFound = errors.New("source not found")

	// ErrDuplicateSource is returned when attempting to add or update a source to an existing URL.
	ErrDuplicateSource = errors.New("duplicate source URL")

	// ErrDuplicateSourceID is returned when attempting to add a source with an existing ID.
	ErrDuplicateSourceID = errors.New("duplicate source ID")

	// ErrInvalidSourceURL is returned when a source URL fails validation.
	ErrInvalidSourceURL = errors.New("invalid source URL")

	// ErrCannotRemoveLast is returned when attempting to remove the only remaining source.
	ErrCannotRemoveLast = errors.New("cannot remove last source: at least one source required")
)

// SourceItem is an alias for config.SourceItem.
type SourceItem = config.SourceItem

// Source is an alias for config.Source.
type Source = config.Source

// Service provides lifecycle and configuration management for subscription sources.
// It persists all mutations through ConfigService.
type Service struct {
	configSvc *config.Service
}

// SourceService is an alias for Service.
type SourceService = Service

// NewService creates a new source lifecycle service backed by configSvc.
func NewService(configSvc *config.Service) *Service {
	return &Service{
		configSvc: configSvc,
	}
}

// List returns an isolated snapshot copy of all configured sources.
func (s *Service) List() []SourceItem {
	if s == nil || s.configSvc == nil {
		return nil
	}
	cfg := s.configSvc.Get()
	res := make([]SourceItem, len(cfg.Sources))
	copy(res, cfg.Sources)
	return res
}

// Get returns the source with the matching ID, or ErrSourceNotFound.
func (s *Service) Get(id string) (SourceItem, error) {
	if s == nil || s.configSvc == nil {
		return SourceItem{}, fmt.Errorf("config service unavailable")
	}
	cfg := s.configSvc.Get()
	for _, src := range cfg.Sources {
		if src.ID == id {
			return src, nil
		}
	}
	return SourceItem{}, fmt.Errorf("%w: %s", ErrSourceNotFound, id)
}

// AddToConfig applies canonical source normalization, ID generation, duplicate checks,
// default naming, and appends the source item to the provided *config.Config.
// It mutates cfg.Sources in-place but does NOT persist to disk, allowing it to
// participate inside a single atomic ConfigService.Update() transaction.
func (s *Service) AddToConfig(cfg *config.Config, item SourceItem) (SourceItem, error) {
	if s == nil {
		return SourceItem{}, fmt.Errorf("source service unavailable")
	}
	if cfg == nil {
		return SourceItem{}, fmt.Errorf("config cannot be nil")
	}

	normURL, err := config.NormalizeURL(item.URL)
	if err != nil {
		return SourceItem{}, fmt.Errorf("%w: %w", ErrInvalidSourceURL, err)
	}
	item.URL = normURL

	if item.ID == "" {
		item.ID = config.GenerateSourceID(item.URL)
	}

	// Check for duplicate URL or ID
	for _, existing := range cfg.Sources {
		if existing.URL == item.URL {
			return SourceItem{}, fmt.Errorf("%w: %s (existing id: %s)", ErrDuplicateSource, item.URL, existing.ID)
		}
		if existing.ID == item.ID {
			return SourceItem{}, fmt.Errorf("%w: %s", ErrDuplicateSourceID, item.ID)
		}
	}

	if item.Name == "" {
		item.Name = config.DefaultSourceName(item.URL)
	}
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now().UTC()
	}

	cfg.Sources = append(cfg.Sources, item)
	return item, nil
}

// Add adds a new source item, persisting the updated configuration through ConfigService.
func (s *Service) Add(item SourceItem) (SourceItem, error) {
	if s == nil || s.configSvc == nil {
		return SourceItem{}, fmt.Errorf("config service unavailable")
	}

	var created SourceItem
	err := s.configSvc.Update(func(cfg *config.Config) error {
		var err error
		created, err = s.AddToConfig(cfg, item)
		return err
	})
	if err != nil {
		return SourceItem{}, err
	}
	return created, nil
}

// AddURL creates and adds an enabled source from a raw URL.
func (s *Service) AddURL(rawURL string, name ...string) (SourceItem, error) {
	var displayName string
	if len(name) > 0 {
		displayName = name[0]
	}
	return s.Add(SourceItem{
		URL:     rawURL,
		Name:    displayName,
		Enabled: true,
	})
}

// Update mutates an existing source by ID, persisting through ConfigService.
func (s *Service) Update(id string, mutator func(*SourceItem) error) (SourceItem, error) {
	if s == nil || s.configSvc == nil {
		return SourceItem{}, fmt.Errorf("config service unavailable")
	}
	if mutator == nil {
		return SourceItem{}, fmt.Errorf("mutator function cannot be nil")
	}

	var updated SourceItem
	err := s.configSvc.Update(func(cfg *config.Config) error {
		idx := -1
		for i, src := range cfg.Sources {
			if src.ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			return fmt.Errorf("%w: %s", ErrSourceNotFound, id)
		}

		candidate := cfg.Sources[idx]
		origURL := candidate.URL

		if err := mutator(&candidate); err != nil {
			return fmt.Errorf("mutation failed: %w", err)
		}

		// Ensure ID cannot be changed via mutator
		candidate.ID = id

		if candidate.URL != origURL {
			normURL, err := config.NormalizeURL(candidate.URL)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrInvalidSourceURL, err)
			}
			candidate.URL = normURL

			// Ensure new URL doesn't conflict with another source
			for i, existing := range cfg.Sources {
				if i != idx && existing.URL == candidate.URL {
					return fmt.Errorf("%w: %s (existing id: %s)", ErrDuplicateSource, candidate.URL, existing.ID)
				}
			}
		}

		if candidate.Name == "" {
			candidate.Name = config.DefaultSourceName(candidate.URL)
		}

		cfg.Sources[idx] = candidate
		updated = candidate
		return nil
	})
	if err != nil {
		return SourceItem{}, err
	}
	return updated, nil
}

// Remove deletes a source by ID, persisting through ConfigService.
func (s *Service) Remove(id string) error {
	if s == nil || s.configSvc == nil {
		return fmt.Errorf("config service unavailable")
	}

	return s.configSvc.Update(func(cfg *config.Config) error {
		idx := -1
		for i, src := range cfg.Sources {
			if src.ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			return fmt.Errorf("%w: %s", ErrSourceNotFound, id)
		}

		if len(cfg.Sources) <= 1 {
			return ErrCannotRemoveLast
		}

		cfg.Sources = append(cfg.Sources[:idx], cfg.Sources[idx+1:]...)
		return nil
	})
}

// SetEnabled sets the enabled state for a source.
func (s *Service) SetEnabled(id string, enabled bool) error {
	_, err := s.Update(id, func(item *SourceItem) error {
		item.Enabled = enabled
		return nil
	})
	return err
}

// Enable marks a source as enabled.
func (s *Service) Enable(id string) error {
	return s.SetEnabled(id, true)
}

// Disable marks a source as disabled.
func (s *Service) Disable(id string) error {
	return s.SetEnabled(id, false)
}
