# 12: fix(store): derive HasPassed and LastPassedLatency from authoritative history

Type: bugfix
Status: resolved
Blocked by: 11

## Question

How do we prevent stale `HasPassed` and `LastPassedLatency` values from persisting on `CandidateRecord` after successful observations have been evicted from the active `BoundedHistory` window?

## Context Note

This defect predates this ticket (present since commit `5563a80`, not introduced by `30815e4`) — it was newly discovered during review of `30815e4`, not caused by it.

## What to build

1. **P1 — Keep `HasPassed` and `LastPassedLatency` Derived From Authoritative History**:
   - `BoundedHistory` is the sole authoritative source of truth for whether a candidate has a successful observation within its active sliding history window.
   - In `PutWithTransition()`, derive `HasPassed` and `LastPassedLatency` dynamically from `rec.History.LastPassedLatency()` on every mutation.
   - If all `StatusPassed` observations have been evicted from active history (e.g. `Pass -> Pass -> Fail -> Fail` with capacity 2):
     - `rec.HasPassed = false`
     - `rec.LastPassedLatency = 0`
   - In `PassingRanked()`, unproven candidates (including those whose passes have been evicted) sort as unproven (`math.MaxInt64`), strictly ranking behind proven candidates.
   - In `Load()`, recompute `HasPassed` and `LastPassedLatency` deterministically from `rec.History.LastPassedLatency()` regardless of persisted values in the snapshot.
   - Persisted `HasPassed` and `LastPassedLatency` fields are treated strictly as derived/cache data, never authoritative.
   - Preserve frozen ranking precedence:
     1. `HasPassed`
     2. `Score`
     3. `LastPassedLatency`
     4. `ActiveLink`

## Acceptance criteria

- [x] `HasPassed` and `LastPassedLatency` are dynamically derived from `BoundedHistory.LastPassedLatency()` on every probe mutation in `PutWithTransition`.
- [x] Evicting all successful passes from bounded history resets `HasPassed` to `false` and `LastPassedLatency` to `0`.
- [x] `PassingRanked()` treats candidates whose passes were evicted as unproven, ranking them behind proven candidates.
- [x] `Load()` unconditionally recomputes `HasPassed` and `LastPassedLatency` from history.
- [x] All tests pass under race detector.

## Answer

Implemented in `internal/store`:
- Updated `PutWithTransition` to derive `rec.LastPassedLatency, rec.HasPassed` via `rec.History.LastPassedLatency()` on every probe.
- Updated `Load()` to recompute `rec.LastPassedLatency, rec.HasPassed` from `rec.History.LastPassedLatency()`.
- Added regression test `TestScoring_HasPassedAndLatencyDerivedFromAuthoritativeHistory` covering full eviction sequence (`Pass -> Pass -> Fail -> Fail`), ranking precedence verification, and `Save -> Load` derivation verification.
