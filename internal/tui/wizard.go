package tui

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gemsub/internal/config"
	"gemsub/internal/tui/viewmodel"
)

// WizardStep represents the active screen in the first-run onboarding wizard.
type WizardStep int

const (
	WizardStepWelcome WizardStep = iota
	WizardStepSource
	WizardStepSubserver
	WizardStepTesting
	WizardStepReview
)

func (s WizardStep) Name() string {
	switch s {
	case WizardStepWelcome:
		return "Welcome & Architecture"
	case WizardStepSource:
		return "Primary Subscription Source"
	case WizardStepSubserver:
		return "Local Subserver Setup"
	case WizardStepTesting:
		return "Testing & Gemini Defaults"
	case WizardStepReview:
		return "Review & Save"
	default:
		return "Unknown"
	}
}

// WizardState encapsulates the in-memory state of the first-run onboarding wizard.
type WizardState struct {
	Step WizardStep

	// Step 2: Source
	SourceURL   string
	SourceName  string
	sourceFocus int // 0 = URL, 1 = Name
	sourceErr   string

	// Step 3: Subserver
	SubserverListen string
	SubserverPath   string
	subserverFocus  int // 0 = Listen, 1 = Path
	subserverErr    string

	// Step 4: Testing & Gemini
	Concurrency  string
	Timeout      string
	TargetURL    string
	testingFocus int // 0 = Concurrency, 1 = Timeout, 2 = TargetURL
	testingErr   string

	// Step 5: Review & Save
	saving          bool
	saveError       string
	configCommitted bool
	runtimeError    string
}

func newWizardState() WizardState {
	return WizardState{
		Step:            WizardStepWelcome,
		SourceURL:       "",
		SourceName:      "",
		SubserverListen: "127.0.0.1:8765",
		SubserverPath:   "/sub",
		Concurrency:     "20",
		Timeout:         "10s",
		TargetURL:       "https://gemini.google.com/",
	}
}

// Validation helpers

func validateSource(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("source URL must not be empty")
	}
	norm, err := config.NormalizeURL(trimmed)
	if err != nil {
		return err
	}
	if norm == "" {
		return fmt.Errorf("invalid source URL")
	}
	return nil
}

func validateSubserver(listen, path string) error {
	lTrim := strings.TrimSpace(listen)
	if lTrim == "" {
		return fmt.Errorf("listen address must not be empty")
	}
	_, portStr, err := net.SplitHostPort(lTrim)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: must be host:port (e.g. 127.0.0.1:8765)", lTrim)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid listen port: must be between 1 and 65535")
	}

	pTrim := strings.TrimSpace(path)
	if pTrim == "" {
		return fmt.Errorf("subscription path must not be empty")
	}
	if !strings.HasPrefix(pTrim, "/") {
		return fmt.Errorf("subscription path must begin with '/' (e.g. /sub)")
	}
	return nil
}

func validateTesting(concurrency, timeout, targetURL string) (int, error) {
	cTrim := strings.TrimSpace(concurrency)
	c, err := strconv.Atoi(cTrim)
	if err != nil || c <= 0 {
		return 0, fmt.Errorf("concurrency must be a positive integer (got %q)", cTrim)
	}

	tTrim := strings.TrimSpace(timeout)
	d, err := time.ParseDuration(tTrim)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("timeout must be a positive duration string (e.g. 10s, got %q)", tTrim)
	}

	targetTrim := strings.TrimSpace(targetURL)
	if targetTrim == "" {
		return 0, fmt.Errorf("target URL must not be empty")
	}
	u, err := url.ParseRequestURI(targetTrim)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return 0, fmt.Errorf("invalid target URL %q", targetTrim)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return 0, fmt.Errorf("target URL scheme must be http or https, got %q", u.Scheme)
	}

	return c, nil
}

type onboardingSavedMsg struct {
	configSaved bool
	err         error
	runtimeErr  error
}

