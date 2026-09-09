# Ticket 27 Correctness & Performance Review

**Date:** 2026-09-09  
**Ticket:** 27 — `perf(tui): virtualize candidate table rendering and eliminate full-store snapshot thrashing`  
**GitHub Issue:** amirreza-a2a/gemsub#5  
**Review Status:** CHANGES REQUIRED  

---

## A. Verdict

**CHANGES REQUIRED**

The core performance objectives of Ticket 27—eliminating `Store.Snapshots()` full-population cloning from the polling loop, virtualizing row materialization to the visible terminal viewport via `CandidateRowsWindow`, and reducing tick memory allocations by >99%—have been successfully designed and implemented.

However, a rigorous review of the invalidation state machine and resource lifecycle reveals:
1. **A P0 Correctness Bug (Permanent Presentation Staleness):** The interaction between `Adapter.CheckAndResetDirty()` and the 1-second throttle in `rebuildIndexLocked()` causes pending Store state updates to be permanently dropped whenever a Store mutation occurs within the 1-second throttle window and is followed by an idle testing interval.
2. **A P1 Resource Lifecycle Bug (Opaque ID Map Unbounded Growth):** `Adapter.linkToID` and `Adapter.idToLink` monotonically accumulate entries across candidate churn cycles because candidate evictions in `Store.FinishCycle()` are never pruned from the presentation maps.
3. **A P2 Code Quality Hazard:** The legacy `Adapter.CandidateRows()` method remains exported, materializing the entire candidate population into ViewModels, which poses an operational OOM hazard if called in future presentation paths.

---

## B. P0 Findings

