# 11: fix(store): remediate history repair, capacity clamping, and cycle transactions

Type: bugfix
Status: resolved
Blocked by: 10

## Question

How do we eliminate dangerous automatic history mutation on normal Load, protect against unbounded buffer slice allocations from corrupted snapshots, and make cycle absence transitions fully transactional across StartCycle and FinishCycle?

## What to build

Implement focused remediation follow-up to commit `5563a80`:

1. **P0 — Remove Automatic `repairBuggyV2History` Heuristic from `Load()`**:
   - Re-verified live deployment: No real deployment has ever run `6d8f4ad` against a live `state.json` (live `./gemsub_state.json` remains legacy unversioned V1).
   - Removed `repairBuggyV2History` from the normal `Load()` path entirely.
   - Legitimate histories containing a real `Passed(200)`-then-`Inconclusive(+60s)` probe sequence must never be modified or pruned on normal startup.
   - Any future repair for corrupted snapshots will require explicit, dedicated tooling rather than implicit always-on load mutations.
   - Added regression test `TestScoring_LegitimateHistoryNotMutatedOnLoad`.

2. **P1 — Clamp Persisted `BoundedHistory.Capacity` to Configured Maximum**:
   - Persisted `Capacity` in snapshots must never be trusted without validation.
   - On load, clamp `Capacity` to `[1, maxCapacity]` where `maxCapacity` is the store's configured `s.cfg.HistoryCapacity`.
   - Design rationale: All candidate records within a store instance share the engine's configured sliding window depth ($N$). Clamping prevents corrupted or malicious state files from requesting massive allocations like `make([]ProbeSample, 1_000_000_000)`.
   - When buffer capacity is adjusted downward, preserve the most recent `h.Capacity` samples in FIFO order.
   - Added regression tests `TestScoring_AbsurdCapacitySnapshotClampedOnLoad` and unit tests in `history_test.go`.

3. **P1 — Full Cycle Absence Transactionality Across StartCycle / FinishCycle**:
   - Defer all `AbsentCycles` resets (both `StartCycle` presence-based resets and `PutWithTransition` probe-driven resets) until `FinishCycle()` commits.
   - Track `pendingPresent` state during active cycles alongside `pendingAbsent`.
   - Gate 1 checks `pendingPresent`: candidates that reappear are servable during the active cycle.
   - If a cycle is aborted before `FinishCycle()` (crash, cancellation, or new `StartCycle`), no `AbsentCycles` value is modified from its pre-cycle value.
   - `FinishCycle()` atomically increments `AbsentCycles` for `pendingAbsent` candidates (evicting beyond `MaxAbsentCycles`) and commits `AbsentCycles = 0` for `pendingPresent` candidates.
   - Added tests for StartCycle abort, PutWithTransition abort, and normal FinishCycle commit.

4. **P2 — Explicit Documentation of HasPassed-before-Score Ranking Precedence**:
   - Documented the ranking contract in `PassingRanked()`:
     1. Proven candidates (`HasPassed == true`) strictly outrank unproven candidates (`HasPassed == false`).
     2. Reliability score descending.
     3. Last passed latency ascending (unproven candidates sort worst at `math.MaxInt64`).
     4. ActiveLink ascending for deterministic tie-breaking.
   - Rationale: Unproven candidates must never outrank candidates with confirmed successful observations, even if an unproven candidate has a high initial default score or low timeout duration.

5. **P2 — IPv6 Zone Identifier Test Coverage**:
   - Added `TestCanonicalizeLink_IPv6ZoneIdentifiers` confirming RFC 6874 percent-encoded zone IDs (`%25eth0`) are preserved and parsed into valid outbound configs, while unencoded zone IDs are handled safely without panic and rejected by the parser.

## Acceptance criteria

- [x] `repairBuggyV2History` removed from `Load()`; legitimate histories containing Passed-then-Inconclusive(+60s) sequences remain untouched.
- [x] Persisted `Capacity` clamped to `[1, HistoryCapacity]` preventing unbounded memory allocations on malformed snapshots.
- [x] Shrinking history capacity preserves the most recent samples in chronological FIFO order.
- [x] Reappearing candidates during active cycles do not mutate `rec.AbsentCycles` until `FinishCycle()` commits.
- [x] Aborted cycles preserve pre-cycle `AbsentCycles` values without leakage.
- [x] Ranking precedence policy (HasPassed before Score) documented in code and tracker map.
- [x] IPv6 zone identifier handling verified and tested.
- [x] All tests pass with race detector enabled.

## Answer

Implemented in `internal/store`:
- Removed `repairBuggyV2History` from `store.go:Load()`.
- Updated `BoundedHistory.NormalizeAndValidate` to clamp `Capacity` between 1 and `maxCapacity` (`s.cfg.HistoryCapacity`) and preserve newest samples on shrink.
- Added `pendingPresent` to `Store` to make `StartCycle` and `PutWithTransition` absence resets transactional until `FinishCycle()`.
- Documented ranking policy in `PassingRanked` docstring and map.
- Added extensive regression test coverage across `history_test.go`, `scoring_test.go`, and `canonical_test.go`.
