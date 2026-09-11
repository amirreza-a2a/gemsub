package publisher_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/publisher"
	"gemsub/internal/store"
)

// mockBackend implements publisher.Backend for testing.
type mockBackend struct {
	mu                  sync.Mutex
	cfg                 config.PublishingConfig
	publishFn           func(ctx context.Context) error
	publishCount        int
	validatePrereqFn    func(ctx context.Context) error
	validatePrereqCount int
}

func (m *mockBackend) Publish(ctx context.Context) error {
	m.mu.Lock()
	m.publishCount++
	fn := m.publishFn
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (m *mockBackend) UpdateConfig(cfg config.PublishingConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
}

func (m *mockBackend) Config() config.PublishingConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *mockBackend) ValidatePrerequisites(ctx context.Context) error {
	m.mu.Lock()
	m.validatePrereqCount++
	fn := m.validatePrereqFn
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func setupConfigSvc(t *testing.T, pubCfg config.PublishingConfig) (*config.Service, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := &config.Config{
		Sources:          config.NewSources("https://example.com/sub"),
		FetchIntervalRaw: "1h",
		Publishing:       pubCfg,
		Test: config.TestConfig{
			TargetURL:    "https://example.com",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	svc, err := config.NewService(cfgPath, cfg, nil)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}
	return svc, cfgPath
}

func TestSanitizeURL_And_Message(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "token in https url",
			input:    "https://ghp_secretToken12345@github.com/user/repo.git",
			expected: "https://***@github.com/user/repo.git",
		},
		{
			name:     "user and pass in https url",
			input:    "https://myuser:mypassword@github.com/user/repo.git",
			expected: "https://***@github.com/user/repo.git",
		},
		{
			name:     "clean https url",
			input:    "https://github.com/user/repo.git",
			expected: "https://github.com/user/repo.git",
		},
		{
			name:     "clean ssh url",
			input:    "git@github.com:user/repo.git",
			expected: "git@github.com:user/repo.git",
		},
		{
			name:     "error message with embedded token url",
			input:    "git push: fatal: Authentication failed for 'https://ghp_secretToken12345@github.com/user/repo.git'",
			expected: "git push: fatal: Authentication failed for 'https://***@github.com/user/repo.git'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := publisher.SanitizeMessage(tc.input)
			if got != tc.expected {
				t.Errorf("SanitizeMessage(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestService_ConfigReadingAndPersistence(t *testing.T) {
	initPub := config.PublishingConfig{
		Enabled:    false,
		Repository: "/tmp/repo",
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, initPub)
	bus := events.New()
	defer bus.Close()
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	svc := publisher.NewService(cfgSvc, st, bus)

	// 1. Initial config matches
	c := svc.Config()
	if c.Enabled != false || c.Repository != "/tmp/repo" || c.Branch != "main" {
		t.Fatalf("unexpected initial config: %+v", c)
	}

	// 2. SetEnabled
	if err := svc.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled failed: %v", err)
	}
	if !svc.Config().Enabled {
		t.Error("expected Enabled=true after SetEnabled(true)")
	}
	if !cfgSvc.Get().Publishing.Enabled {
		t.Error("expected ConfigService to reflect Enabled=true")
	}

	// 3. SetBranch
	if err := svc.SetBranch("deploy"); err != nil {
		t.Fatalf("SetBranch failed: %v", err)
	}
	if svc.Config().Branch != "deploy" {
		t.Errorf("expected Branch=deploy, got %s", svc.Config().Branch)
	}

	// 4. SetRepository
	if err := svc.SetRepository("/var/repo"); err != nil {
		t.Fatalf("SetRepository failed: %v", err)
	}
	if svc.Config().Repository != "/var/repo" {
		t.Errorf("expected Repository=/var/repo, got %s", svc.Config().Repository)
	}

	// 5. SetRemoteURL
	if err := svc.SetRemoteURL("https://github.com/user/newrepo.git"); err != nil {
		t.Fatalf("SetRemoteURL failed: %v", err)
	}
	if svc.Config().RemoteURL != "https://github.com/user/newrepo.git" {
		t.Errorf("expected RemoteURL updated, got %s", svc.Config().RemoteURL)
	}
}

func initGitRepo(t *testing.T, dir string, remoteURL string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
	}
	run("init", "-b", "main")
	run("config", "user.name", "Gemsub Test")
	run("config", "user.email", "test@example.com")
	if remoteURL != "" {
		run("remote", "add", "origin", remoteURL)
	}
}

func TestService_ValidatePrerequisites_CancelledContext(t *testing.T) {
	tempRepo := t.TempDir()
	initGitRepo(t, tempRepo, "https://github.com/user/repo.git")

	pubCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: tempRepo,
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	svc := publisher.NewService(cfgSvc, st, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	err := svc.ValidatePrerequisites(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestService_ValidatePrerequisites_Disabled(t *testing.T) {
	pubCfg := config.PublishingConfig{
		Enabled:    false,
		Repository: "/tmp/repo",
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	svc := publisher.NewService(cfgSvc, st, nil)

	err := svc.ValidatePrerequisites(context.Background())
	if !errors.Is(err, publisher.ErrPublishingDisabled) {
		t.Fatalf("expected ErrPublishingDisabled, got: %v", err)
	}
}

func TestService_ValidatePrerequisites_NonExistentPath(t *testing.T) {
	tempRepo := t.TempDir()
	pubCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: filepath.Join(tempRepo, "nonexistent-dir"),
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	svc := publisher.NewService(cfgSvc, st, nil)

	err := svc.ValidatePrerequisites(context.Background())
	if !errors.Is(err, publisher.ErrRepositoryUnavailable) {
		t.Fatalf("expected ErrRepositoryUnavailable for non-existent path, got: %v", err)
	}
}

func TestService_ValidatePrerequisites_OrdinaryDirectoryNotGit(t *testing.T) {
	tempRepo := t.TempDir() // ordinary directory without git init
	pubCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: tempRepo,
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	svc := publisher.NewService(cfgSvc, st, nil)

	err := svc.ValidatePrerequisites(context.Background())
	if !errors.Is(err, publisher.ErrRepositoryUnavailable) {
		t.Fatalf("expected ErrRepositoryUnavailable for non-git directory, got: %v", err)
	}
}

func TestService_ValidatePrerequisites_InvalidOrMismatchedRemote(t *testing.T) {
	tempRepo := t.TempDir()
	initGitRepo(t, tempRepo, "https://github.com/user/actual-remote.git")

	// 1. Mismatched remote URL
	pubCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: tempRepo,
		Branch:     "main",
		RemoteURL:  "https://github.com/user/expected-remote.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	svc := publisher.NewService(cfgSvc, st, nil)

	err := svc.ValidatePrerequisites(context.Background())
	if !errors.Is(err, publisher.ErrInvalidConfiguration) {
		t.Fatalf("expected ErrInvalidConfiguration on remote mismatch, got: %v", err)
	}

	// 2. Empty remote URL
	_ = svc.SetRemoteURL("")
	err = svc.ValidatePrerequisites(context.Background())
	if !errors.Is(err, publisher.ErrInvalidConfiguration) {
		t.Fatalf("expected ErrInvalidConfiguration on empty remote_url, got: %v", err)
	}
}

func TestService_ValidatePrerequisites_ValidGitRepository(t *testing.T) {
	tempRepo := t.TempDir()
	initGitRepo(t, tempRepo, "https://github.com/user/repo.git")

	pubCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: tempRepo,
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	svc := publisher.NewService(cfgSvc, st, nil)

	err := svc.ValidatePrerequisites(context.Background())
	if err != nil {
		t.Fatalf("expected valid git repository prerequisites to succeed, got: %v", err)
	}
}

func TestService_Publish_SuccessWithEvents(t *testing.T) {
	pubCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: "/tmp/repo",
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	bus := events.New()
	defer bus.Close()
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	mock := &mockBackend{}
	svc := publisher.NewService(cfgSvc, st, bus, mock)

	var startedEvents []events.PublishingStarted
	var finishedEvents []events.PublishingFinished
	var mu sync.Mutex

	subCh := bus.Subscribe()
	defer bus.Unsubscribe(subCh)

	go func() {
		for raw := range subCh {
			mu.Lock()
			switch evt := raw.(type) {
			case events.PublishingStarted:
				startedEvents = append(startedEvents, evt)
			case events.PublishingFinished:
				finishedEvents = append(finishedEvents, evt)
			}
			mu.Unlock()
		}
	}()

	ctx := context.Background()
	if err := svc.Publish(ctx); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	if mock.publishCount != 1 {
		t.Errorf("expected mock.publishCount=1, got %d", mock.publishCount)
	}

	// Verify status
	status := svc.Status()
	if status.Running {
		t.Error("expected Running=false after publish finished")
	}
	if status.PublishCount != 1 {
		t.Errorf("expected PublishCount=1, got %d", status.PublishCount)
	}
	if status.FailCount != 0 {
		t.Errorf("expected FailCount=0, got %d", status.FailCount)
	}
	if status.LastPublished.IsZero() {
		t.Error("expected non-zero LastPublished")
	}
	if status.LastError != "" {
		t.Errorf("expected empty LastError, got %q", status.LastError)
	}

	// Wait briefly for event delivery
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		gotAll := len(startedEvents) == 1 && len(finishedEvents) == 1
		mu.Unlock()
		if gotAll {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(startedEvents) != 1 {
		t.Fatalf("expected 1 PublishingStarted event, got %d", len(startedEvents))
	}
	if startedEvents[0].Branch != "main" || startedEvents[0].Repository != "/tmp/repo" {
		t.Errorf("unexpected PublishingStarted event payload: %+v", startedEvents[0])
	}

	if len(finishedEvents) != 1 {
		t.Fatalf("expected 1 PublishingFinished event, got %d", len(finishedEvents))
	}
	if finishedEvents[0].Branch != "main" || finishedEvents[0].Repository != "/tmp/repo" {
		t.Errorf("unexpected PublishingFinished event payload: %+v", finishedEvents[0])
	}
}

func TestService_Publish_Disabled(t *testing.T) {
	pubCfg := config.PublishingConfig{
		Enabled:    false,
		Repository: "/tmp/repo",
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	bus := events.New()
	defer bus.Close()
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	mock := &mockBackend{}
	svc := publisher.NewService(cfgSvc, st, bus, mock)

	// Publish is a safe no-op returning nil when disabled
	err := svc.Publish(context.Background())
	if err != nil {
		t.Fatalf("expected nil on disabled Publish, got: %v", err)
	}
	if mock.publishCount != 0 {
		t.Errorf("expected backend not to be called, got count %d", mock.publishCount)
	}

	// ValidatePrerequisites explicitly reports ErrPublishingDisabled when disabled
	if err := svc.ValidatePrerequisites(context.Background()); !errors.Is(err, publisher.ErrPublishingDisabled) {
		t.Fatalf("expected ErrPublishingDisabled from ValidatePrerequisites, got: %v", err)
	}
}

func TestService_Publish_Failure_MasksSecretsAndEmitsEvent(t *testing.T) {
	secretToken := "ghp_superSecretToken999"
	pubCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: "/tmp/repo",
		Branch:     "main",
		RemoteURL:  "https://" + secretToken + "@github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	bus := events.New()
	defer bus.Close()
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	mock := &mockBackend{
		publishFn: func(ctx context.Context) error {
			return errors.New("fatal: Authentication failed for 'https://" + secretToken + "@github.com/user/repo.git'")
		},
	}
	svc := publisher.NewService(cfgSvc, st, bus, mock)

	var failedEvents []events.PublishingFailed
	var mu sync.Mutex

	subCh := bus.Subscribe()
	defer bus.Unsubscribe(subCh)

	go func() {
		for raw := range subCh {
			if evt, ok := raw.(events.PublishingFailed); ok {
				mu.Lock()
				failedEvents = append(failedEvents, evt)
				mu.Unlock()
			}
		}
	}()

	err := svc.Publish(context.Background())
	if err == nil {
		t.Fatal("expected publish to return error, got nil")
	}

	// 1. Check typed error wrapping
	if !errors.Is(err, publisher.ErrAuthenticationFailed) && !errors.Is(err, publisher.ErrGitOperationFailed) {
		t.Errorf("expected error to wrap ErrAuthenticationFailed or ErrGitOperationFailed, got: %v", err)
	}

	// 2. Secret must NEVER appear in error message
	if strings.Contains(err.Error(), secretToken) {
		t.Fatalf("secret token leaked in returned error: %v", err)
	}

	// 3. Status must not leak secret
	status := svc.Status()
	if status.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", status.FailCount)
	}
	if strings.Contains(status.LastError, secretToken) {
		t.Fatalf("secret token leaked in Status.LastError: %q", status.LastError)
	}
	if strings.Contains(status.RemoteURL, secretToken) {
		t.Fatalf("secret token leaked in Status.RemoteURL: %q", status.RemoteURL)
	}

	// 4. Event must be received and must not leak secret
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		hasEvent := len(failedEvents) > 0
		mu.Unlock()
		if hasEvent {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(failedEvents) != 1 {
		t.Fatalf("expected 1 PublishingFailed event, got %d", len(failedEvents))
	}
	if strings.Contains(failedEvents[0].Error, secretToken) {
		t.Fatalf("secret token leaked in PublishingFailed event error string: %q", failedEvents[0].Error)
	}
}

func TestService_Publish_ConcurrentCalls_SingleFlight(t *testing.T) {
	pubCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: "/tmp/repo",
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	bus := events.New()
	defer bus.Close()
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	started := make(chan struct{})
	finish := make(chan struct{})

	mock := &mockBackend{
		publishFn: func(ctx context.Context) error {
			close(started)
			<-finish
			return nil
		},
	}
	svc := publisher.NewService(cfgSvc, st, bus, mock)

	var wg sync.WaitGroup
	var err1, err2 error

	wg.Add(1)
	go func() {
		defer wg.Done()
		err1 = svc.Publish(context.Background())
	}()

	<-started

	// Second concurrent publish while first is active
	err2 = svc.Publish(context.Background())
	if !errors.Is(err2, publisher.ErrPublishingAlreadyRunning) {
		t.Fatalf("expected ErrPublishingAlreadyRunning for concurrent call, got: %v", err2)
	}

	close(finish)
	wg.Wait()

	if err1 != nil {
		t.Fatalf("expected first publish to succeed, got: %v", err1)
	}
}

func TestService_Publish_ContextCancellation(t *testing.T) {
	pubCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: "/tmp/repo",
		Branch:     "main",
		RemoteURL:  "https://github.com/user/repo.git",
	}
	cfgSvc, _ := setupConfigSvc(t, pubCfg)
	bus := events.New()
	defer bus.Close()
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	mock := &mockBackend{}
	svc := publisher.NewService(cfgSvc, st, bus, mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	err := svc.Publish(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on pre-cancelled publish, got: %v", err)
	}
	if mock.publishCount != 0 {
		t.Errorf("expected backend not called for cancelled context, got %d", mock.publishCount)
	}
}

func TestService_StandaloneMode_UsesLocalConfiguration(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	bus := events.New()
	defer bus.Close()

	mock := &mockBackend{}
	// Construct with configSvc == nil (standalone in-memory mode)
	svc := publisher.NewService(nil, st, bus, mock)

	// 1. Initial state is empty and disabled
	if svc.Config().Enabled {
		t.Fatal("expected standalone service to default to disabled")
	}

	// 2. Publish when disabled is a safe no-op
	if err := svc.Publish(context.Background()); err != nil {
		t.Fatalf("expected nil on disabled standalone publish, got: %v", err)
	}
	if mock.publishCount != 0 {
		t.Fatalf("expected mock not called when disabled, got count %d", mock.publishCount)
	}

	// 3. Configure local settings via Service mutation APIs
	localCfg := config.PublishingConfig{
		Enabled:    true,
		Repository: "/standalone/repo",
		Branch:     "standalone-branch",
		RemoteURL:  "https://github.com/standalone/repo.git",
	}
	if err := svc.SetConfig(localCfg); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// 4. Verify local configuration is reflected in Config() and Status()
	c := svc.Config()
	if !c.Enabled || c.Branch != "standalone-branch" || c.Repository != "/standalone/repo" {
		t.Fatalf("unexpected standalone Config(): %+v", c)
	}

	status := svc.Status()
	if !status.Enabled || status.Branch != "standalone-branch" || status.Repository != "/standalone/repo" {
		t.Fatalf("unexpected standalone Status(): %+v", status)
	}

	// 5. Publish uses local configuration and synchronizes backend
	if err := svc.Publish(context.Background()); err != nil {
		t.Fatalf("Publish failed in standalone mode: %v", err)
	}

	if mock.publishCount != 1 {
		t.Fatalf("expected backend publish count 1, got %d", mock.publishCount)
	}
	if mock.cfg.Branch != "standalone-branch" || mock.cfg.Repository != "/standalone/repo" || !mock.cfg.Enabled {
		t.Fatalf("expected backend to receive local configuration, got: %+v", mock.cfg)
	}

	// 6. Disable locally and verify Publish respects local disabled state
	if err := svc.SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled(false) failed: %v", err)
	}
	if err := svc.Publish(context.Background()); err != nil {
		t.Fatalf("Publish after disable failed: %v", err)
	}
	if mock.publishCount != 1 {
		t.Fatalf("expected backend not called after disable, count remained %d", mock.publishCount)
	}
}
