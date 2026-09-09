# 25: feat(tui): expose dual generic and gemini projection observability

Type: feature
Status: ready-for-agent
Blocked by: 20, 23, 24

## Problem

The Terminal User Interface (`internal/tui`) was designed when `gemsub` operated on a single-projection model (`Store.Passing()`). Following the delivery of two-stage probing (Tickets 18–22), dual-projection Git publishing (Ticket 23), and dual-projection HTTP delivery (Ticket 24), the Store and Tester maintain two distinct canonical projections:

1. `Store.NetworkPassing()`: Transport-healthy candidates whose Stage 1 HTTPS probe passed, regardless of Stage 2 Gemini status (including RegionBlocked, TargetDenied, or fresh Inconclusive).
2. `Store.Passing()`: Fully servable candidates that passed both Stage 1 transport and Stage 2 Gemini application gates.

However, the TUI has zero visibility into generic servability or transport evidence:
- The summary header only counts `stats.Servable`, hiding generic proxy yield entirely.
- In the candidate table, candidates with healthy transport that are blocked by Gemini (e.g. `ErrRegionBlocked`, `ErrTargetDenied`) are displayed with a red `FAIL` badge and an empty `[ ]` servable marker, visually indistinguishable from broken/dead proxies.
- Candidate table sorting lumps network-healthy candidates at the bottom with dead proxies rather than adhering to the Store's tiered ranking precedence.
- The `s` key filter strictly filters by Gemini servability, preventing operators from isolating network-healthy proxies.
- The candidate detail view displays Gemini Gate failures without revealing whether Stage 1 transport succeeded, what transport latency was recorded, or whether transport evidence is known versus legacy.

## Current Behavior

- `viewmodel.HeaderViewModel`: Exposes only `ServableCount int` (mapped from `stats.Servable`). Omits `stats.GenericServable`.
- `viewmodel.CandidateRowViewModel`: Exposes `Servable bool` (Gemini-only) and `Status string` (derived strictly from `Latest.Status`, which reflects the Gemini probe outcome).
- `viewmodel.CandidateDetailViewModel`: Exposes `Servable bool` and `ServabilityGate string` (Gemini-only). Lacks `NetworkHealthy`, `TransportOK`, `TransportLatency`, and `TransportEvidenceKnown`.
- `adapter.CandidateRows`:
  - Drops `snap.NetworkHealthy`, `snap.TransportOK`, `snap.TransportLatency`, and `snap.TransportEvidenceKnown`.
  - Sorts candidates strictly by `snap.Servable` before score/latency, ignoring `snap.NetworkHealthy`.
  - Filters by `servableOnly && !snap.Servable`, hiding generic-only candidates.
- `tui.Model`:
  - Header renders: `Servable: <GeminiCount> / <Total>`, misleading operators into believing 0 proxies are usable when hundreds of network-healthy proxies are active.
  - Table renders: `[ ] ... FAIL` in red for RegionBlocked/TargetDenied nodes.
  - Detail view renders: `Servable: NO` with no transport status line.
  - Keystroke `s`: Toggles Gemini-only servable filter.

## Desired Behavior

1. **Header Dual Metrics**:
   - Header displays both Gemini and Generic servable counts:
     `Servable: Gemini: <gemini_count>  Generic: <generic_count> / <total>`
   - Retains active cycle progress, status, and lifecycle metrics unchanged.

2. **Candidate Row Dual Projection Badges**:
   - `CandidateRowViewModel` carries both `Servable` (Gemini) and `NetworkHealthy` (Generic), as well as `TransportOK` and `TransportEvidenceKnown`.
   - Table `SERV` column represents dual servability cleanly (e.g., `[✓✓]` for both, `[G ]` or `[•✓]` for generic-only, `[  ]` for unservable, or dedicated badges).
   - `STATUS` column distinguishes target policy blocks (`BLOCKED` or `DENIED` in yellow/dim) from genuine transport failures (`FAIL` in red) and transport successes (`PASS` in green).

3. **Tiered Ranking Alignment**:
   - Candidate sorting order in `Adapter.CandidateRows` matches canonical Store projection precedence (`Store.NetworkPassingRanked`):
     - Tier 1: Gemini-servable (`Servable == true`)
     - Tier 2: Generic-servable / Network-healthy (`NetworkHealthy == true && Servable == false`)
     - Tier 3: Unservable candidates
     - Within each tier: proven candidates first, then reliability score descending, then latency ascending.

