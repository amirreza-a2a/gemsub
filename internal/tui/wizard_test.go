package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/logging"
	"gemsub/internal/source"
	"gemsub/internal/store"
	"gemsub/internal/tui/adapter"
	"gemsub/internal/tui/viewmodel"
)

// mockWizardController implements Controller for wizard tests.
type mockWizardController struct {
	completeOnboardingFn    func(cfg viewmodel.OnboardingConfig) error
	lastOnboardingConfig    viewmodel.OnboardingConfig
	completeOnboardingCalls int
	startRuntimeFn          func() error
	startRuntimeCalls       int
}

func (m *mockWizardController) Snapshot(filter viewmodel.FilterMode) viewmodel.SnapshotViewModel {
	return viewmodel.SnapshotViewModel{}
}
func (m *mockWizardController) PollSnapshot(filter viewmodel.FilterMode) (viewmodel.SnapshotViewModel, bool) {
	return viewmodel.SnapshotViewModel{}, false
}
func (m *mockWizardController) CandidateDetail(opaqueID string) (viewmodel.CandidateDetailViewModel, bool) {
	return viewmodel.CandidateDetailViewModel{}, false
}
func (m *mockWizardController) CycleLogLevel() (viewmodel.LogViewModel, string) {
	return viewmodel.LogViewModel{}, ""
}
func (m *mockWizardController) CopyCandidateLink(opaqueID string) error {
	return nil
}
func (m *mockWizardController) CandidateRowsWindow(filter viewmodel.FilterMode, offset, limit int) []viewmodel.CandidateRowViewModel {
	return nil
}
func (m *mockWizardController) ConfigCenter() viewmodel.ConfigCenterViewModel {
	return viewmodel.ConfigCenterViewModel{}
}
func (m *mockWizardController) ToggleSource(id string) error {
	return nil
}
func (m *mockWizardController) AddSource(rawURL string, name string) error {
	return nil
}
func (m *mockWizardController) UpdateSource(id string, rawURL string, name string) error {
	return nil
}
func (m *mockWizardController) DeleteSource(id string) error {
	return nil
}
func (m *mockWizardController) UpdateSetting(key string, value string) error {
	return nil
}
func (m *mockWizardController) PauseScheduler() error {
	return nil
}
func (m *mockWizardController) ResumeScheduler() error {
	return nil
}
func (m *mockWizardController) TriggerCycleNow() error {
	return nil
}
func (m *mockWizardController) TestPublishing(ctx context.Context) error {
	return nil
}
func (m *mockWizardController) PublishNow(ctx context.Context) error {
	return nil
}
func (m *mockWizardController) CompleteOnboarding(cfg viewmodel.OnboardingConfig) error {
	m.completeOnboardingCalls++
	m.lastOnboardingConfig = cfg
	if m.completeOnboardingFn != nil {
		return m.completeOnboardingFn(cfg)
	}
	return nil
}

func (m *mockWizardController) StartRuntime() error {
	m.startRuntimeCalls++
	if m.startRuntimeFn != nil {
		return m.startRuntimeFn()
	}
	return nil
}

func setupWizardModel(ctrl Controller) *Model {
	m := New(ctrl)
	m.SetView(ViewWizard)
	return m
}

func sendRune(m *Model, r rune) (*Model, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{r},
	})
	return updated.(*Model), cmd
}

func sendKey(m *Model, keyType tea.KeyType, str string) (*Model, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{
		Type:  keyType,
		Runes: []rune(str),
	})
	return updated.(*Model), cmd
}

