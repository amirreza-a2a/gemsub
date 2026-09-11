package publisher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/store"
)

var (
	// ErrPublishingDisabled is returned when attempting to publish while publishing is disabled.
	ErrPublishingDisabled = errors.New("publishing is disabled")

	// ErrPublishingAlreadyRunning is returned when a publication is already in progress.
	ErrPublishingAlreadyRunning = errors.New("publication already in progress")

	// ErrInvalidConfiguration is returned when publishing configuration is invalid or missing.
	ErrInvalidConfiguration = errors.New("invalid publishing configuration")

	// ErrRepositoryUnavailable is returned when the target repository is missing or inaccessible.
	ErrRepositoryUnavailable = errors.New("publishing repository unavailable")

	// ErrAuthenticationFailed is returned when git authentication fails during publication.
	ErrAuthenticationFailed = errors.New("publishing authentication failed")

	// ErrGitOperationFailed is returned when a git command fails during publication.
	ErrGitOperationFailed = errors.New("git operation failed")
)

var userinfoRegex = regexp.MustCompile(`(?i)(https?://)([^@/\s]+)@`)

// SanitizeMessage masks credentials or tokens in URLs embedded within strings or error messages.
func SanitizeMessage(msg string) string {
	if msg == "" {
		return ""
	}
	return userinfoRegex.ReplaceAllString(msg, "${1}***@")
}

// SanitizeURL masks credentials/tokens in a raw URL string.
func SanitizeURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	return userinfoRegex.ReplaceAllString(rawURL, "${1}***@")
}

// Status contains an isolated snapshot of publication status and metrics.
type Status struct {
	Enabled       bool          `json:"enabled"`
	Running       bool          `json:"running"`
	Repository    string        `json:"repository"`
	Branch        string        `json:"branch"`
	RemoteURL     string        `json:"remote_url"` // Sanitized
	LastPublished time.Time     `json:"last_published,omitempty"`
	LastDuration  time.Duration `json:"last_duration,omitempty"`
	LastError     string        `json:"last_error,omitempty"` // Sanitized
	PublishCount  int           `json:"publish_count"`
	FailCount     int           `json:"fail_count"`
}

// Backend abstracts subscription generation, git operations, and repository validation.
type Backend interface {
	Publish(ctx context.Context) error
	UpdateConfig(cfg config.PublishingConfig)
	ValidatePrerequisites(ctx context.Context) error
}

// Service coordinates publication configuration, prerequisites validation, and execution.
// It acts as the boundary between presentation/application layers and publication internals.
type Service struct {
	mu            sync.Mutex
	configSvc     *config.Service
	st            *store.Store
	bus           *events.EventBus
	backend       Backend
	publishing    bool
	cfg           config.PublishingConfig
	lastPublished time.Time
	lastDuration  time.Duration
	lastError     error
	publishCount  int
	failCount     int
}

// PublishingService is an alias for Service.
type PublishingService = Service

// NewService creates a new publishing lifecycle service.
//
// Configuration authority contract:
//  1. When configSvc is non-nil (standard production runtime), ConfigService is the sole
//     authoritative configuration source. All reads query ConfigService directly, and
//     command-side mutations persist through ConfigService.
//  2. When configSvc is nil (standalone in-memory mode), the Service maintains its own
//     isolated, service-local configuration. In this mode, it is intentionally standalone
//     and does not participate in external runtime ConfigUpdated events.
func NewService(configSvc *config.Service, st *store.Store, bus *events.EventBus, backend ...Backend) *Service {
	var pubCfg config.PublishingConfig
	if configSvc != nil {
		pubCfg = configSvc.Get().Publishing
	}
	var b Backend
	if len(backend) > 0 && backend[0] != nil {
		b = backend[0]
	} else {
		b = New(&pubCfg, st)
	}

	return &Service{
		configSvc: configSvc,
		st:        st,
		bus:       bus,
		backend:   b,
		cfg:       pubCfg,
	}
}

