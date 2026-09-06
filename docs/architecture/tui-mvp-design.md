# Terminal UI (TUI) MVP — Final Design Specification

This document establishes the frozen architecture, presentation boundary, state ownership, event-refresh dynamics, and implementation invariants for the Terminal UI (TUI) MVP in `gemsub`.

---

### 1. Presentation Boundary & Decoupled ViewModel
- **Separation of Concerns**: The TUI layer is strictly a presentation consumer. The Bubble Tea model (`tea.Model`) does not import or reference `store.CandidateRecord`, `store.BoundedHistory`, `store.ScoringConfig`, or internal `events.subscriber` queues.
- **Immutable Presentation ViewModels**:
  - ViewModels are immutable, presentation-oriented projections and may contain preformatted display fields such as `ScoreFormatted`, `LatencyFormatted`, and `HistoryGlyphs`.
  - Bubble Tea view and update functions operate exclusively on these decoupled ViewModels.
- **Opaque Presentation Identity**:
  - `CandidateRowViewModel.ID` and `CandidateDetailViewModel.ID` are opaque presentation identifiers (`string`).
  - The adapter owns the mapping between opaque presentation IDs and internal candidate identities for the lifetime of the current UI snapshot. Bubble Tea never interprets the ID semantically.
  - Bubble Tea and the view rendering layer have zero knowledge of internal identity representations (such as `CanonicalLink` or future `ConfigFingerprint` / deduplication keys).
- **Candidate Ordering**:
  - The adapter owns the presentation query and applies the repository's canonical candidate ranking policy. Bubble Tea does not reimplement or duplicate ranking logic.
  - The adapter delivers a pre-sorted slice `[]CandidateRowViewModel` following this deterministic order:
    1. Servable candidates before unservable candidates.
    2. Within each group, proven candidates (`HasPassed == true`) before unproven candidates (`HasPassed == false`).
    3. Reliability Score descending.
    4. Last passed latency ascending.
    5. Stable internal identity ascending as deterministic tie-breaker.
  - Bubble Tea renders rows strictly in the order delivered.

---

### 2. State Ownership & Startup Ordering
- **Explicit Dependency Ownership**:
  - `cmd/gemsub/main.go` owns the core application infrastructure: `Store`, `EventBus`, and `RingLogHandler`.
  - The TUI adapter is a read/projection consumer of application state; it may maintain ephemeral presentation coordination state such as dirty flags and subscriber lifecycle state, but it must never maintain an independent candidate-state cache or shadow store.
  - The TUI layer must never create or replace the application's logging infrastructure or EventBus.
- **Startup Sequence**:
  Initialization follows this strict sequence:
  1. **Logging Initialization**: In non-headless mode, `logging.Setup(false, 1000, os.Stderr)` instantiates and registers the `*RingLogHandler`.
  2. **Store Bootstrap**: `st := store.New(...)` is instantiated and `st.Load()` restores persisted state before live scheduler execution.
  3. **EventBus Instantiation**: `bus := events.New()` creates the application EventBus.
  4. **TUI Adapter Subscription & Initial Hydration**:
     - The TUI adapter is instantiated with `st`, `bus`, and `ringHandler`.
     - The adapter subscribes to `bus` (`sub := bus.Subscribe()`) before scheduler execution begins.
     - The adapter synchronously hydrates initial ViewModel state: `HeaderViewModel` from `st.Stats()`, pre-sorted `[]CandidateRowViewModel`, and log buffer from `ringHandler.RecordsByLevel(slog.LevelInfo)`.
  5. **Scheduler & Server Launch**: `scheduler.New(cfg, st, bus)` is launched (`go sched.Run(ctx)`), followed by the HTTP subserver.
  6. **Bubble Tea Execution**: `tea.NewProgram` runs with the pre-hydrated model.
- **Startup Concurrency Semantics**:
  - Events emitted after TUI subscription but before Bubble Tea enters its event loop are not required to be replayed. Correctness is established by initial hydration from the authoritative Store followed by subsequent Store re-query.

---

### 3. EventBus Semantics & Throttled Refresh
- **Invalidation Hints (Lossy Delivery Semantics)**:
  - `EventBus` events delivered to the TUI are asynchronous invalidation and notification hints.
  - Event delivery to the TUI may be lossy when the non-blocking subscriber queue is full during high-concurrency probe bursts.
  - Event loss is acceptable because Store is the authoritative source of candidate state.
  - On a refresh tick, the TUI queries the Store/read model again rather than reconstructing state from events.
  - The TUI subscriber does not use or require guaranteed-delivery semantics.