### P0-1: Permanent Presentation Staleness via Decoupled Revision Consumption and Throttling
* **Location:** [`internal/tui/adapter/adapter.go:L226-242`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go#L226-L242) (`CheckAndResetDirty`) and [`internal/tui/adapter/adapter.go:L301-308`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go#L301-L308) (`rebuildIndexLocked`)
* **Exact Issue:**
  `CheckAndResetDirty()` unconditionally consumes the Store revision change:
  ```go
  currentRev := a.st.Revision()
  if currentRev != a.lastRevision {
      a.lastRevision = currentRev
      dirty = true
  }
  ```
  When `PollSnapshot()` executes in response to `dirty == true`, it invokes `rebuildIndexLocked(false)`. However, `rebuildIndexLocked(false)` contains a 1-second throttle:
  ```go
  if !force && a.cachedEntries != nil && len(a.cachedEntries) == stats.Total {
      if currentRev == a.lastIndexRev || now.Sub(a.lastIndexTime) < time.Second {
          return
      }
  }
  ```
  If `now.Sub(a.lastIndexTime) < time.Second`, `rebuildIndexLocked` returns early **without** updating `a.cachedEntries` and **without** updating `a.lastIndexRev`.
  On the subsequent 100 ms tick, `CheckAndResetDirty()` runs again. Because `a.lastRevision` was already set to `currentRev` on the previous tick, `currentRev != a.lastRevision` evaluates to `false`. If no further probe results arrive (e.g. between cycle probes or when testing concludes), `CheckAndResetDirty()` returns `false` on all future ticks.
* **Concrete Failure Scenario:**
  1. At $t = 0.0\text{s}$, cycle starts; Store revision is 10. `a.cachedEntries` is built for rev 10 (`lastIndexRev = 10, lastIndexTime = 0.0\text{s}`).
  2. At $t = 0.2\text{s}$, a candidate probe finishes. Store revision increments to 11.
  3. At $t = 0.3\text{s}$, the 10 Hz `TickMsg` triggers `PollSnapshot()`. `CheckAndResetDirty()` sees $11 \ne 10$, sets `a.lastRevision = 11`, and returns `dirty = true`.
  4. `PollSnapshot()` calls `rebuildIndexLocked(false)`. Since $0.3\text{s} - 0.0\text{s} = 0.3\text{s} < 1.0\text{s}$, it returns early. `a.cachedEntries` remains at revision 10. `a.lastIndexRev` remains 10.
  5. Probing pauses (e.g. rate limit, cycle pause, or last probe of cycle).
  6. At $t = 0.4\text{s}, 0.5\text{s}, \dots, 1.5\text{s}, \dots$, `CheckAndResetDirty()` checks `currentRev (11) != a.lastRevision (11)`, which is `false`. It returns `false`.
  7. Even though 1 second has elapsed and the throttle has expired, `PollSnapshot` is never executed again. The TUI candidate table is permanently stuck displaying stale revision 10 data until an unrelated future event mutates Store revision.
* **Severity:** P0 (Functional correctness violation; violates eventual consistency guarantee).
* **Remediation Recommendation:**
  In `CheckAndResetDirty()`, evaluate whether the candidate index itself is pending refresh:
  ```go
  // In CheckAndResetDirty:
  a.indexMu.Lock()
  indexStale := a.st != nil && a.cachedEntries != nil &&
      (a.lastIndexRev != a.st.Revision() || len(a.cachedEntries) != a.st.Stats().Total) &&
      time.Since(a.lastIndexTime) >= a.indexThrottleInterval
  a.indexMu.Unlock()
  if indexStale {
      dirty = true
  }
  ```
  Alternatively, separate `lastReportedRevision` from `lastIndexedRevision`, ensuring `CheckAndResetDirty()` returns `true` whenever `a.lastIndexRev != currentRev && time.Since(a.lastIndexTime) >= time.Second`.

---

## C. P1 Findings

### P1-1: Unbounded Presentation ID Map Growth across Candidate Churn
* **Location:** [`internal/tui/adapter/adapter.go:L397-414`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go#L397-L414) (`materializeRowViewModelLocked`)
* **Exact Issue:**
  In the pre-Ticket-27 implementation, `linkToID` and `idToLink` maps were re-allocated on every full `CandidateRows()` call, naturally pruning candidates that were no longer present.
  In Ticket 27, `linkToID` and `idToLink` were made persistent fields on `Adapter`. However, no pruning logic was added. When candidates are evicted from `Store` during `FinishCycle()` (`AbsentCycles > MaxAbsentCycles`), their opaque IDs (`cand-N`) and link strings remain in `a.linkToID` and `a.idToLink` indefinitely.
* **Concrete Failure Scenario:**
  In dynamic environments where subscription aggregators rotate 10,000 new nodes every cycle and evict old ones, `a.linkToID` and `a.idToLink` grow by 10,000 string entries per cycle. After 100 cycles, the maps retain 1,000,000 orphaned entries (~100 MB leaked memory), defeating the memory-bounding purpose of the ticket.
* **Severity:** P1 (Memory leak in long-running daemon process).
* **Remediation Recommendation:**
  During `rebuildIndexLocked()`, collect all active canonical links from `entries` into a set. Then iterate `a.linkToID` under `a.mu.Lock()` and delete any entry whose canonical link is no longer present in the active population.

---

## D. P2 Findings

### P2-1: Exported Full-Population Materialization Method `CandidateRows`
* **Location:** [`internal/tui/adapter/adapter.go:L512-527`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go#L512-L527) (`CandidateRows`)
* **Exact Issue:**
  `CandidateRows(filter viewmodel.FilterMode)` still materializes the entire candidate population (e.g. 50,000 `CandidateRowViewModel` instances) in a single slice. It is not called anywhere in production (`internal/tui/model.go` calls only `CandidateRowsWindow`), but remains an exported public method on `Adapter` primarily used by legacy unit tests in `adapter_test.go`.
* **Concrete Failure Scenario:**
  Future presentation code or an external caller could invoke `CandidateRows` on a 50k candidate store, causing a sudden 80 MB allocation burst and CPU stall.
* **Severity:** P2 (Maintenance / regression hazard).
* **Remediation Recommendation:**
  Either unexport the method (e.g. `candidateRowsForTest`) or document clearly that it is a testing utility, migrating unit tests to verify ordering through `CandidateRowsWindow` or the lightweight index.

---

## E. Explicit Investigation Checkpoints

### 1. CandidateIndexEntry Footprint
* **Primary Source:** [`internal/store/store.go:L985-1000`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L985-L1000)
* **Actual Structural Footprint:**
  On 64-bit architecture (`linux/amd64`):
  - `CanonicalLink` (`string`): 16 bytes
  - `ActiveLink` (`string`): 16 bytes
  - `Servable` (`bool`): 1 byte
  - `NetworkHealthy` (`bool`): 1 byte
  - `HasPassed` (`bool`): 1 byte
  - Alignment padding: 5 bytes
  - `Score` (`float64`): 8 bytes
  - `LastPassedLatency` (`time.Duration` / `int64`): 8 bytes
  - `TransportLatency` (`time.Duration` / `int64`): 8 bytes
  - `TransportEvidenceKnown` (`bool`): 1 byte
  - `TransportOK` (`bool`): 1 byte
  - Alignment padding: 6 bytes
  - `LatestStatus` (`Status` = `string`): 16 bytes
  - `LatestCategory` (`ErrorCategory` = `string`): 16 bytes
  - `HistoryCount` (`int`): 8 bytes
  - `LastCycleID` (`uint64`): 8 bytes
  - **Total Struct Size:** Exactly **120 bytes** (`unsafe.Sizeof(CandidateIndexEntry{}) == 120`).
* **Footprint for 50,000 Entries:**
  $50,000 \times 120\text{ bytes} = 6,000,000\text{ bytes} \approx 5.72\text{ MiB}$ (plus slice header and allocator page rounding $\to 6.00\text{ MB}$).
* **Classification:**
  Documentation drift in the original issue spec. The ticket estimated "~40 bytes" based on scalar counts without accounting for Go 16-byte string headers (`CanonicalLink`, `ActiveLink`, `LatestStatus`, `LatestCategory`). At 120 bytes, it allocates in a single slice without deep-copying samples, achieving a 92.5% reduction vs `rec.Clone()`. This is structurally safe and not a memory defect.

### 2. Lock Ordering / Deadlock Analysis
* **Primary Source:** [`internal/tui/adapter/adapter.go`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go) and [`internal/store/store.go`](file:///home/amirreza-a2a/gemsub/internal/store/store.go)
* **Acquisition Graph:**
  - `CandidateRowsWindow`: acquires `a.indexMu.Lock()`, calls Store methods (acquiring `s.mu.RLock()`), then acquires `a.mu.Lock()`, then releases `a.mu`, then releases `a.indexMu`.
  - `CandidateRows`: acquires `a.indexMu.Lock()`, calls Store methods (`s.mu.RLock()`), then acquires `a.mu.Lock()`.
  - `buildSnapshot`: acquires `a.indexMu.Lock()`, calls Store methods (`s.mu.RLock()`), releases `a.indexMu.Unlock()`. Then calls `CandidateRowsWindow`, then `a.Header()` (acquiring `a.mu.Lock()` independently).
  - `CheckAndResetDirty` / `Header`: acquires `a.mu.Lock()`, calls `s.mu.RLock()` via `Stats()` / `Revision()`, releases `a.mu.Unlock()`.
  - `CandidateDetail` / `CopyCandidateLink`: acquires `a.mu.RLock()`, releases, then calls `s.mu.RLock()`.
* **Lock Hierarchy:**
  $$\text{Adapter.indexMu} \succ \text{Adapter.mu} \succ \text{Store.mu}$$
* **Deadlock Risk:**
  Zero. There is no path where `a.mu` is held and `a.indexMu` is requested. `Store` has zero knowledge of `Adapter` and never calls into it. `Store.mu \to \text{Adapter.mu}` is physically impossible.

### 3. Hidden Full-Store Work
* **Primary Source:** Grep verification across `gemsub` codebase.
* **Findings:**
  - `Store.Snapshots()` callers: 0 in production (`cmd/`, `internal/tui/`). Only called in `internal/store/` unit tests.
  - `Adapter.CandidateRows()` callers: 0 in `internal/tui/model.go`. Only called in `adapter_test.go`.
  - `reconcileCycleStateLocked()` invokes `a.st.CandidateIndex()`, but only when `stats.CycleCount > a.lastCompletedCycleCount` (once per completed cycle). Normal ticks execute 0 calls.

### 4. CandidateIndex Semantics
* **Primary Source:** [`internal/store/store.go:L1004-1043`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L1004-L1043)
* **Integrity:**
  - Exposes only value types; no internal slice or map pointers leaked.
  - No deep-copying of `BoundedHistory`; accesses only scalar `rec.History.Count` and `rec.History.Last()`.
  - Reuses canonical classification logic (`s.servabilityGateLocked`, `s.isNetworkHealthyRecordLocked`).
  - Read-only under `s.mu.RLock()`.

### 5. Virtualization Correctness
* **Primary Source:** [`internal/tui/model.go:L319-420`](file:///home/amirreza-a2a/gemsub/internal/tui/model.go#L319-L420) and [`renderCandidateTable:L545-600`](file:///home/amirreza-a2a/gemsub/internal/tui/model.go#L545-L600)
* **Edge Case Verification:**
  - **First / Middle / Last page:** Offset clamping in `adjustScroll()` and `CandidateRowsWindow()` handles boundaries cleanly.
  - **Empty list:** Returns "No candidates in current view." without panicking; arrow keys no-op.
  - **Dynamic list shrinkage / expansion:** `applySnapshot()` clamps `m.cursorIndex < m.totalRows` and `tableOffset < m.totalRows`.
  - **Selection state stability:** `m.detailCandidateID` tracks candidate identity across background re-sorts.
  - **View purity:** `renderCandidateTable()` contains 0 mutations and 0 lock acquisitions.

### 6. Index Invalidation Correctness
* **Primary Source:** [`internal/tui/adapter/adapter.go:L224-242`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go#L224-L242) and [`L301-308`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go#L301-L308)
* **Status:** **FAILS due to P0-1.** While dynamic population size changes (`len(cachedEntries) != stats.Total`) correctly bypass the throttle, score/latency/servability changes on existing candidates can be permanently dropped if they occur within 1 second of the last index rebuild.

### 7. Performance Claims
* **Benchmark Evidence:**
  - Viewport materialization: **64.6 KB / window** (vs ~80.17 MB legacy) $\to$ **99.92% reduction**.
  - Warm tick polling: **128.5 KB / tick** (vs ~80.17 MB legacy) $\to$ **99.84% reduction**.
  - CandidateIndex creation: **6.00 MB / 50k** (vs ~80.17 MB legacy) in **1 single allocation**.
* **CPU Consumption:**
  - Viewport windowing takes 16.6 ms on 50k candidates (dominated by 50k index sort).
  - At 1 Hz throttling, candidate sorting consumes $\approx 1.6\%$ of one CPU core, well below the 5% budget.

### 8. CandidateRows Legacy Method
* **Status:** Still public; not called by `Model`. P2 finding logged.

### 9. Store Seam Quality
* **Fields Evaluated:**
  - `CanonicalLink`, `ActiveLink`: necessary for identity and display.
  - `Servable`, `NetworkHealthy`, `HasPassed`, `Score`, `LastPassedLatency`, `TransportLatency`: strictly required for Ticket 25 3-tier ranking.
  - `TransportEvidenceKnown`, `TransportOK`, `LatestStatus`, `LatestCategory`: required for SERV and STATUS badge rendering (`[✓✓]`, `[G ]`, `BLOCKED`, `DENIED`, `FAIL`).
  - `HistoryCount`: required to format score as `"---"` for unproven candidates.
  - `LastCycleID`: required for cycle reconciliation in `reconcileCycleStateLocked`.
* **Conclusion:** Seam is appropriately minimal; no superfluous fields exist.

### 10. Functional Regression
* **Status:** 100% compliant with Ticket 25 tier ordering and presentation rules.

### 11. Memory Lifetime
* **Status:** **FAILS due to P1-1.** Orphaned presentation IDs leak across candidate eviction cycles.

### 12. Race Safety
* **Status:** Verified with `go test -race ./internal/tui/...` and `go test -race ./internal/store/...`. Clean execution with 0 data races.

### 13. API/Testing Hygiene
* **Status:** Clean. `TotalRows` properly exposed on `SnapshotViewModel`. Unused `FilteredCount` was removed.

---

## F. Explicit Answers to Key Questions

1. **Is there any deadlock risk?**
   **No.** Lock order is strictly unidirectional: `Adapter.indexMu` $\succ$ `Adapter.mu` $\succ$ `Store.mu`.
2. **Is there any correctness regression?**
   **Yes (P0).** Presentation staleness bug in `CheckAndResetDirty` / `rebuildIndexLocked`.
3. **Is the P0 performance objective actually achieved?**
   **Yes.** Memory allocations per tick are reduced by 99.84% (from ~80 MB to 128 KB), and full-store cloning via `Store.Snapshots()` is completely eliminated from the TUI polling loop.
4. **Is CandidateIndex API appropriately minimal?**
   **Yes.** All 14 fields directly support tier ranking, badge rendering, or cycle reconciliation without leaking presentation constructs.
5. **Is CandidateRows() retaining a meaningful regression risk?**
   **Minor (P2).** It is unused in production, but leaving it public presents an operational trap.
6. **Is the implementation safe to commit?**
   **No.** P0-1 (permanent presentation staleness) and P1-1 (opaque ID memory leak) must be resolved before commit.

---

## G. Acceptance Matrix for Ticket 27

| Acceptance Criterion | Result | Evidence / Citation |
| :--- | :---: | :--- |
| `Store.Snapshots()` cloning eliminated from 10 Hz polling loop | **PASS** | `adapter.go:L735-763`, grep confirms 0 calls |
| Viewmodel materialization bounded to viewport rows | **PASS** | `model.go:L362-383`, `CandidateRowsWindow` |
| $\ge 95\%$ heap allocation reduction under 50k dataset | **PASS** | Benchmark: 128.5 KB vs 80.17 MB (99.84% reduction) |
| TUI CPU usage $< 5\%$ of a core during active cycles | **PASS** | 1 Hz sort takes 16.6 ms/sec = 1.66% CPU |
| Header progress counters update smoothly at 10 Hz | **PASS** | `adapter.go:L251-282`, reads authoritative `Store.Stats()` |
| Filter cycling ('s') filters without data loss | **PASS** | Verified in `model_test.go:L442-475` |
| Cursor navigation, scrolling, and detail modal responsive | **PASS** | Verified in `model_test.go:L478-528` |
| Cursor clamped on dynamic membership changes | **PASS** | Verified in `model_test.go:L530-567` |
| Candidate tier ranking / status badges 100% Ticket 25 compliant | **PASS** | Verified in `adapter_test.go:L1276-1333` |
| `reconcileCycleStateLocked` avoids `Store.Snapshots()` | **PASS** | `adapter.go:L196`, uses `CandidateIndex()` |
| All unit / regression tests pass | **PASS** | `go test ./...` passes cleanly |
| Zero data races under `go test -race` | **PASS** | `go test -race ./internal/tui/...` passes |
| Eventual presentation consistency guaranteed | **FAIL** | **P0-1**: stale state updates dropped under throttling |
| Bounded memory lifetime under candidate churn | **FAIL** | **P1-1**: orphaned IDs leak in `idToLink`/`linkToID` |
