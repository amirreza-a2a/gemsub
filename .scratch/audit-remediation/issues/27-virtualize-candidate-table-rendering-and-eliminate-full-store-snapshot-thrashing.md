# 27: perf(tui): virtualize candidate table rendering and eliminate full-store snapshot thrashing

Type: performance / UX
Priority: P0
Area: TUI / Performance
Status: ready-for-agent
Blocked by: 25
Source Debt: TD-015 (TUI candidate table rendering and full-store snapshot thrashing)

## Problem

The Terminal User Interface (TUI) currently requests full `Store` snapshots at approximately 10 Hz via `PollSnapshot` in the Bubble Tea event loop (`internal/tui/model.go`). The `Store.Snapshots()` path clones and sorts the entire active candidate population on every dirty tick.

In production deployments with public subscription aggregators, the candidate population reaches approximately 56,799 records. At this scale, each call to `Store.Snapshots()` causes:
- ~48.4 ms CPU consumption per snapshot
- ~80.17 MB heap allocation per snapshot
- ~567,990 individual slice and struct allocations per snapshot
- ~800 MB/s allocation pressure at the 10 Hz polling rate

Because `Store.Revision()` increments on every probe outcome during testing cycles, the TUI detects a dirty state on almost every 100 ms tick. This generates catastrophic Garbage Collection (GC) pressure, frame drops, high CPU load (pegging 50% of a CPU core), and an unresponsive terminal interface, even though the terminal viewport only displays approximately 20 candidate rows at any given time.

## Current Behavior

1. **Full Population Cloning**: `Adapter.CandidateRows()` calls `Store.Snapshots()`, which takes `s.mu.RLock()` and invokes `rec.Clone()` across all 56,799 candidates, deep-copying bounded sample slices for every candidate regardless of visibility.
2. **Eager ViewModel Materialization**: `CandidateRows` constructs 56,799 `viewmodel.CandidateRowViewModel` structs on every refresh, allocating two full-population lookup maps (`linkToID` and `idToLink`) and sorting the entire dataset with `sort.Slice`.
3. **High-Frequency Churn**: The 100 ms background tick (`tickCmd()`) triggers full snapshot rebuilds whenever any probe finishes, conflating fast-changing cycle progress counters (passed/failed counts) with slow-changing candidate table ordering.
4. **Viewport Disconnect**: Bubble Tea receives a 56,799-element slice and discards over 99.9% of the rendered data to display the ~20 visible table rows.

## Desired Behavior

1. **Eliminate Full-Store Cloning on Normal Refreshes**:
   - The TUI must NOT deep-clone or materialize all candidate records on every refresh tick.
   - Separate candidate index/reference ordering from row viewmodel materialization.
2. **Virtual Viewport Materialization**:
   - Materialize only the `CandidateRowViewModel` instances required for the active terminal viewport (e.g. `cursorIndex` window of ~20–30 rows).
3. **Stable Candidate Identification**:
   - Preserve stable opaque presentation IDs and selection state across refreshes, scrolling, and filter toggling.
4. **Throttled Candidate Ordering & Decoupled Metrics**:
   - Decouple fast-changing progress/header metrics from expensive candidate ordering.
   - Update header/progress information at high frequency (e.g. 10 Hz) from `Store.Stats()` without re-sorting the candidate table.
   - Throttle full candidate sorting and reconciliation to approximately 1 Hz, unless explicit operator interaction (e.g. keystroke, scrolling, filter toggle) demands an immediate refresh.
5. **Preserve Projection and Ranking Semantics**:
   - Preserve existing filter transitions: `All Candidates` → `Gemini Servable` → `Generic Servable` → `All Candidates`.
   - Preserve candidate tier ordering established by Ticket 25:
     - Tier 1: Gemini-servable (`Servable == true`)
     - Tier 2: Generic-servable / Network-healthy (`NetworkHealthy == true && Servable == false`)
     - Tier 3: Unservable candidates
     - Within each tier: proven candidates first, then score descending, then latency ascending, then stable identity tie-breaker.
6. **Preserve Cursor and Navigation Invariants**:
   - Cursor navigation (`j`, `k`, `g`, `G`, `pgup`, `pgdown`) and detail modal inspection (`enter`, `space`) must remain smooth and predictable under 60k candidates.
   - Cursor position must remain correctly clamped when filtered candidate membership changes between index rebuilds.
7. **Architectural Seams**:
   - Store remains authoritative for candidate state, health, scoring, and projection membership.
   - A single minimal read-only presentation/index method may be added to `internal/store/store.go`. This seam MUST expose only lightweight sortable candidate information (~40 bytes per record) and MUST NOT deep-copy `BoundedHistory` or clone records.
   - No Store scoring, servability, persistence, domain-policy, or authoritative semantic changes are permitted.
   - Do not duplicate domain decisions in the TUI presentation layer.
8. **Reconcile Cycle State Cloned Path Elimination**:
   - `reconcileCycleStateLocked` in `internal/tui/adapter/adapter.go` must also avoid the full `Store.Snapshots()` cloning path. It may use `Store.Stats()` plus targeted record access or the new lightweight index.

## Exact Scope

- `internal/tui/adapter/adapter.go`
- `internal/tui/adapter/adapter_test.go`
- `internal/tui/model.go`
- `internal/tui/model_test.go`
- `internal/tui/viewmodel/viewmodel.go`
- `internal/store/store.go` (strictly limited to one minimal read-only presentation/index method; no domain logic, scoring, servability, persistence, or policy changes)

## Out of Scope

- Modifying Store domain scoring, servability logic, or persistence.
- Modifying Publisher or Subserver implementations.
- Altering terminal visual styles, colors, or header labels.

## Acceptance Criteria

- [ ] `Store.Snapshots()` full-population cloning is eliminated from the normal 10 Hz TUI polling loop.
- [ ] Viewmodel materialization is bounded to the visible table viewport rows.
- [ ] Heap allocations per TUI refresh tick are reduced by at least 95% under a 50k+ candidate dataset, verified by benchmark.
- [ ] TUI CPU usage remains below 5% of a core during active probing cycles under 50k+ candidates, verified by benchmark.
- [ ] Header progress counters continue to update smoothly at 10 Hz during active cycles.
- [ ] Filter cycling ('s') correctly filters between All, Gemini, and Generic projections without data loss.
- [ ] Cursor navigation, scrolling, and detail view expansion remain responsive and functional.
- [ ] Cursor position remains correctly clamped when filtered candidate membership changes between index rebuilds.
- [ ] Candidate tier ranking and status badge rendering remain 100% compliant with Ticket 25 semantics.
- [ ] `reconcileCycleStateLocked` avoids full `Store.Snapshots()` cloning.
- [ ] All existing TUI unit and regression tests pass; new tests verify bounded viewport rendering.
- [ ] Zero race conditions detected under `go test -race ./internal/tui/...`.

## Dependencies

- **Ticket 25**: Dual generic and gemini projection observability in TUI.