- **Bounded Render Frequency**:
  - Rendering is constrained to a bounded render frequency: a maximum rate of **10 Hz** (a minimum interval of **100 ms** between renders).
  - An event listener goroutine marks internal state as `dirty` on probe completion events.
  - A 10 Hz ticker coordinates redraws:
    - When `dirty == false`, no Store snapshot or ViewModel rebuild is performed. The periodic tick performs only the minimum scheduling work required for refresh coordination.
    - When `dirty == true`, the adapter queries Store for updated state, dispatches a coalesced update message (`tea.Msg`) to Bubble Tea, and resets `dirty`.
    - On cycle completion, an immediate refresh is dispatched so final status is visible without waiting for the next tick interval.

---

### 4. Candidate List MVP
- **Columns (Compact Table Layout)**:
  1. `SERV` (3 chars): `[✓]` (green) if servable, `[ ]` (dim) if not.
  2. `PROTO` (5 chars): `vless`, `vmess`, `troj `, `ss   `.
  3. `STATUS` (5 chars): `PASS ` (green), `FAIL ` (red), `INCON` (yellow), `PEND ` (dim).
  4. `SCORE` (5 chars): `0.85` or `---`.
  5. `LAT` (6 chars): `120ms` or `---`.
  6. `HISTORY` (10 chars): Compact bounded history glyphs (e.g. `[●●○×●●●●●●]`).
  7. `ENDPOINT / REMARK` (flex width): Host:port or remark name, truncated to available terminal width.
- **Navigation & Filtering Keys**:
  - `Up` / `k`: Move selection up.
  - `Down` / `j`: Move selection down.
  - `PageUp` / `Ctrl+B`: Page up.
  - `PageDown` / `Ctrl+F`: Page down.
  - `Home` / `g`: Jump to top of list.
  - `End` / `G`: Jump to last candidate.
  - `Enter` / `Space`: Toggle candidate detail view.
  - `s`: Toggle filter between `All Candidates` and `Servable Only`.

---

### 5. Candidate Detail MVP & Credential Masking
- **Information Rendered**:
  - Identification: Protocol, Opaque ID, Remark.
  - Endpoint: Host/IP, Port, Path, SNI.
  - Serving State: Servable (`YES` / `NO`), with gate failure reason if unservable.
  - Reliability Metrics: Score, Proven status (`HasPassed`), Absent cycles count.
  - Latest Observation: Status, Error Category, Reason string, HTTP Status Code, Latency, Probe Attempts, Timestamp.
  - Observable Warnings: Normalization/parser warnings if present.
  - Sample History: Chronological list of bounded window samples (`Age`, `Status`, `Category`, `Code`, `Latency`, `Attempts`).
- **Compact History Glyphs**:
  - `●` (Green): `StatusPassed`
  - `×` (Red): `StatusFailed`
  - `○` (Yellow): `StatusInconclusive`
  - `·` (Dim Gray): Empty slot in circular buffer
  - ASCII fallback: `[P P ? F P P P P P P]`.
- **Mandatory Credential Masking**:
  - Passwords, private UUIDs, and secret query parameters MUST NEVER be displayed in cleartext on screen by default.
  - Active links are rendered with masked credentials (e.g. `vless://[REDACTED]@host:port?...` or truncated UUID `4a3b...c91d`).
- **Clipboard Capability & Failure Behavior**:
  - Clipboard integration (e.g. copying an active link via `y`) is an optional capability.
  - If clipboard capability is unavailable, the clipboard action must fail non-fatally with a user-visible status. Clipboard availability must never affect core TUI functionality.

---

### 6. Logs MVP
- **Log Source**: Sourced from `*logging.RingLogHandler` via `ringHandler.RecordsByLevel(minLevel)` or `ringHandler.Formatted()`.
- **Navigation & Controls**:
  - `Tab`: Switch focus between Candidate View and Log View.
  - In Log View:
    - `j` / `Down`: Scroll down 1 line.
    - `k` / `Up`: Scroll up 1 line.
    - `PageDown` / `Ctrl+F`: Scroll down 1 page.
    - `PageUp` / `Ctrl+B`: Scroll up 1 page.
    - `G`: Scroll to bottom (re-engages auto-follow mode).
    - `g`: Scroll to top (pauses auto-follow).
- **Log Level Filtering**:
  - `l` (or keys `1`-`4`): Cycle minimum log level: `DEBUG` -> `INFO` -> `WARN` -> `ERROR`. Default is `INFO`.

---

### 7. Headless Mode
- **Runtime Isolation**:
  - Headless mode must instantiate zero TUI runtime components:
    - No Bubble Tea program.
    - No TUI adapter.
    - No TUI event subscriber.
    - No TUI timers/tickers.
    - No TUI-specific goroutines.
    - No `RingLogHandler` in headless mode.
  - In headless mode, `logging.Setup(true, 1000, os.Stderr)` configures standard `slog.TextHandler` to `os.Stderr`, and the runtime blocks on `<-ctx.Done()`.

---

