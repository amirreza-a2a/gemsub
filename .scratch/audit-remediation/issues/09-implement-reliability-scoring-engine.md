# 09: feat(store): implement reliability scoring engine with bounded history

Type: task
Status: resolved
Blocked by: 08

## Question

How do we replace binary single-cycle pass/fail serving and fragile integer counters with a bounded-history, recency-weighted reliability scoring engine that enforces strict operational policy gates?

## What to build

Implement the frozen Reliability Scoring Engine specification:
- `CanonicalizeLink` candidate identity separated from `ActiveLink` serving representation.
- `ProbeSample` observation model where `Result.Passed` strictly represents latest probe outcome (`Status == StatusPassed`).
- `BoundedHistory` true circular buffer with fixed capacity $N$, $O(1)$ memory per candidate, and FIFO eviction.
- Recency-weighted decay scoring formula $S = \sum (\lambda^{k-i} \cdot W(O_i)) / \sum \lambda^{k-i}$.
- Four operational policy gates for servability: Source Presence, Target Policy Override, Cold Start, and Score Threshold.
- Absence tracking committing increments only in `FinishCycle` with $M=2$ grace period before eviction.
- Persistence V2 schema with deterministic score recomputation on `Load()` and backward-compatible V1 migration.
- Atomic state file saving with `f.Sync()` and rename.
- `Store.Passing()` and `Store.PassingRanked()`.

## Acceptance criteria

- [x] Authoritative Store key is `CanonicalLink` with deterministic scheme/host lowercasing, query sorting, duplicate value sorting, and fragment preservation.
- [x] `BoundedHistory` circular buffer provides strict $O(1)$ memory and FIFO eviction.
- [x] Reliability scoring implements exponential recency decay with verified numerical examples (7 passes + 1, 2, 3 timeouts: 0.841, 0.722, 0.632).
- [x] `Result.Passed` strictly reflects `Result.Status == StatusPassed`, never conflated with `IsServable`.
- [x] Target Policy Override immediately disqualifies `ErrRegionBlocked` and `ErrTargetDenied` candidates regardless of historical score.
- [x] Source Presence Gate disqualifies absent candidates; `FinishCycle` commits absence increments with $M=2$ grace period before eviction; cycle aborts never commit increments.
- [x] Persistence V2 recomputes Score on `Load()` from `BoundedHistory`, and backward-compatible V1 migration works cleanly.
- [x] Thread-safe concurrent access verified under race detector.

## Answer

Implemented the Reliability Scoring Engine in `internal/store`:

1. **Candidate Identity (`canonical.go`)**:
   - Implemented `CanonicalizeLink` to provide deterministic canonical keys: lowercases scheme and hostname, sorts query parameters alphabetically and duplicate values deterministically, normalizes percent-encoding, and preserves fragment identity and proxy parser compatibility.
   - Decoupled `CanonicalLink` (store key) from `ActiveLink` (emitted serving URL).

2. **Observation Model & Bounded History (`history.go`)**:
   - Defined `ProbeSample` tracking `CycleID`, `TestedAt`, `Status`, `Category`, `StatusCode`, `Latency`, and `Attempts`.
   - Implemented `BoundedHistory` fixed-capacity circular buffer with strict $O(1)$ memory, FIFO eviction on overflow, and deep-copy `Clone()`.
   - Derived projections: `PreviouslyPassed` (Last-Known-Good conclusive outcome) and `ConsecutiveInconclusive` (trailing inconclusive run length).
   - Fixed observation semantics: `Result.Passed` strictly represents `Result.Status == StatusPassed`.

3. **Recency-Weighted Reliability Scoring (`history.go`, `config.go`)**:
   - Implemented deterministic recency-weighted exponential decay with default $\lambda=0.75, N=10, T_{servable}=0.65$.
   - Verified numerical examples (7 passes followed by 1, 2, 3 timeouts yielding scores ~0.841, ~0.722, ~0.632).

4. **Candidate Record & Operational Policy Servability Gating (`record.go`, `store.go`)**:
   - Defined `CandidateRecord` storing `CanonicalLink`, `ActiveLink`, `Score`, `AbsentCycles`, `Latest`, and `History`.
   - Enforced 4 operational policy gates in `isServableRecordLocked`:
     1. Source Presence Gate: `AbsentCycles == 0` and not pending absent.
     2. Target Policy Override: immediate disqualification for `ErrRegionBlocked` or `ErrTargetDenied`.
     3. Cold Start Gate: requires `MinObservationsForServing` (default 1).
     4. Score Threshold Gate: `Score >= MinServableScore` (default 0.65).

5. **Absence & Cycle Lifecycle (`store.go`)**:
   - `StartCycle` marks missing candidates as `pendingAbsent` (disqualifying from active serving without evicting).
   - `FinishCycle` commits absence increments; evicts only after $M=2$ absent cycles.
   - Cycle aborts/cancellations discard pending absence without committing increments. Skipped probes do not increment absence or add failures.

6. **Persistence & Serving (`store.go`)**:
   - Implemented Version 2 snapshot schema with atomic temp-file write, `f.Sync()`, and rename.
   - `Load()` recomputes `Score` deterministically from authoritative `BoundedHistory`.
   - Implemented backward-compatible legacy V1 snapshot migration.
   - Implemented `Passing()` and `PassingRanked()` returning active links.