// Config returns an isolated snapshot copy of current publishing configuration.
func (s *Service) Config() config.PublishingConfig {
	if s == nil {
		return config.PublishingConfig{}
	}
	if s.configSvc != nil {
		return s.configSvc.Get().Publishing
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// UpdateConfig mutates publishing configuration atomically via ConfigService.
func (s *Service) UpdateConfig(updateFn func(*config.PublishingConfig) error) error {
	if s == nil {
		return errors.New("publishing service unavailable")
	}
	if s.configSvc != nil {
		err := s.configSvc.Update(func(cfg *config.Config) error {
			return updateFn(&cfg.Publishing)
		})
		if err != nil {
			return err
		}
		current := s.configSvc.Get().Publishing
		s.mu.Lock()
		s.cfg = current
		if s.backend != nil {
			s.backend.UpdateConfig(current)
		}
		s.mu.Unlock()
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cloned := s.cfg
	if err := updateFn(&cloned); err != nil {
		return err
	}
	s.cfg = cloned
	if s.backend != nil {
		s.backend.UpdateConfig(cloned)
	}
	return nil
}

// SetEnabled enables or disables subscription publishing.
func (s *Service) SetEnabled(enabled bool) error {
	return s.UpdateConfig(func(c *config.PublishingConfig) error {
		c.Enabled = enabled
		return nil
	})
}

// SetBranch sets the target publishing git branch.
func (s *Service) SetBranch(branch string) error {
	return s.UpdateConfig(func(c *config.PublishingConfig) error {
		c.Branch = branch
		return nil
	})
}

// SetRepository sets the target publishing git repository directory.
func (s *Service) SetRepository(repo string) error {
	return s.UpdateConfig(func(c *config.PublishingConfig) error {
		c.Repository = repo
		return nil
	})
}

// SetRemoteURL sets the expected remote origin URL.
func (s *Service) SetRemoteURL(remoteURL string) error {
	return s.UpdateConfig(func(c *config.PublishingConfig) error {
		c.RemoteURL = remoteURL
		return nil
	})
}

// SetConfig replaces publishing configuration atomically.
func (s *Service) SetConfig(cfg config.PublishingConfig) error {
	return s.UpdateConfig(func(c *config.PublishingConfig) error {
		*c = cfg
		return nil
	})
}

// ValidatePrerequisites verifies the real prerequisites needed before publication:
// - context cancellation
// - publishing is enabled
// - repository path exists and is a directory
// - repository is actually a git repository
// - configured remote information matches origin
// - branch is configured
// Does not mutate repository state or perform git commit/push.
func (s *Service) ValidatePrerequisites(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	cfg := s.Config()
	if !cfg.Enabled {
		return ErrPublishingDisabled
	}
	repoPath := ExpandHome(cfg.Repository)
	if strings.TrimSpace(repoPath) == "" {
		return fmt.Errorf("%w: repository path is empty", ErrInvalidConfiguration)
	}
	if strings.TrimSpace(cfg.RemoteURL) == "" {
		return fmt.Errorf("%w: remote_url is required", ErrInvalidConfiguration)
	}

	info, err := os.Stat(repoPath)
	if err != nil {
		return fmt.Errorf("%w: repository path %q inaccessible: %v", ErrRepositoryUnavailable, repoPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: repository path %q is not a directory", ErrRepositoryUnavailable, repoPath)
	}

	if s.backend != nil {
		s.backend.UpdateConfig(cfg)
		if err := s.backend.ValidatePrerequisites(ctx); err != nil {
			return classifyAndSanitizeError(err)
		}
	}

	return nil
}

// Publish executes subscription publishing, ensuring single-flight serialization and event publication.
// When publishing is disabled in configuration, it returns nil immediately as a safe no-op.
func (s *Service) Publish(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	cfg := s.Config()
	if !cfg.Enabled {
		return nil
	}

	s.mu.Lock()
	if s.publishing {
		s.mu.Unlock()
		return ErrPublishingAlreadyRunning
	}
	if s.backend != nil {
		s.backend.UpdateConfig(cfg)
	}
	s.publishing = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.publishing = false
		s.mu.Unlock()
	}()

	start := time.Now()
	cleanRepo := SanitizeMessage(cfg.Repository)

	if s.bus != nil {
		s.bus.Publish(events.PublishingStarted{
			StartedAt:  start,
			Repository: cleanRepo,
			Branch:     cfg.Branch,
		})
	}

	var pubErr error
	if s.backend != nil {
		pubErr = s.backend.Publish(ctx)
	}

	dur := time.Since(start)
	if pubErr != nil {
		sanitizedErr := classifyAndSanitizeError(pubErr)
		s.mu.Lock()
		s.lastDuration = dur
		s.lastError = sanitizedErr
		s.failCount++
		s.mu.Unlock()

		if s.bus != nil {
			s.bus.Publish(events.PublishingFailed{
				StartedAt:  start,
				FailedAt:   time.Now(),
				Duration:   dur,
				Repository: cleanRepo,
				Branch:     cfg.Branch,
				Error:      sanitizedErr.Error(),
			})
		}
		return sanitizedErr
	}

	s.mu.Lock()
	s.lastPublished = time.Now()
	s.lastDuration = dur
	s.lastError = nil
	s.publishCount++
	s.mu.Unlock()

	if s.bus != nil {
		s.bus.Publish(events.PublishingFinished{
			StartedAt:  start,
			FinishedAt: time.Now(),
			Duration:   dur,
			Repository: cleanRepo,
			Branch:     cfg.Branch,
		})
	}

	return nil
}

// IsPublishing returns whether a publication operation is actively in progress.
func (s *Service) IsPublishing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishing
}

// Status returns an isolated snapshot of publishing status, metrics, and sanitized error.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.cfg
	if s.configSvc != nil {
		cfg = s.configSvc.Get().Publishing
	}

	var lastErrStr string
	if s.lastError != nil {
		lastErrStr = SanitizeMessage(s.lastError.Error())
	}

	return Status{
		Enabled:       cfg.Enabled,
		Running:       s.publishing,
		Repository:    SanitizeMessage(cfg.Repository),
		Branch:        cfg.Branch,
		RemoteURL:     SanitizeURL(cfg.RemoteURL),
		LastPublished: s.lastPublished,
		LastDuration:  s.lastDuration,
		LastError:     lastErrStr,
		PublishCount:  s.publishCount,
		FailCount:     s.failCount,
	}
}

type sanitizedError struct {
	sentinel error
	msg      string
}

func (e *sanitizedError) Error() string {
	return e.msg
}

func (e *sanitizedError) Unwrap() error {
	return e.sentinel
}

func classifyAndSanitizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	msg := SanitizeMessage(err.Error())

	// 1. If already typed with a sentinel error, preserve sentinel and sanitize text
	if errors.Is(err, ErrPublishingDisabled) {
		return &sanitizedError{sentinel: ErrPublishingDisabled, msg: msg}
	}
	if errors.Is(err, ErrPublishingAlreadyRunning) {
		return ErrPublishingAlreadyRunning
	}
	if errors.Is(err, ErrInvalidConfiguration) {
		return &sanitizedError{sentinel: ErrInvalidConfiguration, msg: msg}
	}
	if errors.Is(err, ErrRepositoryUnavailable) {
		return &sanitizedError{sentinel: ErrRepositoryUnavailable, msg: msg}
	}
	if errors.Is(err, ErrAuthenticationFailed) {
		return &sanitizedError{sentinel: ErrAuthenticationFailed, msg: msg}
	}
	if errors.Is(err, ErrGitOperationFailed) {
		return &sanitizedError{sentinel: ErrGitOperationFailed, msg: msg}
	}

	// 2. Fallback classification for raw git command output
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "authentication failed") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "could not read username"):
		return &sanitizedError{sentinel: ErrAuthenticationFailed, msg: msg}
	case strings.Contains(lower, "not a valid git repository") ||
		strings.Contains(lower, "repository path") ||
		strings.Contains(lower, "inaccessible"):
		return &sanitizedError{sentinel: ErrRepositoryUnavailable, msg: msg}
	case strings.Contains(lower, "remote origin url mismatch") ||
		strings.Contains(lower, "remote_url is required") ||
		strings.Contains(lower, "repository path is empty") ||
		strings.Contains(lower, "branch is required"):
		return &sanitizedError{sentinel: ErrInvalidConfiguration, msg: msg}
	case strings.Contains(lower, "git "):
		return &sanitizedError{sentinel: ErrGitOperationFailed, msg: msg}
	default:
		return errors.New(msg)
	}
}