### 8. Termination & Shutdown
- **Single Lifecycle Coordination**:
  - Shutdown is coordinated through a single application lifecycle path. The application must ensure scheduler cancellation, server shutdown, Store persistence, and terminal restoration are completed before process exit.
  - User pressing `q` or `Ctrl+C` instructs Bubble Tea to quit, restoring terminal cooked mode and releasing alternate screen buffers, followed by cancellation of the top-level application context.
  - External OS signals (`SIGINT`, `SIGTERM`) cancel the application context, initiating the same graceful teardown sequence.

---

### 9. Layout & Responsive Behavior
- **Window Resizing**: TUI handles `tea.WindowSizeMsg` dynamically.
- **Dimensions & Degradation**:
  - Minimum supported terminal dimensions: **80 columns × 24 lines**.
  - Fallback is shown when `width < 80 || height < 24`.
  - Fallback renders this exact message:
    `"Terminal window too small (minimum: 80x24). Please enlarge."`
  - Normal rendering resumes automatically when the window is enlarged to at least 80x24.

---

### 10. Dependencies
- Standard Charm libraries:
  - `github.com/charmbracelet/bubbletea` (v1.x)
  - `github.com/charmbracelet/bubbles` (v0.20.x)
  - `github.com/charmbracelet/lipgloss` (v1.0.x)

---

## A. FROZEN TUI ARCHITECTURAL DECISIONS

1. **Decoupled Opaque Boundary**: Bubble Tea operates strictly on immutable ViewModels with opaque candidate IDs. Bubble Tea has zero semantic knowledge of `CanonicalLink`, `CandidateRecord`, `BoundedHistory`, or `ScoringConfig`.
2. **Adapter Query Ordering**: The adapter owns the presentation query and applies the canonical candidate ranking policy. Bubble Tea does not reimplement or duplicate ranking logic.
3. **Lossy Event Invalidation Hints**: EventBus events are non-blocking invalidation hints. Event loss during queue saturation is acceptable; the TUI queries the authoritative Store on refresh ticks.
4. **Bounded Render Frequency**: Redraws are bounded to a maximum frequency of 10 Hz (minimum 100 ms interval) via a coalescing tick mechanism.
5. **Clean Dependency Ownership**: The application owns Store, EventBus, and RingLogHandler. The TUI adapter is a read/projection consumer of application state; it may maintain ephemeral presentation coordination state such as dirty flags and subscriber lifecycle state, but it must never maintain an independent candidate-state cache or shadow store. Headless mode instantiates zero TUI runtime components and no RingLogHandler.
6. **Strict Startup Sequencing**: `Store.Load()` and TUI adapter bus subscription occur before scheduler execution begins. Events emitted prior to Bubble Tea's event loop are not replayed; correctness is guaranteed by initial Store hydration and subsequent re-queries.
7. **Observable Lifecycle States**: CycleStatus is limited to lifecycle states explicitly observable from the existing scheduler lifecycle and cancellation mechanisms.
8. **Display Masking Invariant & Optional Clipboard**: Credential masking is mandatory for on-screen display. Clipboard integration is an optional capability; if unavailable, it fails non-fatally without degrading core TUI functionality.
9. **Single Coordinated Shutdown**: Shutdown is coordinated through a single application lifecycle path ensuring scheduler cancellation, server shutdown, Store persistence, and terminal restoration before exit.

---

## B. MVP VIEW MODEL

```go
package viewmodel

import (
	"log/slog"
	"time"
)

// CycleStatus is limited to lifecycle states explicitly observable from the existing scheduler lifecycle and cancellation mechanisms.
type CycleStatus string

const (
	CycleIdle    CycleStatus = "Idle"
	CycleRunning CycleStatus = "Running"
)

// HeaderViewModel contains global cycle stats and progress metrics.
type HeaderViewModel struct {
	CycleCount        int
	LastCycle         time.Time
	CycleStatus       CycleStatus
	ProgressCurrent   int
	ProgressTotal     int
	TotalCandidates   int
	PassedCount       int
	FailedCount       int
	InconclusiveCount int
	ServableCount     int
}

// CandidateRowViewModel represents an opaque, pre-sorted candidate row for the table.
type CandidateRowViewModel struct {
	ID               string // Opaque presentation identifier
	Protocol         string // vless, vmess, trojan, ss
	Endpoint         string // host:port
	Remark           string // user remark/tag
	Status           string // PASS, FAIL, INCON, PEND
	ScoreFormatted   string // e.g. "0.85" or "---"
	LatencyFormatted string // e.g. "142ms" or "---"
	HistoryGlyphs    string // e.g. "[●●○×●●●●●●]"
	Servable         bool
	HasPassed        bool
}

// SampleViewModel represents a single historical observation in bounded history.
type SampleViewModel struct {
	Index      int
	Age        string
	Status     string
	Category   string
	StatusCode int
	Latency    time.Duration
	Attempts   int
}

// CandidateDetailViewModel contains complete inspection data for the selected candidate.
type CandidateDetailViewModel struct {
	ID               string // Opaque presentation identifier
	MaskedLink       string // e.g. vless://[REDACTED]@host:port?...
	Protocol         string
	Endpoint         string
	Remark           string
	Status           string
	Category         string
	Reason           string
	StatusCode       int
	Score            float64
	ScoreFormatted   string
	Servable         bool
	ServabilityGate  string // Failure reason if unservable
	HasPassed        bool
	AbsentCycles     int
	TestedAt         time.Time
	Latency          time.Duration
	Attempts         int
	Warnings         []string
	Samples          []SampleViewModel
}

// LogViewModel contains log viewer display state.
type LogViewModel struct {
	Lines        []string
	ScrollOffset int
	MinLevel     slog.Level
	TotalCount   int
	FollowMode   bool
}
```

