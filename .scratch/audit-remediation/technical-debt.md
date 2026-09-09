# Technical Debt Register

The canonical local register for non-blocking findings, deferred hardening items, code quality cleanup, and upstream dependency issues identified during architecture audits and independent code reviews in `gemsub`.

---

## 1. Summary & Classification

| Classification | Count | Description |
|:---|:---:|:---|
| **Active Technical Debt (P1)** | 0 | Material architectural, product, or reliability issues requiring planned engineering tickets. |
| **Future Hardening & Observability (P2)** | 4 | Non-blocking enhancements to metrics, operational logging, scheduling fairness, or optional configuration. |
| **Code Quality & Test Cleanup (P2)** | 7 | Maintainability, naming clarity, deduplication, and test fixture consolidation. |
| **External & Upstream Tracking** | 1 | Issues rooted in external dependencies tracked across upstream releases. |
| **Promoted / Resolved** | 3 | Items promoted to active GitHub issues or resolved in implementation commits. |
| **Total Registered Items** | **14** | |

---

## 2. Register Index

| ID | Title | Priority | Area | Origin | Status |
|:---|:---|:---:|:---|:---|:---:|
| [TD-001](#td-001--publisher-atomic-publication) | Publisher atomic publication | P1 | Publisher | Ticket 23 Review / Post-Ticket-23 Audit | Resolved (Ticket 26, e02c9c0) |
| [TD-002](#td-002--scheduler-candidate-rotation-under-probelimit) | Scheduler candidate rotation under ProbeLimit | P2 | Scheduler / Scheduling Fairness | Post-Ticket-23 Audit / TD-002 Audit | Open / Deferred |
| [TD-003](#td-003--tui-dual-projection-observability) | TUI dual-projection observability | P1 | TUI | Post-Ticket-23 Audit | Resolved (Ticket 25, 33329c0) |
| [TD-004](#td-004--scheduler-generic-servability-metrics) | Scheduler generic servability metrics | P2 | Scheduler / Observability | Post-Ticket-23 Audit | Open |
| [TD-005](#td-005--serveconfig-projection-alignment) | ServeConfig projection alignment | P2 | Configuration / Subserver | Post-Ticket-23 Audit | Open / Deferred |
| [TD-006](#td-006--publishertest-code-cleanup) | Publisher test fixture cleanup | P2 | Test / Code Quality | Ticket 23 Review | Open |
| [TD-007](#td-007--subserver-code-hygiene) | Subserver code hygiene | P2 | Subserver | Ticket 24 Review | Open |
| [TD-008](#td-008--upstream-sing-box-race-tracking) | Upstream sing-box race tracking | External | Dependency (`sing-box`) | Tickets 18/21/22 / Post-Ticket-23 Audit | Monitoring |
| [TD-009](#td-009--tui-narrow-terminal-header-layout) | TUI narrow-terminal header layout | P2 | TUI / Presentation | Ticket 25 independent review | Open |
| [TD-010](#td-010--tui-presentation-ranking-divergence-documentation) | TUI presentation ranking divergence documentation | P2 | TUI / Architecture Documentation | Ticket 25 independent review | Open / Documentation |
| [TD-011](#td-011--tui-legacyunknown-transport-detail-test-coverage) | TUI legacy/unknown transport detail test coverage | P2 | TUI / Tests | Ticket 25 independent review | Open |
| [TD-012](#td-012--publisher-atomicwritefile-api-visibility) | Publisher AtomicWriteFile API visibility | P2 | Publisher / Code Quality | Ticket 26 independent code review | Open |
| [TD-013](#td-013--publisher-filesystem-failure-injection-api-visibility) | Publisher filesystem failure-injection API visibility | P2 | Publisher / Testability | Ticket 26 independent code review | Open |
| [TD-014](#td-014--publisher-owned-path-slice-deduplication) | Publisher owned-path slice deduplication | P2 | Publisher / Code Quality | Ticket 26 independent code review | Open |

---

## 3. Active Technical Debt (P1)

### TD-001 — Publisher atomic publication

- **ID:** `TD-001`
- **Title:** Publisher atomic publication and rollback semantics
- **Priority:** P1
- **Area:** Publisher
- **Status:** Resolved
- **Resolution Ticket:** Ticket 26
- **GitHub Issue:** #4
- **Commit:** `e02c9c08da79624287c3e2098b5d97e8e9295fcf`
- **Date Resolved:** 2026-09-09
- **Origin:** Ticket 23 independent code review / Post-Ticket-23 architecture audit
- **Problem:**
  The Publisher writes generated projection files (`generic/*.txt`, `gemini/*.txt`, `meta.json`) directly into the active working tree of the target publication repository prior to Git staging. If a filesystem write fails mid-cycle (e.g. out of disk space, permission error, process interruption), the publication directory is left in a partially updated state. In subsequent cycles, `checkWorkingTreeSafety` detects uncommitted modifications and halts further automated publishing until manual intervention.
- **Impact:**
  Unattended daemon deployments can experience permanent publishing stoppage following a transient filesystem failure.
- **Proposed Future Remediation:**
  Render the complete publication tree into an isolated temporary/scratch directory, then atomically copy or swap files into the target repository, or implement automated rollback semantics on generation failure before Git staging is reached.
- **Dependencies:** `internal/publisher`
- **Promotion Criteria:**
  Promote when publication reliability in unattended production environments becomes a requirement, or prior to expanding Publisher functionality to remote Git push / multi-branch delivery.

---

### TD-003 — TUI dual-projection observability

- **ID:** `TD-003`
- **Title:** TUI dual-projection observability (Generic vs. Gemini)
- **Priority:** P1
- **Area:** TUI
- **Status:** Resolved
- **Resolution Ticket:** Ticket 25
- **GitHub Issue:** #3
- **Commit:** `33329c08334a2bbf6c1212b11eeac4ef8bd02006`
- **Date Resolved:** 2026-09-09
- **Origin:** Post-Ticket-23 architecture audit
- **Problem:**
  The Terminal User Interface (TUI) was built around a single-projection mental model (`Store.Passing()`). In the current TUI, candidates that pass Stage 1 transport health but fail Stage 2 Gemini application verification (such as `ErrRegionBlocked` or `ErrTargetDenied`) appear as generic failures. There is no visible indicator or counter showing the health of the generic/network-healthy candidate pool (`Store.NetworkPassing()`).
- **Impact:**
  Operators cannot observe generic proxy availability, transport latencies, or dual-projection yields from the interactive terminal interface.
- **Proposed Future Remediation:**
  Update TUI view models, summary headers, and candidate row renderers to surface dual-projection metrics: generic servable count (`Stats.GenericServable`), Gemini servable count (`Stats.Servable`), and explicit visual indicators for transport-healthy versus target-blocked proxies without duplicating Store logic.
- **Dependencies:** `internal/tui`, `internal/store`
- **Promotion Criteria:**
  Promote when interactive terminal monitoring of generic/network-healthy proxies is required by operators.

---

## 4. Future Hardening & Observability (P2)

### TD-002 — Scheduler candidate rotation under ProbeLimit

- **ID:** `TD-002`
- **Title:** Scheduler candidate rotation under ProbeLimit
- **Priority:** P2
- **Area:** Scheduler / Scheduling Fairness
- **Status:** Open / Deferred
- **Origin:** Post-Ticket-23 Architecture Audit / TD-002 implementation audit
- **Audit Disposition:**
  Original P1 correctness hypothesis refuted by source inspection and existing `TestScoring_CycleAbortAndSkippedProbes`. Reclassified to P2 scheduling fairness enhancement.
- **Problem:**
  `SchedulerConfig.ProbeLimit` intentionally limits how many candidates are probed within a cycle. `Store.StartCycle` receives the complete upstream source link set and correctly treats unprobed-but-present candidates as present (`skipped != failed, skipped != absent`). Therefore, Store absence accounting must remain unchanged. The remaining limitation is that `internal/scheduler/scheduler.go` currently executes static slice head truncation (`candidates[:s.cfg.ProbeLimit]`), meaning candidates beyond `ProbeLimit` can remain unprobed indefinitely when the same ordered source pool is repeatedly fetched.
- **Impact:**
  Candidate starvation / incomplete verification coverage when `ProbeLimit > 0` and the candidate pool exceeds `ProbeLimit`.
- **Proposed Future Remediation:**
  Add scheduler-side candidate rotation (e.g. stateful round-robin offset or prioritizing least-recently-tested nodes) or another explicit fair selection strategy so candidates beyond the current `ProbeLimit` boundary eventually receive probe opportunities across cycles.
  - Do **NOT** change `Store.StartCycle` or `Store.FinishCycle` for this concern.
  - Do **NOT** use absence semantics as a proxy for probe scheduling.
  - Candidate rotation is a fairness/product enhancement, not a Store correctness fix.
- **Dependencies:** `internal/scheduler`
- **Promotion Criteria:**
  Promote to a dedicated implementation ticket only when bounded probing is an intentional operational mode and fair verification of candidate pools larger than `ProbeLimit` becomes a product requirement.

---

### TD-004 — Scheduler generic servability metrics

- **ID:** `TD-004`
- **Title:** Scheduler generic servability metrics in cycle logging
- **Priority:** P2
- **Area:** Scheduler / Observability
- **Status:** Open
- **Origin:** Post-Ticket-23 architecture audit
- **Problem:**
  At the end of each test cycle, `scheduler.runCycle` logs operational summary statistics (`slog.Info("scheduler: cycle completed", ...)`), reporting `total`, `passed`, `failed`, `inconclusive`, and `servable`. However, `servable` only counts Gemini-servable candidates (`Stats.Servable`). It omits `Stats.GenericServable`, leaving headless daemon logs opaque regarding generic proxy yield.
- **Impact:**
  Log-based operational alerting cannot detect drops in generic proxy availability.
- **Proposed Future Remediation:**
  Add `generic_servable` attribute to `scheduler: cycle completed` log entries and any related health event bus payloads.
- **Dependencies:** `internal/scheduler`, `internal/store`
- **Promotion Criteria:**
  Promote when cycle metrics logging is updated or bundled with a general observability improvement pass.

---

### TD-005 — ServeConfig projection alignment

- **ID:** `TD-005`
- **Title:** ServeConfig projection-routing configuration alignment
- **Priority:** P2
- **Area:** Configuration / Subserver
- **Status:** Open / Deferred
- **Origin:** Post-Ticket-23 architecture audit
- **Problem:**
  `config.ServeConfig` exposes `Listen`, `Path`, and `Format`, but contains no fields to configure projection routing defaults (e.g., mapping the root `/sub` endpoint to `generic` instead of `gemini`, or toggling specific projection paths). Ticket 24 implemented routing convention-over-configuration (dedicated `/sub/generic`, `/sub/gemini`, and `/sub?projection=...`), which satisfies current backward-compatibility and functionality requirements.
- **Impact:**
  Low. Users cannot alter the default projection served on the base path via `config.json`.
- **Proposed Future Remediation:**
  If configuration-driven projection mapping becomes necessary, add an optional `DefaultProjection string` field to `ServeConfig` with validation (`"gemini"` or `"generic"`).
- **Dependencies:** `internal/config`, `internal/subserver`
- **Promotion Criteria:**
  Promote only if operators explicitly require configuration-level control over base path projection mapping.

---

### TD-009 — TUI narrow-terminal header layout

- **ID:** `TD-009`
- **Title:** TUI narrow-terminal header layout
- **Priority:** P2
- **Area:** TUI / Presentation
- **Status:** Open
- **Origin:** Ticket 25 independent review
- **Problem:**
  The Ticket 25 dual-servability header line can exceed 80 columns when cycle metrics and probe progress are displayed together, causing line wrapping on the minimum 80-column terminal.
- **Impact:**
  Minor presentation degradation and one-line header displacement on narrow terminals.
- **Proposed Future Remediation:**
  Introduce width-aware header formatting, abbreviated labels, or conditional omission/truncation of lower-priority progress information when terminal width is constrained.
  - Do **NOT** change Ticket 25 implementation now merely to address this debt item.
- **Dependencies:** `internal/tui/model.go`
- **Promotion Criteria:**
  Promote during a dedicated TUI responsive layout or cosmetic polish pass.

---

## 5. Code Quality & Test Cleanup (P2)

### TD-006 — Publisher/test code cleanup

- **ID:** `TD-006`
- **Title:** Publisher test fixture consolidation and deduplication
- **Priority:** P2
- **Area:** Test / Code Quality
- **Status:** Open
- **Origin:** Ticket 23 independent code review
- **Problem:**
  Multiple integration tests in `internal/publisher/publisher_test.go` independently execute identical Git repository initialization steps (`git init`, setting user/email, creating initial commit, setting main branch).
- **Impact:**
  Test boilerplate duplication; minor maintenance friction when modifying test harness setup.
- **Proposed Future Remediation:**
  Extract a shared `setupTestGitRepo(t *testing.T) string` helper function within `publisher_test.go`.
- **Dependencies:** `internal/publisher/publisher_test.go`
- **Promotion Criteria:**
  Promote during a dedicated test harness maintenance or code-hygiene pass.

---

### TD-007 — Subserver code hygiene

- **ID:** `TD-007`
- **Title:** Subserver code hygiene and naming refinements
- **Priority:** P2
- **Area:** Subserver
- **Status:** Open
- **Origin:** Ticket 24 independent code review
- **Problem:**
  Minor code-quality and naming items identified during Ticket 24 independent review:
  1. Base-path normalization logic duplicated verbatim in `Server.Run` and `Server.handleSub`.
  2. Query parameter variables `p1` and `p2` in `handleSub` are non-descriptive.
  3. Raw string literals used for projection names (`"generic"`, `"gemini"`) and formats (`"raw"`, `"base64"`).
  4. Repetitive table assertion loops in `TestServer_EmptyProjections`.
- **Impact:**
  Minor readability and maintainability friction. No behavioral or runtime defects.
- **Proposed Future Remediation:**
  Extract `normalizeBasePath(p string) string` helper, rename `p1`/`p2` to `protocolParam`/`protoParam`, declare typed constants for projections and formats, and consolidate empty projection test cases into a subtest table.
- **Dependencies:** `internal/subserver/server.go`, `internal/subserver/server_test.go`
- **Promotion Criteria:**
  Promote during a dedicated code-hygiene pass or when `internal/subserver` is next touched for feature work.

---

### TD-010 — TUI presentation ranking divergence documentation

- **ID:** `TD-010`
- **Title:** TUI presentation ranking divergence documentation
- **Priority:** P2
- **Area:** TUI / Architecture Documentation
- **Status:** Open / Documentation
- **Origin:** Ticket 25 independent review
- **Problem:**
  The TUI CandidateRows ordering intentionally differs from `Store.NetworkPassingRanked()` for Tier 2 candidates. Store delivery ordering prioritizes network latency, while the TUI presentation ordering also considers `HasPassed` and reliability score for operator readability.
- **Impact:**
  No runtime or delivery correctness impact, but the distinction must remain explicit to prevent future accidental coupling or assumptions that both orderings are identical.
- **Proposed Future Remediation:**
  Document this as an intentional boundary:
  - Store ranking is canonical for delivery projections.
  - TUI ordering is presentation-specific.
  - Do not unify the ranking implementations unless a future architecture decision explicitly requires a shared ordering contract.
- **Dependencies:** `internal/tui/adapter/adapter.go`, `docs/agents/domain.md`
- **Promotion Criteria:**
  Promote when updating repository architecture documentation or during a general domain context refresh.

---

### TD-011 — TUI legacy/unknown transport detail test coverage

- **ID:** `TD-011`
- **Title:** TUI legacy/unknown transport detail test coverage
- **Priority:** P2
- **Area:** TUI / Tests
- **Status:** Open
- **Origin:** Ticket 25 independent review
- **Problem:**
  The `CandidateDetail` transport fallback for `TransportEvidenceKnown=false` is implemented defensively, but the current unit-test suite does not directly assert the legacy/unknown rendering branch.
- **Impact:**
  Test coverage gap for legacy/unproven candidate detail display. No runtime defect.
- **Proposed Future Remediation:**
  Add a deterministic unit test covering:
  - `TransportEvidenceKnown=false`
  - `NetworkHealthy=false`
  - rendered transport state = `UNKNOWN`
  - `Evidence=legacy`
  - no panic
  - Also test the legacy `NetworkHealthy=true` case if useful.
  - Do not modify Ticket 25 tests now.
- **Dependencies:** `internal/tui/model_test.go`
- **Promotion Criteria:**
  Promote during a dedicated TUI test suite maintenance pass.

---

### TD-012 — Publisher AtomicWriteFile API visibility

- **ID:** `TD-012`
- **Title:** Publisher AtomicWriteFile API visibility
- **Priority:** P2
- **Area:** Publisher / Code Quality
- **Status:** Open
- **Origin:** Ticket 26 independent code review
- **Problem:**
  `AtomicWriteFile` is exported on package `publisher` although it is an internal helper whose production callers are inside the publisher package. The current black-box tests access it directly.
- **Impact:**
  Unnecessary package API surface expansion within `internal/publisher`. Since the package is internal, there is no risk of external module leakage, but repository conventions favor keeping internal utilities unexported.
- **Proposed Future Remediation:**
  Consider making the helper unexported or using an `export_test.go` bridge during future test maintenance.
  - This is non-blocking and requires no immediate remediation.
- **Dependencies:** `internal/publisher/publisher.go`, `internal/publisher/publisher_test.go`
- **Promotion Criteria:**
  Promote during future publisher test refactoring or code maintenance.

---

### TD-013 — Publisher filesystem failure-injection API visibility

- **ID:** `TD-013`
- **Title:** Publisher filesystem failure-injection API visibility
- **Priority:** P2
- **Area:** Publisher / Testability
- **Status:** Open
- **Origin:** Ticket 26 independent code review
- **Problem:**
  `WithFSOverrides` is exported on `Publisher` for filesystem failure injection in tests.
- **Impact:**
  Exposes a test-specific configuration method on the production `Publisher` struct.
- **Proposed Future Remediation:**
  Evaluate whether a narrower test-only seam or constructor would reduce package API surface while preserving hermetic failure testing.
  - This is non-blocking and requires no immediate remediation.
- **Dependencies:** `internal/publisher/publisher.go`, `internal/publisher/publisher_test.go`
- **Promotion Criteria:**
  Promote during a dedicated test harness maintenance or code-hygiene pass.

---

### TD-014 — Publisher owned-path slice deduplication

- **ID:** `TD-014`
- **Title:** Publisher owned-path slice deduplication
- **Priority:** P2
- **Area:** Publisher / Code Quality
- **Status:** Open
- **Origin:** Ticket 26 independent code review
- **Problem:**
  The combined `targetFiles + legacyRootFiles` slice construction is duplicated in multiple locations.
- **Impact:**
  Minor code duplication and repetitive slice allocations across publication operations.
- **Proposed Future Remediation:**
  Consider extracting an immutable `allPublisherFiles` package-level slice/helper.
  - This is non-blocking cleanup only.
- **Dependencies:** `internal/publisher/publisher.go`
- **Promotion Criteria:**
  Promote during future code cleanup or optimization of `internal/publisher`.

---

## 6. External & Upstream Tracking

### TD-008 — Upstream sing-box race tracking

- **ID:** `TD-008`
- **Title:** Upstream sing-box interface monitor data race tracking
- **Priority:** External / Upstream
- **Area:** External Dependency (`github.com/sagernet/sing-box`)
- **Status:** Monitoring
- **Origin:** Ticket 18/21/22 race verification and Post-Ticket-23 architecture audit
- **Problem:**
  Running `go test -race` against tests that instantiate live `box.Box` instances via `sing-box` (e.g. `TestEndToEnd_PositivePath` or `TestExecuteAttempt_TransportErrorsNotCollapsed`) detects a data race in `github.com/sagernet/sing-box@v1.14.0/route/network.go:574` inside the interface monitor during concurrent Box startup and shutdown.
- **Impact:**
  This is an upstream dependency issue, not a `gemsub`-native concurrency defect. It prevents clean `-race` execution on tests creating live outbound boxes unless upstream patches it or `gemsub` upgrades to a fixed version.
- **Proposed Future Remediation:**
  Monitor upstream `sagernet/sing-box` releases for fixes to the network route interface monitor. When an updated release is published, upgrade the dependency in `go.mod` and verify race detector output. Do NOT implement speculative local workarounds.
- **Dependencies:** Upstream `github.com/sagernet/sing-box`
- **Promotion Criteria:**
  Promote to a dependency upgrade ticket when an upstream sing-box release resolves the interface monitor race.

---

## 7. Promoted & Closed Items

### TD-001 — Publisher atomic publication
- **Status:** Resolved
- **Resolution Ticket:** Ticket 26
- **GitHub Issue:** #4
- **Commit:** `e02c9c08da79624287c3e2098b5d97e8e9295fcf`
- **Date Resolved:** 2026-09-09

### TD-003 — TUI dual-projection observability
- **Status:** Resolved
- **Resolution Ticket:** Ticket 25
- **GitHub Issue:** #3
- **Commit:** `33329c08334a2bbf6c1212b11eeac4ef8bd02006`
- **Date Resolved:** 2026-09-09

### Record Template for Promoted Items

```markdown
### TD-XXX — Title
- **Status:** Promoted
- **Promoted to Ticket:** Ticket <number>
- **GitHub Issue:** #<issue_number>
- **Date Promoted:** YYYY-MM-DD
```

### Record Template for Resolved Items

```markdown
### TD-XXX — Title
- **Status:** Resolved
- **Resolution Ticket:** Ticket <number>
- **Commit:** <commit_hash>
- **Date Resolved:** YYYY-MM-DD
```

---

## 8. Debt Management Workflow

To maintain architectural integrity and prevent review fatigue, the team adheres to the following workflow for technical debt:

```text
1. Independent Code Review / Architecture Audit identifies finding
                          ↓
2. Determine if finding blocks current ticket
        ├── YES (P0 Blocker) ──────→ Must fix before commit
        └── NO (Non-blocking)
                    ↓
3. Classify finding
        ├── Material issue (P1) ───→ Register in Technical Debt Register
        ├── Non-blocking (P2) ─────→ Register in Technical Debt Register
        └── Upstream dependency ───→ Register in Technical Debt Register (Monitoring)
                    ↓
4. Backlog triage & prioritization
        ├── High priority / scope ─→ Promote via /to-tickets (creates GitHub Issue)
        └── Low priority / minor ───→ Retain in register until batched or relevant file touched
                    ↓
5. Implementation & Verification
                    ↓
6. Mark Resolved in Register with Ticket number and Commit hash
```

### Key Operating Rules

1. **No Phantom Tickets:** Do not create GitHub Issues for minor P2 code cleanup items until they are scheduled for active engineering work.
2. **Immutability of Scope:** Independent code reviews must not introduce out-of-scope refactoring into an active implementation ticket; non-blocking findings must be registered here.
3. **Traceability:** Every debt entry must link back to its originating ticket or audit and state clear promotion criteria.
4. **Clean Resolution:** When a debt item is resolved, record the resolving ticket number and Git commit hash in the Promoted & Closed section.