func (m *Model) saveOnboardingCmd() tea.Cmd {
	return func() tea.Msg {
		if m.ctrl == nil {
			return onboardingSavedMsg{err: fmt.Errorf("controller unavailable")}
		}

		// If configuration is already committed, only attempt runtime startup
		if m.wizard.configCommitted {
			err := m.ctrl.StartRuntime()
			return onboardingSavedMsg{configSaved: true, runtimeErr: err}
		}

		if err := validateSource(m.wizard.SourceURL); err != nil {
			return onboardingSavedMsg{err: err}
		}
		if err := validateSubserver(m.wizard.SubserverListen, m.wizard.SubserverPath); err != nil {
			return onboardingSavedMsg{err: err}
		}
		concurrency, err := validateTesting(m.wizard.Concurrency, m.wizard.Timeout, m.wizard.TargetURL)
		if err != nil {
			return onboardingSavedMsg{err: err}
		}

		// Phase 1: Atomic configuration persistence
		err = m.ctrl.CompleteOnboarding(viewmodel.OnboardingConfig{
			SourceURL:   strings.TrimSpace(m.wizard.SourceURL),
			SourceName:  strings.TrimSpace(m.wizard.SourceName),
			Listen:      strings.TrimSpace(m.wizard.SubserverListen),
			Path:        strings.TrimSpace(m.wizard.SubserverPath),
			Concurrency: concurrency,
			Timeout:     strings.TrimSpace(m.wizard.Timeout),
			TargetURL:   strings.TrimSpace(m.wizard.TargetURL),
		})
		if err != nil {
			return onboardingSavedMsg{err: err}
		}

		// Phase 2: Runtime worker startup
		runtimeErr := m.ctrl.StartRuntime()
		return onboardingSavedMsg{configSaved: true, runtimeErr: runtimeErr}
	}
}

func (m *Model) retryRuntimeCmd() tea.Cmd {
	return func() tea.Msg {
		if m.ctrl == nil {
			return onboardingSavedMsg{configSaved: true, runtimeErr: fmt.Errorf("controller unavailable")}
		}
		err := m.ctrl.StartRuntime()
		return onboardingSavedMsg{configSaved: true, runtimeErr: err}
	}
}