4. **Multi-Mode Servability Filter**:
   - Keystroke `s` cycles through filter states:
     `All Candidates` → `Gemini Servable` → `Generic Servable` → `All Candidates`
   - Updates status bar message accordingly (`"Filter: Gemini servable"`, `"Filter: Generic servable"`, `"Filter: All candidates"`).

5. **Candidate Detail Transport Section**:
   - Detail view exposes explicit dual servability:
     `Servable:  Gemini: YES/NO (Gate: ...)  |  Generic: YES/NO`
   - Detail view includes Stage 1 Transport inspection line:
     `Transport: Status=OK/FAILED/UNKNOWN  Latency=120ms  Evidence=explicit/legacy`
   - Retains credential masking, sample history, and warning list.

## Exact Scope

- `internal/tui/viewmodel/viewmodel.go`: Add dual projection and transport evidence fields to `HeaderViewModel`, `CandidateRowViewModel`, and `CandidateDetailViewModel`.
- `internal/tui/adapter/adapter.go`: Populate dual projection fields from `CandidateSnapshot` and `Store.Stats()`; implement tiered ranking; support multi-mode servable filter.
- `internal/tui/model.go`: Render dual servability in header line 2, table `SERV`/`STATUS` columns, candidate detail inspection view, and cycle filter keystroke `s`.
- `internal/tui/adapter/adapter_test.go`: Unit tests for adapter mapping, header stats, tiered sorting, and filter modes.
- `internal/tui/model_test.go`: View rendering tests for dual metrics, badges, and detail inspection.

## Explicit Non-Goals

- Do NOT redesign TUI layout, styles, color themes, or border decorations.
- Do NOT alter keyboard shortcuts other than enhancing `s` filter cycling.
- Do NOT modify `Store` state, classification rules, or persistence schemas (consume existing APIs only).
- Do NOT introduce presentation-layer classification heuristics or shadow store logic.
- Do NOT modify `Tester` or `Scheduler`.

## Architectural Invariants Preserved

1. **Store Authority**: The TUI never computes candidate health, servability, or decay. It consumes `CandidateSnapshot.NetworkHealthy`, `CandidateSnapshot.Servable`, `CandidateSnapshot.TransportOK`, and `Store.Stats()` directly.
2. **Deterministic Opaque Mapping**: Ephemeral presentation IDs (`cand-1`, `cand-2`) and credential masking (`MaskLink`) remain strictly enforced.
3. **Eventual Convergence**: Dirty tracking, Store revision checks, and 10 Hz refresh throttling remain intact without regressions.

## Acceptance Criteria

- [ ] `HeaderViewModel` exposes `GenericServableCount` and `ServableCount`, populated from `Store.Stats()`.
- [ ] Header line 2 displays both `Gemini` and `Generic` servable counts alongside total candidates.
- [ ] Candidates with Stage 1 Transport OK and Gemini `ErrRegionBlocked` or `ErrTargetDenied` are visibly distinguished from transport failures in table rows.
- [ ] Candidate table sorting places Gemini-servable candidates first, Generic-servable candidates second, and failed candidates third.
- [ ] Keystroke `s` cycles through All -> Gemini Servable -> Generic Servable -> All.
- [ ] Candidate inspection detail displays dual servability (`Gemini` and `Generic`) and explicit `Transport` health and latency.
- [ ] Unproven and legacy candidates without explicit transport evidence are gracefully indicated as `UNKNOWN` or `LEGACY` without panics.
- [ ] Bubble Tea Model renders cleanly in standard 80x24 terminal dimensions without line wrap artifacts.
- [ ] All unit and regression tests in `internal/tui/...` pass with zero race conditions.

## Dependencies

- **Ticket 20**: `Store.Result` and `CandidateSnapshot` explicit transport evidence (`NetworkHealthy`, `TransportOK`, `TransportLatency`, `TransportEvidenceKnown`).
- **Ticket 24**: `Store.Stats().GenericServable` added and verified against `Store.NetworkPassing()`.

## Compatibility Requirements

- Minimum terminal size requirement remains 80x24.
- Clipboard copy (`y` key) continues copying unmasked `ActiveLink`.
- Headless execution (`gemsub -headless`) is unaffected.
