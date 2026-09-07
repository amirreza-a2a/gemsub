# 13: fix(tui): current-cycle metrics in header

Type: bugfix
Status: resolved
Blocked by: None (can start immediately)

## Problem

The TUI header currently displays Pass/Fail/Incon counts derived from `Store.Stats()`, while probe progress is cycle-local. This makes the header semantically misleading.

## Required contract

- Pass/Fail/Incon counts shown as "Current cycle" metrics must represent the active cycle only.
- Store aggregate statistics must not be presented as current-cycle counters.
- Cycle-local counters are presentation/runtime state, not persisted Store state.
- Existing Store aggregate statistics must remain available where appropriate.
- Preserve the frozen TUI presentation boundary.
- Do not redesign the Store reliability model.

## Acceptance criteria

- [x] Current-cycle PASS/FAIL/INCON counters are updated from existing lifecycle/progress events.
- [x] Counters reset at CycleStarted.
- [x] Counters reach final values at CycleFinished.
- [x] Lost EventBus events must not permanently corrupt candidate-state projection; use the existing revision mechanism where relevant.
- [x] Existing aggregate Store statistics remain correct.
- [x] Add focused tests.

## Answer

Remediated TUI header current-cycle metrics in `internal/tui/adapter`, `internal/tui/model`, and `internal/tui/viewmodel`:

1. **Root Cause**:
   - `EventBus` has intentionally lossy delivery semantics. When a subscriber queue is saturated during high-concurrency probe bursts, events can be dropped.
   - Store revision tracking (`st.Revision()`) ensures candidate-state projection always converges to the authoritative `Store`, but cycle-local metrics (`cyclePassed`, `cycleFailed`, `cycleInconclusive`, `cycleStatus`) are ephemeral presentation state.
   - If `CycleFinished` or intermediate `ProbeCompleted` events were dropped without a reconciliation mechanism, the TUI could remain stuck in `CycleRunning` or present lagging progress counts even after Store finalized the cycle.

2. **Chosen Convergence Mechanism**:
   - **Cumulative Event Payloads**: `events.ProbeCompleted` and `events.CycleFinished` already carry cumulative `ProgressMetrics` (`Completed`, `Total`, `Passed`, `Failed`, `Inconclusive`) from the atomic counters in `tester.RunPool`. Because the payload is cumulative rather than incremental deltas, any single subsequent event immediately recovers all previously dropped intermediate counts without permanent corruption.
   - **Store Cycle Reconciliation on Lost `CycleFinished`**: When `Store.FinishCycle()` commits, `Store.cycleCount` is incremented and `s.revision` bumps. In `Adapter.reconcileCycleStateLocked()`, if `a.cycleStatus == viewmodel.CycleRunning` and `stats.CycleCount > a.lastCompletedCycleCount`, the Adapter detects that Store has completed the cycle even if `events.CycleFinished` was lost. It reconciles `a.cycleStatus = viewmodel.CycleIdle`, sets `a.progressCurrent = a.progressTotal`, reconstructs the cycle's exact pass/fail/incon counts from authoritative `Store` history (`snap.Record.History.Last().CycleID == completedCycleID`), and marks state dirty.
   - **Unconditional Reset on Next Cycle**: When `events.CycleStarted` arrives, all cycle counters are unconditionally reset to 0, ensuring no state leaks across cycles.
   - **Authoritative Store Aggregate Stats Preserved**: Store aggregate statistics in `Store.Stats()` remain unchanged and directly accessible via `Adapter.StoreStats()`.

3. **Presentation & Layout**:
   - Updated `internal/tui/model.go` header rendering to explicitly label cycle counts: `"Servable: %s / %d  |  Cycle: Pass: %d  Fail: %d  Incon: %d  |  %s"`. This makes the distinction between Store servable totals and active cycle counts immediately obvious to the user while strictly fitting within 80-column terminal dimensions.
   - Exported `tui.TickMsg` to allow direct, testable execution of the actual background tick/refresh update path in tests without secondary model instantiation.

4. **Verification**:
   - `TestAdapter_CurrentCycleMetricsLifecycle`: verifies full lifecycle from cold start, incremental probe updates, completion, and next-cycle reset.
   - `TestAdapter_LostIntermediateProbeCompletedEvents`: verifies that dropping intermediate probe events does not corrupt state and that cumulative metrics catch up immediately on subsequent events.
   - `TestAdapter_LostCycleFinishedRecoverableViaStore`: verifies that dropping `CycleFinished` and the final probe events is cleanly reconciled via Store state (`stats.CycleCount` + `History.Last()`).
   - `TestAdapter_NextCycleStartedUnconditionalReset`: verifies unconditional zeroing of all counters on `CycleStarted`.
   - `TestAdapter_CancellationAndAbortPaths`: verifies partial metrics on cancellation and zeroed metrics on empty-fetch abort.
   - `TestModel_HeaderRendersCurrentCycleMetrics`: tests Bubble Tea `Model.Update(TickMsg)` directly, asserting explicit `"Cycle: Pass: ..."` header rendering.
   - Ran `gofmt`, `make test`, `go test -tags with_utls -race -count=1 ./...`, `make build`, and `git diff --check`.