func (m *Model) handleWizardKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	}

	switch m.wizard.Step {
	case WizardStepWelcome:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "enter", "right", " ":
			m.wizard.Step = WizardStepSource
			return m, nil
		}

	case WizardStepSource:
		switch msg.String() {
		case "esc":
			m.wizard.sourceErr = ""
			m.wizard.Step = WizardStepWelcome
			return m, nil
		case "tab", "down":
			m.wizard.sourceFocus = (m.wizard.sourceFocus + 1) % 2
			return m, nil
		case "shift+tab", "up":
			m.wizard.sourceFocus = (m.wizard.sourceFocus + 2 - 1) % 2
			return m, nil
		case "enter":
			if err := validateSource(m.wizard.SourceURL); err != nil {
				m.wizard.sourceErr = err.Error()
				return m, nil
			}
			m.wizard.sourceErr = ""
			m.wizard.Step = WizardStepSubserver
			return m, nil
		case "backspace":
			m.wizard.sourceErr = ""
			if m.wizard.sourceFocus == 0 {
				if len(m.wizard.SourceURL) > 0 {
					m.wizard.SourceURL = m.wizard.SourceURL[:len(m.wizard.SourceURL)-1]
				}
			} else {
				if len(m.wizard.SourceName) > 0 {
					m.wizard.SourceName = m.wizard.SourceName[:len(m.wizard.SourceName)-1]
				}
			}
			return m, nil
		default:
			if len(msg.Runes) > 0 {
				m.wizard.sourceErr = ""
				if m.wizard.sourceFocus == 0 {
					m.wizard.SourceURL += string(msg.Runes)
				} else {
					m.wizard.SourceName += string(msg.Runes)
				}
				return m, nil
			}
		}

	case WizardStepSubserver:
		switch msg.String() {
		case "esc":
			m.wizard.subserverErr = ""
			m.wizard.Step = WizardStepSource
			return m, nil
		case "tab", "down":
			m.wizard.subserverFocus = (m.wizard.subserverFocus + 1) % 2
			return m, nil
		case "shift+tab", "up":
			m.wizard.subserverFocus = (m.wizard.subserverFocus + 2 - 1) % 2
			return m, nil
		case "enter":
			if err := validateSubserver(m.wizard.SubserverListen, m.wizard.SubserverPath); err != nil {
				m.wizard.subserverErr = err.Error()
				return m, nil
			}
			m.wizard.subserverErr = ""
			m.wizard.Step = WizardStepTesting
			return m, nil
		case "backspace":
			m.wizard.subserverErr = ""
			if m.wizard.subserverFocus == 0 {
				if len(m.wizard.SubserverListen) > 0 {
					m.wizard.SubserverListen = m.wizard.SubserverListen[:len(m.wizard.SubserverListen)-1]
				}
			} else {
				if len(m.wizard.SubserverPath) > 0 {
					m.wizard.SubserverPath = m.wizard.SubserverPath[:len(m.wizard.SubserverPath)-1]
				}
			}
			return m, nil
		default:
			if len(msg.Runes) > 0 {
				m.wizard.subserverErr = ""
				if m.wizard.subserverFocus == 0 {
					m.wizard.SubserverListen += string(msg.Runes)
				} else {
					m.wizard.SubserverPath += string(msg.Runes)
				}
				return m, nil
			}
		}

	case WizardStepTesting:
		switch msg.String() {
		case "esc":
			m.wizard.testingErr = ""
			m.wizard.Step = WizardStepSubserver
			return m, nil
		case "tab", "down":
			m.wizard.testingFocus = (m.wizard.testingFocus + 1) % 3
			return m, nil
		case "shift+tab", "up":
			m.wizard.testingFocus = (m.wizard.testingFocus + 2) % 3
			return m, nil
		case "enter":
			if _, err := validateTesting(m.wizard.Concurrency, m.wizard.Timeout, m.wizard.TargetURL); err != nil {
				m.wizard.testingErr = err.Error()
				return m, nil
			}
			m.wizard.testingErr = ""
			m.wizard.Step = WizardStepReview
			return m, nil
		case "backspace":
			m.wizard.testingErr = ""
			switch m.wizard.testingFocus {
			case 0:
				if len(m.wizard.Concurrency) > 0 {
					m.wizard.Concurrency = m.wizard.Concurrency[:len(m.wizard.Concurrency)-1]
				}
			case 1:
				if len(m.wizard.Timeout) > 0 {
					m.wizard.Timeout = m.wizard.Timeout[:len(m.wizard.Timeout)-1]
				}
			case 2:
				if len(m.wizard.TargetURL) > 0 {
					m.wizard.TargetURL = m.wizard.TargetURL[:len(m.wizard.TargetURL)-1]
				}
			}
			return m, nil
		default:
			if len(msg.Runes) > 0 {
				m.wizard.testingErr = ""
				switch m.wizard.testingFocus {
				case 0:
					m.wizard.Concurrency += string(msg.Runes)
				case 1:
					m.wizard.Timeout += string(msg.Runes)
				case 2:
					m.wizard.TargetURL += string(msg.Runes)
				}
				return m, nil
			}
		}

	case WizardStepReview:
		switch msg.String() {
		case "esc":
			if !m.wizard.saving {
				if m.wizard.configCommitted {
					m.activeView = ViewCandidates
					if m.ctrl != nil {
						m.applySnapshot(m.ctrl.Snapshot(m.filterMode), true)
					}
					return m, nil
				}
				m.wizard.saveError = ""
				m.wizard.Step = WizardStepTesting
			}
			return m, nil
		case "q":
			if !m.wizard.saving {
				return m, tea.Quit
			}
		case "c":
			if m.wizard.configCommitted && !m.wizard.saving {
				m.activeView = ViewConfig
				m.refreshConfigCenter()
				return m, nil
			}
		case "enter":
			if !m.wizard.saving {
				m.wizard.saving = true
				if m.wizard.configCommitted {
					m.wizard.runtimeError = ""
					return m, m.retryRuntimeCmd()
				}
				m.wizard.saveError = ""
				return m, m.saveOnboardingCmd()
			}
		}
	}

	return m, nil
}