---

## C. EVENT / STATE FLOW & STARTUP ORDERING

```
Startup Sequence in main.go:
  1. logging.Setup(headless, ...) -> returns *RingLogHandler (if !headless)
  2. st := store.New(...); st.Load()
  3. bus := events.New()
  4. adapter := tui.NewAdapter(st, bus, ringHandler)
     adapter.Subscribe(bus)
     initialModel := adapter.HydrateInitialModel()
  5. sched := scheduler.New(cfg, st, bus); go sched.Run(ctx)
  6. go subserver.Run(ctx)
  7. tea.NewProgram(initialModel).Run()
```

```
Runtime Event & Render Flow:
 [Scheduler] ────── (CycleStarted / Finished) ─────┐
       │                                           │
   [Tester] ─────── (ProbeCompleted hints) ────► [EventBus] (lossy subscriber)
       │                                           │
  st.PutWithTransition(r)                          │
       ▼                                           ▼
    [Store] ◄────────────────────────────── [TUI Adapter] (dirty flag set)
(Source of Truth)                                  │
       │                                      Tick (max 10 Hz)
       │                                           ▼
       └────────────── (Query & Map) ──────► [tea.Model] (Renders pre-sorted rows)
```

---

## D. PERFORMANCE, CONCURRENCY & RESILIENCE INVARIANTS

1. **Bounded Render Frequency**: Renders are bounded to a maximum of 10 Hz (minimum 100 ms interval between renders). When `dirty == false`, no Store snapshot or ViewModel rebuild is performed; the periodic tick performs only the minimum scheduling work required for refresh coordination.
2. **Lossy Delivery Tolerance**: If the TUI subscriber queue overflows during probe bursts, discarded events do not corrupt state. The next refresh tick queries authoritative Store state directly.
3. **Deterministic Query Ordering**: The adapter sorts candidates deterministically before passing ViewModels to Bubble Tea:
   - Servable before unservable.
   - Within each group: proven (`HasPassed == true`) before unproven (`HasPassed == false`).
   - Score descending.
   - Latency ascending.
   - Internal identity ascending (tie-breaker).
4. **Non-Blocking Store Access**: Store read locks during ViewModel creation are short-lived, ensuring tester workers are never blocked.
5. **No Pointer Retention**: Bubble Tea models retain only immutable values and strings; zero pointers to internal Store structures leak into the UI runtime.

---

## E. DEFERRED FEATURES (NON-GOALS FOR MVP)

1. **Interactive Config Editing**: Editing URLs, subscriptions, or sing-box configurations.
2. **Manual Per-Candidate Probe Triggers**: Triggering probes on individual candidates from the TUI.
3. **Mouse Support**: Mouse clicks and mouse-wheel scrolling.
4. **Complex Modal Dialogs**: Multi-step modal workflows or popups.
5. **Multi-Tab Routing**: Deep tab navigation; MVP uses a clean split-pane / single-key toggle between Candidate View and Log View.
6. **Live Traffic Proxying**: Routing active user traffic through candidates from the TUI.
7. **Candidate Creation**: Adding new candidate configurations interactively.

---

## F. IMPLEMENTATION INVARIANTS

1. **Zero Production Changes**: This specification freezes the contract. No code is modified during this audit.
2. **Headless Runtime Isolation**: Headless mode must execute without instantiating any TUI runtime components (no Bubble Tea program, no TUI adapter, no TUI event subscriber, no TUI timers/tickers, no TUI goroutines, and no RingLogHandler).
3. **Preserve Existing Tests**: All existing test suites across `store`, `scheduler`, `tester`, `events`, and `logging` must continue passing without regression.
4. **Zero Shadow Store**: Any UI implementation that attempts to cache or maintain an independent map of candidate states violates this contract.
5. **Graceful Terminal Teardown**: Alternate screen buffers must always be released and cooked mode restored upon application exit across all exit paths.

---

TUI MVP DESIGN CONTRACT: FROZEN