func TestWizard_Validation_Source(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"empty URL", "", true},
		{"whitespace URL", "   ", true},
		{"missing scheme", "example.com/subs.txt", true},
		{"unsupported scheme ftp", "ftp://example.com/subs.txt", true},
		{"valid http URL", "http://example.com/subs.txt", false},
		{"valid https URL", "https://example.com/subs.txt", false},
		{"valid https with params", "https://example.com/sub?token=123", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSource(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSource(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestWizard_Validation_Subserver(t *testing.T) {
	tests := []struct {
		name    string
		listen  string
		path    string
		wantErr bool
	}{
		{"empty listen", "", "/sub", true},
		{"missing port", "127.0.0.1", "/sub", true},
		{"port 0", "127.0.0.1:0", "/sub", true},
		{"port too high", "127.0.0.1:70000", "/sub", true},
		{"non-numeric port", "127.0.0.1:abc", "/sub", true},
		{"empty path", "127.0.0.1:8765", "", true},
		{"path missing slash", "127.0.0.1:8765", "sub", true},
		{"valid listen and path", "127.0.0.1:8765", "/sub", false},
		{"valid all-interfaces", "0.0.0.0:8080", "/v1/sub", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubserver(tt.listen, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSubserver(%q, %q) error = %v, wantErr %v", tt.listen, tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestWizard_Validation_Testing(t *testing.T) {
	tests := []struct {
		name        string
		concurrency string
		timeout     string
		targetURL   string
		wantErr     bool
	}{
		{"non-numeric concurrency", "abc", "10s", "https://gemini.google.com/", true},
		{"zero concurrency", "0", "10s", "https://gemini.google.com/", true},
		{"negative concurrency", "-5", "10s", "https://gemini.google.com/", true},
		{"invalid timeout format", "20", "invalid", "https://gemini.google.com/", true},
		{"zero timeout", "20", "0s", "https://gemini.google.com/", true},
		{"negative timeout", "20", "-2s", "https://gemini.google.com/", true},
		{"empty target URL", "20", "10s", "", true},
		{"non-http scheme target", "20", "10s", "ftp://gemini.google.com/", true},
		{"target missing host", "20", "10s", "https://", true},
		{"valid testing parameters", "20", "10s", "https://gemini.google.com/", false},
		{"valid with custom target", "50", "15s", "http://localhost:8080/check", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateTesting(tt.concurrency, tt.timeout, tt.targetURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTesting(%q, %q, %q) error = %v, wantErr %v",
					tt.concurrency, tt.timeout, tt.targetURL, err, tt.wantErr)
			}
		})
	}
}

func TestWizard_Navigation_FullFlow(t *testing.T) {
	mockCtrl := &mockWizardController{}
	m := setupWizardModel(mockCtrl)

	// Step 1: Welcome
	if m.wizard.Step != WizardStepWelcome {
		t.Fatalf("expected initial step Welcome, got %v", m.wizard.Step)
	}

	// Press Enter to advance to Step 2: Source
	m, _ = sendKey(m, tea.KeyEnter, "enter")
	if m.wizard.Step != WizardStepSource {
		t.Fatalf("expected step Source, got %v", m.wizard.Step)
	}

	// Press Esc to return to Step 1
	m, _ = sendKey(m, tea.KeyEsc, "esc")
	if m.wizard.Step != WizardStepWelcome {
		t.Fatalf("expected step Welcome after Esc, got %v", m.wizard.Step)
	}

	// Press Space to advance to Step 2 again
	m, _ = sendKey(m, tea.KeySpace, " ")
	if m.wizard.Step != WizardStepSource {
		t.Fatalf("expected step Source, got %v", m.wizard.Step)
	}

	// Step 2: Press Enter with empty source URL -> should show error and stay on Step 2
	m, _ = sendKey(m, tea.KeyEnter, "enter")
	if m.wizard.Step != WizardStepSource {
		t.Errorf("expected to stay on Step 2 when URL is empty, got %v", m.wizard.Step)
	}
	if m.wizard.sourceErr == "" {
		t.Error("expected sourceErr when URL is empty")
	}

	// Type valid URL
	sourceURL := "https://example.com/subs.txt"
	for _, r := range sourceURL {
		m, _ = sendRune(m, r)
	}
	if m.wizard.SourceURL != sourceURL {
		t.Errorf("expected SourceURL %q, got %q", sourceURL, m.wizard.SourceURL)
	}

	// Switch focus to SourceName
	m, _ = sendKey(m, tea.KeyTab, "tab")
	if m.wizard.sourceFocus != 1 {
		t.Errorf("expected sourceFocus == 1 after Tab, got %d", m.wizard.sourceFocus)
	}

	// Type alias
	sourceName := "MyFeed"
	for _, r := range sourceName {
		m, _ = sendRune(m, r)
	}
	if m.wizard.SourceName != sourceName {
		t.Errorf("expected SourceName %q, got %q", sourceName, m.wizard.SourceName)
	}

	// Press Enter to advance to Step 3: Subserver
	m, _ = sendKey(m, tea.KeyEnter, "enter")
	if m.wizard.Step != WizardStepSubserver {
		t.Fatalf("expected step Subserver, got %v (err: %s)", m.wizard.Step, m.wizard.sourceErr)
	}
	if m.wizard.sourceErr != "" {
		t.Errorf("expected sourceErr cleared, got %q", m.wizard.sourceErr)
	}

	// Step 3: Check defaults
	if m.wizard.SubserverListen != "127.0.0.1:8765" {
		t.Errorf("expected default listen 127.0.0.1:8765, got %s", m.wizard.SubserverListen)
	}
	if m.wizard.SubserverPath != "/sub" {
		t.Errorf("expected default path /sub, got %s", m.wizard.SubserverPath)
	}

	// Press Esc to return to Step 2 and verify entered data preserved
	m, _ = sendKey(m, tea.KeyEsc, "esc")
	if m.wizard.Step != WizardStepSource {
		t.Fatalf("expected step Source after Esc, got %v", m.wizard.Step)
	}
	if m.wizard.SourceURL != sourceURL || m.wizard.SourceName != sourceName {
		t.Errorf("expected preserved inputs in Step 2: URL=%s, Name=%s", m.wizard.SourceURL, m.wizard.SourceName)
	}

	// Re-advance to Step 3
	m, _ = sendKey(m, tea.KeyEnter, "enter")
	if m.wizard.Step != WizardStepSubserver {
		t.Fatalf("expected step Subserver, got %v", m.wizard.Step)
	}

	// Advance from Step 3 to Step 4 with valid defaults
	m, _ = sendKey(m, tea.KeyEnter, "enter")
	if m.wizard.Step != WizardStepTesting {
		t.Fatalf("expected step Testing, got %v (err: %s)", m.wizard.Step, m.wizard.subserverErr)
	}

	// Step 4: Check defaults
	if m.wizard.Concurrency != "20" {
		t.Errorf("expected default concurrency 20, got %s", m.wizard.Concurrency)
	}
	if m.wizard.Timeout != "10s" {
		t.Errorf("expected default timeout 10s, got %s", m.wizard.Timeout)
	}
	if m.wizard.TargetURL != "https://gemini.google.com/" {
		t.Errorf("expected default target URL, got %s", m.wizard.TargetURL)
	}

	// Advance to Step 5: Review
	m, _ = sendKey(m, tea.KeyEnter, "enter")
	if m.wizard.Step != WizardStepReview {
		t.Fatalf("expected step Review, got %v (err: %s)", m.wizard.Step, m.wizard.testingErr)
	}

	// Verify Review rendering includes entered values
	rendered := m.renderWizard()
	if !strings.Contains(rendered, sourceURL) {
		t.Errorf("expected Review rendering to contain %q", sourceURL)
	}
	if !strings.Contains(rendered, sourceName) {
		t.Errorf("expected Review rendering to contain %q", sourceName)
	}
	if !strings.Contains(rendered, "127.0.0.1:8765") {
		t.Errorf("expected Review rendering to contain listen address")
	}

	// Press Esc from Review to return to Step 4
	m, _ = sendKey(m, tea.KeyEsc, "esc")
	if m.wizard.Step != WizardStepTesting {
		t.Fatalf("expected step Testing after Esc from Review, got %v", m.wizard.Step)
	}

	// Return to Step 5
	m, _ = sendKey(m, tea.KeyEnter, "enter")
	if m.wizard.Step != WizardStepReview {
		t.Fatalf("expected step Review, got %v", m.wizard.Step)
	}

	// Press Enter to confirm and save
	var saveCmd tea.Cmd
	m, saveCmd = sendKey(m, tea.KeyEnter, "enter")
	if !m.wizard.saving {
		t.Error("expected wizard.saving == true during save")
	}
	if saveCmd == nil {
		t.Fatal("expected non-nil tea.Cmd for saving")
	}

	// Execute save command
	msg := saveCmd()
	savedMsg, ok := msg.(onboardingSavedMsg)
	if !ok {
		t.Fatalf("expected onboardingSavedMsg, got %T", msg)
	}
	if savedMsg.err != nil {
		t.Fatalf("save returned error: %v", savedMsg.err)
	}

	// Dispatch onboardingSavedMsg to model
	updated, _ := m.Update(savedMsg)
	m = updated.(*Model)

	// Invariant: After onboarding completion, activeView transitions to ViewCandidates
	if m.ActiveView() != ViewCandidates {
		t.Errorf("expected ActiveView == ViewCandidates after save, got %v", m.ActiveView())
	}
	if m.wizard.saving {
		t.Error("expected wizard.saving == false after completion")
	}

	// Verify controller received the correct config
	if mockCtrl.lastOnboardingConfig.SourceURL != sourceURL {
		t.Errorf("expected controller to receive sourceURL %q, got %q", sourceURL, mockCtrl.lastOnboardingConfig.SourceURL)
	}
	if mockCtrl.lastOnboardingConfig.SourceName != sourceName {
		t.Errorf("expected controller to receive sourceName %q, got %q", sourceName, mockCtrl.lastOnboardingConfig.SourceName)
	}
	if mockCtrl.lastOnboardingConfig.Concurrency != 20 {
		t.Errorf("expected controller to receive concurrency 20, got %d", mockCtrl.lastOnboardingConfig.Concurrency)
	}
}

func TestWizard_SaveFailure(t *testing.T) {
	expectedErr := errors.New("permission denied")
	mockCtrl := &mockWizardController{
		completeOnboardingFn: func(cfg viewmodel.OnboardingConfig) error {
			return expectedErr
		},
	}
	m := setupWizardModel(mockCtrl)
	m.wizard.Step = WizardStepReview
	m.wizard.SourceURL = "https://example.com/subs.txt"

	// Press Enter to trigger save
	m, saveCmd := sendKey(m, tea.KeyEnter, "enter")
	if saveCmd == nil {
		t.Fatal("expected saveCmd != nil")
	}

	msg := saveCmd()
	updated, _ := m.Update(msg)
	m = updated.(*Model)

	// On failure, wizard should stay in ViewWizard on Step 5 and display the error
	if m.ActiveView() != ViewWizard {
		t.Errorf("expected ViewWizard on save error, got %v", m.ActiveView())
	}
	if m.wizard.Step != WizardStepReview {
		t.Errorf("expected Step Review on save error, got %v", m.wizard.Step)
	}
	if m.wizard.saving {
		t.Error("expected wizard.saving == false after failure")
	}
	if !strings.Contains(m.wizard.saveError, "permission denied") {
		t.Errorf("expected saveError to contain %q, got %q", "permission denied", m.wizard.saveError)
	}

	// Verify error is rendered in View
	view := m.View()
	if !strings.Contains(view, "permission denied") {
		t.Errorf("expected View() to render save error, got:\n%s", view)
	}
}

func TestWizard_StepEditingAndBackspace(t *testing.T) {
	mockCtrl := &mockWizardController{}
	m := setupWizardModel(mockCtrl)

	// Go to Step 2: Source
	m.wizard.Step = WizardStepSource
	m.wizard.sourceFocus = 0
	m.wizard.SourceURL = "https://foo.com"

	// Send backspace
	m, _ = sendKey(m, tea.KeyBackspace, "backspace")
	if m.wizard.SourceURL != "https://foo.co" {
		t.Errorf("expected %q after backspace, got %q", "https://foo.co", m.wizard.SourceURL)
	}

	// Switch focus to name and backspace
	m.wizard.sourceFocus = 1
	m.wizard.SourceName = "Feed1"
	m, _ = sendKey(m, tea.KeyBackspace, "backspace")
	if m.wizard.SourceName != "Feed" {
		t.Errorf("expected %q after backspace, got %q", "Feed", m.wizard.SourceName)
	}

	// Go to Step 3: Subserver
	m.wizard.Step = WizardStepSubserver
	m.wizard.subserverFocus = 0
	m.wizard.SubserverListen = "127.0.0.1:8765"
	m, _ = sendKey(m, tea.KeyBackspace, "backspace")
	if m.wizard.SubserverListen != "127.0.0.1:876" {
		t.Errorf("expected %q after backspace, got %q", "127.0.0.1:876", m.wizard.SubserverListen)
	}

	// Switch focus to path
	m.wizard.subserverFocus = 1
	m.wizard.SubserverPath = "/sub"
	m, _ = sendKey(m, tea.KeyBackspace, "backspace")
	if m.wizard.SubserverPath != "/su" {
		t.Errorf("expected %q after backspace, got %q", "/su", m.wizard.SubserverPath)
	}

	// Go to Step 4: Testing & Gemini
	m.wizard.Step = WizardStepTesting
	m.wizard.testingFocus = 0
	m.wizard.Concurrency = "20"
	m, _ = sendKey(m, tea.KeyBackspace, "backspace")
	if m.wizard.Concurrency != "2" {
		t.Errorf("expected %q after backspace, got %q", "2", m.wizard.Concurrency)
	}

	// Tab to timeout (focus 1)
	m, _ = sendKey(m, tea.KeyTab, "tab")
	if m.wizard.testingFocus != 1 {
		t.Errorf("expected testingFocus 1, got %d", m.wizard.testingFocus)
	}
	m.wizard.Timeout = "10s"
	m, _ = sendKey(m, tea.KeyBackspace, "backspace")
	if m.wizard.Timeout != "10" {
		t.Errorf("expected %q after backspace, got %q", "10", m.wizard.Timeout)
	}

	// Tab to target URL (focus 2)
	m, _ = sendKey(m, tea.KeyTab, "tab")
	if m.wizard.testingFocus != 2 {
		t.Errorf("expected testingFocus 2, got %d", m.wizard.testingFocus)
	}
	m.wizard.TargetURL = "https://gemini.google.com/"
	m, _ = sendKey(m, tea.KeyBackspace, "backspace")
	if m.wizard.TargetURL != "https://gemini.google.com" {
		t.Errorf("expected %q after backspace, got %q", "https://gemini.google.com", m.wizard.TargetURL)
	}

	// Tab wraps around to concurrency (focus 0)
	m, _ = sendKey(m, tea.KeyTab, "tab")
	if m.wizard.testingFocus != 0 {
		t.Errorf("expected testingFocus to wrap to 0, got %d", m.wizard.testingFocus)
	}
}

func TestWizard_FieldNavigation_ShiftTabAndUp(t *testing.T) {
	mockCtrl := &mockWizardController{}
	m := setupWizardModel(mockCtrl)

	// --- Step 2: Source (2 fields: URL=0, Name=1) ---
	m.wizard.Step = WizardStepSource
	m.wizard.sourceFocus = 0

	// Backward from 0 with Shift+Tab should wrap to 1
	m, _ = sendKey(m, tea.KeyShiftTab, "shift+tab")
	if m.wizard.sourceFocus != 1 {
		t.Errorf("Step 2: expected focus 1 after Shift+Tab from 0, got %d", m.wizard.sourceFocus)
	}
	// Backward from 1 with Up should go to 0
	m, _ = sendKey(m, tea.KeyUp, "up")
	if m.wizard.sourceFocus != 0 {
		t.Errorf("Step 2: expected focus 0 after Up from 1, got %d", m.wizard.sourceFocus)
	}
	// Forward from 0 with Tab should go to 1
	m, _ = sendKey(m, tea.KeyTab, "tab")
	if m.wizard.sourceFocus != 1 {
		t.Errorf("Step 2: expected focus 1 after Tab from 0, got %d", m.wizard.sourceFocus)
	}
	// Forward from 1 with Down should wrap to 0
	m, _ = sendKey(m, tea.KeyDown, "down")
	if m.wizard.sourceFocus != 0 {
		t.Errorf("Step 2: expected focus 0 after Down from 1, got %d", m.wizard.sourceFocus)
	}

	// --- Step 3: Subserver (2 fields: Listen=0, Path=1) ---
	m.wizard.Step = WizardStepSubserver
	m.wizard.subserverFocus = 0

	// Backward from 0 with Shift+Tab should wrap to 1
	m, _ = sendKey(m, tea.KeyShiftTab, "shift+tab")
	if m.wizard.subserverFocus != 1 {
		t.Errorf("Step 3: expected focus 1 after Shift+Tab from 0, got %d", m.wizard.subserverFocus)
	}
	// Backward from 1 with Up should go to 0
	m, _ = sendKey(m, tea.KeyUp, "up")
	if m.wizard.subserverFocus != 0 {
		t.Errorf("Step 3: expected focus 0 after Up from 1, got %d", m.wizard.subserverFocus)
	}
	// Forward from 0 with Tab should go to 1
	m, _ = sendKey(m, tea.KeyTab, "tab")
	if m.wizard.subserverFocus != 1 {
		t.Errorf("Step 3: expected focus 1 after Tab from 0, got %d", m.wizard.subserverFocus)
	}
	// Forward from 1 with Down should wrap to 0
	m, _ = sendKey(m, tea.KeyDown, "down")
	if m.wizard.subserverFocus != 0 {
		t.Errorf("Step 3: expected focus 0 after Down from 1, got %d", m.wizard.subserverFocus)
	}

	// --- Step 4: Testing & Gemini (3 fields: Concurrency=0, Timeout=1, TargetURL=2) ---
	m.wizard.Step = WizardStepTesting
	m.wizard.testingFocus = 0

	// Backward from 0 with Shift+Tab should wrap to 2
	m, _ = sendKey(m, tea.KeyShiftTab, "shift+tab")
	if m.wizard.testingFocus != 2 {
		t.Errorf("Step 4: expected focus 2 after Shift+Tab from 0, got %d", m.wizard.testingFocus)
	}
	// Backward from 2 with Up should go to 1
	m, _ = sendKey(m, tea.KeyUp, "up")
	if m.wizard.testingFocus != 1 {
		t.Errorf("Step 4: expected focus 1 after Up from 2, got %d", m.wizard.testingFocus)
	}
	// Backward from 1 with Shift+Tab should go to 0
	m, _ = sendKey(m, tea.KeyShiftTab, "shift+tab")
	if m.wizard.testingFocus != 0 {
		t.Errorf("Step 4: expected focus 0 after Shift+Tab from 1, got %d", m.wizard.testingFocus)
	}
	// Forward from 0 with Tab should go to 1
	m, _ = sendKey(m, tea.KeyTab, "tab")
	if m.wizard.testingFocus != 1 {
		t.Errorf("Step 4: expected focus 1 after Tab from 0, got %d", m.wizard.testingFocus)
	}
	// Forward from 1 with Down should go to 2
	m, _ = sendKey(m, tea.KeyDown, "down")
	if m.wizard.testingFocus != 2 {
		t.Errorf("Step 4: expected focus 2 after Down from 1, got %d", m.wizard.testingFocus)
	}
	// Forward from 2 with Tab should wrap to 0
	m, _ = sendKey(m, tea.KeyTab, "tab")
	if m.wizard.testingFocus != 0 {
		t.Errorf("Step 4: expected focus 0 after Tab from 2, got %d", m.wizard.testingFocus)
	}
}

func TestWizard_QuitKeyHandling(t *testing.T) {
	mockCtrl := &mockWizardController{}
	m := setupWizardModel(mockCtrl)

	// In Step 1: 'q' quits
	m.wizard.Step = WizardStepWelcome
	_, cmd := sendKey(m, tea.KeyRunes, "q")
	if cmd == nil {
		t.Error("expected tea.Quit cmd for 'q' in Step 1, got nil")
	}

	// In Step 5: 'q' quits when not saving
	m.wizard.Step = WizardStepReview
	m.wizard.saving = false
	_, cmd = sendKey(m, tea.KeyRunes, "q")
	if cmd == nil {
		t.Error("expected tea.Quit cmd for 'q' in Step 5 when not saving, got nil")
	}

	// In Step 5: 'q' does NOT quit when saving
	m.wizard.saving = true
	_, cmd = sendKey(m, tea.KeyRunes, "q")
	if cmd != nil {
		t.Errorf("expected nil cmd for 'q' while saving, got %v", cmd)
	}

	// Ctrl+C always quits
	_, cmd = sendKey(m, tea.KeyCtrlC, "ctrl+c")
	if cmd == nil {
		t.Error("expected tea.Quit cmd for ctrl+c, got nil")
	}
}

func TestWizard_RenderAllSteps(t *testing.T) {
	mockCtrl := &mockWizardController{}
	m := setupWizardModel(mockCtrl)

	steps := []WizardStep{
		WizardStepWelcome,
		WizardStepSource,
		WizardStepSubserver,
		WizardStepTesting,
		WizardStepReview,
	}

	for _, step := range steps {
		m.wizard.Step = step
		output := m.View()
		if len(output) == 0 {
			t.Errorf("expected non-empty View() for step %s", step.Name())
		}
		if !strings.Contains(output, step.Name()) && !strings.Contains(output, "Welcome to Gemsub") {
			t.Errorf("expected View() to contain step name or title for step %s", step.Name())
		}
	}
}

func TestWizard_RuntimeStartupFailureAndIndependentRetry(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	stateFile := filepath.Join(tmpDir, "state.json")

	st := store.New(stateFile, 2)
	bus := events.New()
	ring := logging.NewRingLogHandler(100)
	ad := adapter.New(st, bus, ring)
	defer ad.Close()

	cfgSvc := config.NewDefaultService(cfgPath, bus)
	srcSvc := source.NewService(cfgSvc)
	ad.SetServices(cfgSvc, srcSvc, nil, nil)

	var runtimeFail bool = true
	var runtimeAttempts int
	ad.SetRuntimeStarter(func() error {
		runtimeAttempts++
		if runtimeFail {
			return errors.New("simulated runtime worker startup failure")
		}
		return nil
	})

	m := setupWizardModel(ad)
	m.wizard.Step = WizardStepReview
	m.wizard.SourceURL = "https://example.com/subs.txt"
	m.wizard.SourceName = "My Feed"
	m.wizard.SubserverListen = "127.0.0.1:8765"
	m.wizard.SubserverPath = "/sub"
	m.wizard.Concurrency = "20"
	m.wizard.Timeout = "10s"
	m.wizard.TargetURL = "https://gemini.google.com/"

	// Initial assertion: first-run is true, file does not exist
	if !cfgSvc.IsFirstRun() {
		t.Fatal("expected IsFirstRun() == true initially")
	}

	// Press Enter to save and start:
	// Phase 1 (config commit) succeeds, Phase 2 (runtime startup) fails
	m, cmd := sendKey(m, tea.KeyEnter, "enter")
	if cmd == nil {
		t.Fatal("expected cmd from enter on review step")
	}
	msg := cmd()
	updated, _ := m.Update(msg)
	m = updated.(*Model)

	// Invariant 1: configuration commit succeeded
	if len(cfgSvc.Get().Sources) != 1 {
		t.Fatalf("expected 1 source committed in ConfigService, got %d", len(cfgSvc.Get().Sources))
	}
	// Invariant 2: runtime startup failed
	if m.wizard.runtimeError == "" {
		t.Fatal("expected runtimeError to be populated in wizard")
	}
	if !strings.Contains(m.wizard.runtimeError, "simulated runtime worker startup failure") {
		t.Errorf("unexpected runtimeError: %s", m.wizard.runtimeError)
	}
	// Invariant 3: config remains persisted on disk
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("expected config.json to remain persisted on disk: %v", err)
	}
	// Invariant 4: isFirstRun remains false
	if cfgSvc.IsFirstRun() {
		t.Fatal("expected IsFirstRun() == false after config commit")
	}
	// Invariant 5: wizard cannot accidentally re-register the source (configCommitted is true)
	if !m.wizard.configCommitted {
		t.Fatal("expected wizard.configCommitted == true")
	}

	// Try pressing Enter again while runtime still fails:
	// It must NOT call CompleteOnboarding (which would error with ErrDuplicateSource);
	// it must call StartRuntime independently!
	m, cmd = sendKey(m, tea.KeyEnter, "enter")
	if cmd == nil {
		t.Fatal("expected cmd on retry")
	}
	msg = cmd()
	updated, _ = m.Update(msg)
	m = updated.(*Model)

	// Verify it still failed at runtime level without corrupting config or throwing ErrDuplicateSource
	if m.wizard.saveError != "" {
		t.Fatalf("unexpected saveError (did it re-register source?): %s", m.wizard.saveError)
	}
	if len(cfgSvc.Get().Sources) != 1 {
		t.Errorf("expected still exactly 1 source, got %d", len(cfgSvc.Get().Sources))
	}
	if runtimeAttempts != 2 {
		t.Errorf("expected 2 runtime attempts, got %d", runtimeAttempts)
	}

	// Invariant 6: runtime can be retried independently and succeed
	runtimeFail = false
	m, cmd = sendKey(m, tea.KeyEnter, "enter")
	if cmd == nil {
		t.Fatal("expected cmd on second retry")
	}
	msg = cmd()
	updated, _ = m.Update(msg)
	m = updated.(*Model)

	// Invariant 7: successful retry transitions to ViewCandidates
	if m.activeView != ViewCandidates {
		t.Fatalf("expected activeView == ViewCandidates after successful retry, got %v", m.activeView)
	}
	if m.wizard.runtimeError != "" {
		t.Errorf("expected runtimeError cleared after successful start, got %q", m.wizard.runtimeError)
	}
}

func TestWizard_SaveOnboarding_InvalidConcurrencyReturnsError(t *testing.T) {
	mockCtrl := &mockWizardController{}
	m := setupWizardModel(mockCtrl)
	m.wizard.Step = WizardStepReview
	m.wizard.SourceURL = "https://example.com/subs.txt"
	m.wizard.SubserverListen = "127.0.0.1:8765"
	m.wizard.SubserverPath = "/sub"
	m.wizard.Concurrency = "not-a-number"
	m.wizard.Timeout = "10s"
	m.wizard.TargetURL = "https://gemini.google.com/"

	cmd := m.saveOnboardingCmd()
	msg := cmd().(onboardingSavedMsg)
	if msg.err == nil {
		t.Fatal("expected error for invalid concurrency in saveOnboardingCmd, got nil")
	}
	if mockCtrl.completeOnboardingCalls != 0 {
		t.Errorf("expected CompleteOnboarding not to be called, got %d calls", mockCtrl.completeOnboardingCalls)
	}
}