func (m *Model) renderWizard() string {
	var sb strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	activeLabelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	inactiveLabelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	activeBoxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("14")).Padding(0, 1)
	inactiveBoxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	cardStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6")).Padding(1, 2)
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)

	// Step indicator
	stepNames := []string{"1. Welcome", "2. Source", "3. Subserver", "4. Testing", "5. Review"}
	var indicatorParts []string
	for i, name := range stepNames {
		if WizardStep(i) == m.wizard.Step {
			indicatorParts = append(indicatorParts, titleStyle.Render("["+name+"]"))
		} else if WizardStep(i) < m.wizard.Step {
			indicatorParts = append(indicatorParts, successStyle.Render("✓ "+name))
		} else {
			indicatorParts = append(indicatorParts, dimStyle.Render("  "+name))
		}
	}
	sb.WriteString("  " + strings.Join(indicatorParts, dimStyle.Render("  ──  ")) + "\n\n")

	switch m.wizard.Step {
	case WizardStepWelcome:
		var content strings.Builder
		content.WriteString(titleStyle.Render("Welcome to Gemsub") + "\n\n")
		content.WriteString("Gemsub is an automated proxy subscription testing and delivery daemon.\n\n")
		content.WriteString("How it works:\n")
		content.WriteString("  1. Sources   : Ingests proxy links (VLESS, VMess, Trojan) from subscription feeds.\n")
		content.WriteString("  2. Tester    : Evaluates transport connectivity and target reachability (Gemini).\n")
		content.WriteString("  3. Store     : Authoritatively tracks health, latency, scores, and projections.\n")
		content.WriteString("  4. Subserver : Serves passing links locally over HTTP for client auto-update.\n")
		content.WriteString("  5. Publisher : (Optional) Publishes passing subscriptions to Git repositories.\n\n")
		content.WriteString(dimStyle.Render("This wizard will help you configure your primary source and initial settings.") + "\n")
		content.WriteString(dimStyle.Render("All settings can be modified anytime later in the Configuration Center.") + "\n\n")
		content.WriteString(activeLabelStyle.Render("Press [Enter] to begin configuration, or [q] to exit."))

		sb.WriteString(cardStyle.Render(content.String()))

	case WizardStepSource:
		var content strings.Builder
		content.WriteString(titleStyle.Render("Step 2: Primary Subscription Source") + "\n\n")
		content.WriteString("Provide at least one subscription feed URL containing proxy configurations.\n")
		content.WriteString(dimStyle.Render("Supported schemes: http://, https://") + "\n\n")

		// Source URL input
		urlLabel := inactiveLabelStyle.Render("Subscription Feed URL:")
		urlBox := inactiveBoxStyle
		if m.wizard.sourceFocus == 0 {
			urlLabel = activeLabelStyle.Render("Subscription Feed URL: *")
			urlBox = activeBoxStyle
		}
		valURL := m.wizard.SourceURL
		if valURL == "" && m.wizard.sourceFocus == 0 {
			valURL = dimStyle.Render("(enter URL, e.g. https://example.com/sub.txt)")
		}
		content.WriteString(urlLabel + "\n")
		content.WriteString(urlBox.Render(valURL) + "\n\n")

		// Source Name input
		nameLabel := inactiveLabelStyle.Render("Display Alias (optional, press Tab to focus):")
		nameBox := inactiveBoxStyle
		if m.wizard.sourceFocus == 1 {
			nameLabel = activeLabelStyle.Render("Display Alias (optional):")
			nameBox = activeBoxStyle
		}
		valName := m.wizard.SourceName
		if valName == "" && m.wizard.sourceFocus == 1 {
			valName = dimStyle.Render("(default: derived from hostname)")
		}
		content.WriteString(nameLabel + "\n")
		content.WriteString(nameBox.Render(valName) + "\n\n")

		if m.wizard.sourceErr != "" {
			content.WriteString(errStyle.Render("Error: "+m.wizard.sourceErr) + "\n\n")
		}

		content.WriteString(dimStyle.Render("[Tab] Switch Field  [Enter] Next Step  [Esc] Back"))
		sb.WriteString(cardStyle.Render(content.String()))

	case WizardStepSubserver:
		var content strings.Builder
		content.WriteString(titleStyle.Render("Step 3: Local Subserver Setup") + "\n\n")
		content.WriteString("Gemsub runs a local HTTP server so your client app (Throne, Clash, etc.)\n")
		content.WriteString("can auto-update from your tested subscriptions.\n\n")

		// Listen Address
		listenLabel := inactiveLabelStyle.Render("Listen Address (host:port):")
		listenBox := inactiveBoxStyle
		if m.wizard.subserverFocus == 0 {
			listenLabel = activeLabelStyle.Render("Listen Address (host:port): *")
			listenBox = activeBoxStyle
		}
		content.WriteString(listenLabel + "\n")
		content.WriteString(listenBox.Render(m.wizard.SubserverListen) + "\n\n")

		// Path
		pathLabel := inactiveLabelStyle.Render("Subscription URL Path:")
		pathBox := inactiveBoxStyle
		if m.wizard.subserverFocus == 1 {
			pathLabel = activeLabelStyle.Render("Subscription URL Path: *")
			pathBox = activeBoxStyle
		}
		content.WriteString(pathLabel + "\n")
		content.WriteString(pathBox.Render(m.wizard.SubserverPath) + "\n\n")

		if m.wizard.subserverErr != "" {
			content.WriteString(errStyle.Render("Error: "+m.wizard.subserverErr) + "\n\n")
		}

		content.WriteString(dimStyle.Render("[Tab] Switch Field  [Enter] Next Step  [Esc] Back"))
		sb.WriteString(cardStyle.Render(content.String()))

	case WizardStepTesting:
		var content strings.Builder
		content.WriteString(titleStyle.Render("Step 4: Testing & Gemini Defaults") + "\n\n")
		content.WriteString("Configure proxy candidate testing concurrency, probe timeout, and validation target.\n\n")

		// Concurrency
		concLabel := inactiveLabelStyle.Render("Probe Concurrency (parallel testers):")
		concBox := inactiveBoxStyle
		if m.wizard.testingFocus == 0 {
			concLabel = activeLabelStyle.Render("Probe Concurrency: *")
			concBox = activeBoxStyle
		}
		content.WriteString(concLabel + "\n")
		content.WriteString(concBox.Render(m.wizard.Concurrency) + "\n\n")

		// Timeout
		toLabel := inactiveLabelStyle.Render("Probe Timeout (e.g. 10s):")
		toBox := inactiveBoxStyle
		if m.wizard.testingFocus == 1 {
			toLabel = activeLabelStyle.Render("Probe Timeout: *")
			toBox = activeBoxStyle
		}
		content.WriteString(toLabel + "\n")
		content.WriteString(toBox.Render(m.wizard.Timeout) + "\n\n")

		// Target URL
		targetLabel := inactiveLabelStyle.Render("Validation Target URL (Gemini endpoint):")
		targetBox := inactiveBoxStyle
		if m.wizard.testingFocus == 2 {
			targetLabel = activeLabelStyle.Render("Validation Target URL: *")
			targetBox = activeBoxStyle
		}
		content.WriteString(targetLabel + "\n")
		content.WriteString(targetBox.Render(m.wizard.TargetURL) + "\n\n")

		if m.wizard.testingErr != "" {
			content.WriteString(errStyle.Render("Error: "+m.wizard.testingErr) + "\n\n")
		}

		content.WriteString(dimStyle.Render("[Tab] Switch Field  [Enter] Next Step  [Esc] Back"))
		sb.WriteString(cardStyle.Render(content.String()))

	case WizardStepReview:
		var content strings.Builder
		content.WriteString(titleStyle.Render("Step 5: Review & Save") + "\n\n")
		content.WriteString("Review your initial configuration below before saving:\n\n")

		alias := m.wizard.SourceName
		if alias == "" {
			alias = "(auto-derived from URL)"
		}

		content.WriteString(fmt.Sprintf("  • %-22s : %s\n", "Primary Source URL", m.wizard.SourceURL))
		content.WriteString(fmt.Sprintf("  • %-22s : %s\n", "Source Alias", alias))
		content.WriteString(fmt.Sprintf("  • %-22s : %s\n", "Subserver Listen", m.wizard.SubserverListen))
		content.WriteString(fmt.Sprintf("  • %-22s : %s\n", "Subserver Path", m.wizard.SubserverPath))
		content.WriteString(fmt.Sprintf("  • %-22s : %s\n", "Testing Concurrency", m.wizard.Concurrency))
		content.WriteString(fmt.Sprintf("  • %-22s : %s\n", "Testing Timeout", m.wizard.Timeout))
		content.WriteString(fmt.Sprintf("  • %-22s : %s\n", "Target URL", m.wizard.TargetURL))
		content.WriteString(fmt.Sprintf("  • %-22s : %s\n", "Config File", "./config.json"))
		content.WriteString(fmt.Sprintf("  • %-22s : %s\n\n", "State File", "./gemsub_state.json"))

		if m.wizard.configCommitted {
			content.WriteString(successStyle.Render("✓ Initial configuration saved successfully to disk.") + "\n\n")
		}
		if m.wizard.saveError != "" {
			content.WriteString(errStyle.Render("Save Error: "+m.wizard.saveError) + "\n\n")
		}
		if m.wizard.runtimeError != "" {
			content.WriteString(errStyle.Render("Runtime Startup Error: "+m.wizard.runtimeError) + "\n\n")
		}

		if m.wizard.saving {
			if m.wizard.configCommitted {
				content.WriteString(activeLabelStyle.Render("Starting daemon runtime... Please wait."))
			} else {
				content.WriteString(activeLabelStyle.Render("Saving configuration and starting daemon runtime... Please wait."))
			}
		} else if m.wizard.configCommitted && m.wizard.runtimeError != "" {
			content.WriteString(activeLabelStyle.Render("Press [Enter] to retry starting runtime daemon, or [Esc] to proceed to candidate view."))
		} else {
			content.WriteString(activeLabelStyle.Render("Press [Enter] to confirm and start Gemsub, or [Esc] to go back and edit."))
		}

		sb.WriteString(cardStyle.Render(content.String()))
	}

	return sb.String()
}
