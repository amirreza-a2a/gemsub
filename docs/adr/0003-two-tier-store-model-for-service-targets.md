# Two-tier Store model for service targets

The Store will distinguish between **Tier 1** and **Tier 2** service
targets, providing different levels of state management to each.

**Tier 1** (Gemini, Claude): full `BoundedHistory` per candidate,
reliability scoring, ranked projections, and durable persistence.

**Tier 2** (user-defined services): current-cycle pass/fail status
only, without per-candidate history, scoring, or ranked projections.

## Why the split exists

`BoundedHistory` is a fixed-capacity circular buffer
(`internal/store/history.go`) that provides strict O(1) memory per
candidate. Each `ProbeSample` in the buffer contains the full probe
outcome including per-service `ClaudeStatus`, `ClaudeCategory`,
`ClaudeLatency`, transport evidence, and jitter. For N candidates,
memory usage is O(N × capacity × sizeof(ProbeSample)).

When issue #35 generalized the Store to support per-service
projections, benchmarks measuring the addition of the `Services
map[string]TargetResult` field to `CandidateRecord` showed:

- **3.9× increase in memory consumption** per record (from the map
  header, bucket array, and per-entry overhead).
- **21× increase in allocations** per record snapshot operation (from
  map initialization and deep-copy in `CandidateRecord.Clone()`).

These numbers are for a *single* additional map field. If every
user-defined service received its own `BoundedHistory` entry (a
second circular buffer per service per candidate), the memory
multiplier would be even larger. For an operator running 20,000+
candidates — a normal operating scale — giving every user-added
service full history tracking would break the O(1)-per-candidate
memory invariant that `BoundedHistory` was designed to enforce.

The two-tier model preserves the memory invariant:

- Tier 1 services are baked into the `ProbeSample` struct fields
  (e.g. `ClaudeStatus`, `ClaudeLatency`) — they add a constant
  per-sample cost, independent of the number of services.
- Tier 2 services use the `Services map[string]TargetResult` on
  `CandidateRecord` for current-cycle status only. No history buffer,
  no scoring, no persistence overhead.

## Considered options

1. **Flat model: every service gets full history.** Rejected because
   of the benchmark evidence above. The map-per-record cost is already
   measurable at 3.9× memory; adding a per-service `BoundedHistory`
   would be worse.

2. **Two-tier model (chosen).** Tier 1 services get full treatment
   because they are the project's core targets and the data is needed
   for scoring-based ranked subscriptions. Tier 2 services get
   live-only treatment, which is sufficient for binary "is this
   service reachable through this proxy right now?" questions.

3. **Configurable tier promotion.** Allow users to promote a Tier 2
   service to Tier 1 via config. Deferred: this is possible as a
   future extension but adds implementation complexity (dynamic
   `ProbeSample` field allocation or per-service history slices) that
   isn't justified until a user actually needs historical scoring for
   a custom service.

## Consequences

- User-defined Tier 2 services cannot provide ranked subscription
  output (there is no scoring data to rank by). Their projections are
  unranked pass/fail lists.
- Promoting a service from Tier 2 to Tier 1 requires a code change
  (adding fields to `ProbeSample` and `CandidateRecord`), not just
  a config change. This is intentional: Tier 1 status carries a
  permanent per-candidate memory cost.
- Anyone proposing to change this split should first reproduce the
  benchmarks from issue #35's analysis (commit `690509b`) and verify
  that the memory cost is acceptable at the target candidate scale.

### Amendment (Phase 4 - Bounded Hysteresis, Complete Deduplication, and Fast Persistence)

The live-only model for Tier 2 services is refined to support single-cycle flap dampening, memory efficiency, and crash recovery without violating the O(1) per-candidate storage invariant:

1. **Store-Owned Bit-Packed Flap Hysteresis**:
   - `store.TargetResult` incorporates an inline `Hysteresis uint8` field (0 heap allocation overhead):
     - **Bit 7 (MSB, `0x80`)**: `hadPassed` flag (records whether the candidate has passed at least once).
     - **Bits 0–6 (`0x7F`)**: `consecutiveFailures` counter (0–127).
   - Servability rule: `AbsentCycles == 0 && (res.Status == StatusPassed || (res.HadPassed() && res.Failures() <= tolerance))` with `tolerance = 1`.
   - All hysteresis state transitions execute in `Store.PutWithTransition`. Definitive policy categories (`ErrRegionBlocked`, `ErrTargetDenied`) bypass hysteresis and evict the candidate immediately (`failures = 127`). Inconclusive outcomes count as failures subject to the 1-cycle tolerance.
   - Stage 1 transport failures write failure records into each service's `TargetResult`, subjecting transport blips to the same 1-cycle tolerance without checking a separate `TransportOK` gate.
   - **Local Congestion ("Not Measured") Contract**: When a service probe is omitted from `Result.Services` (e.g. aborted due to local semaphore congestion), `Store.PutWithTransition` treats it as a strict no-op: the candidate's existing entry and hysteresis counter remain untouched.

2. **Complete In-Memory Deduplication by Deletion**:
   - `rec.Latest.Services` is removed from stored records across all store methods (`PutWithTransition`, `Load`, and `Clone`), routing all service lookups through `rec.Services`. `Result.Services` remains strictly transient transport data between the tester and store. This eliminates map aliasing in `Clone()` and reclaims **41.18 MB (45.46%)** of heap allocations across 56,799 candidates.

3. **Fast Snapshot Persistence & Pruning**:
   - `writeSnapshotJSON` serializes dynamic Tier 2 entries under an optional `"tier2"` record key (`"tier2":{"svc":{"s":"passed","h":128}}`).
   - `fastDecodeSnapshot` parses `"tier2"` directly. Implementation is gated on `Load Duration <= 1.25x Baseline` in the same test run, avoiding `json.Unmarshal` fallback.
   - Decommissioned services are pruned via a decoupled `activeServices []string` Store API without importing `internal/config`.
   - Pruning grace counters (`servicePruneAbsence map[string]int`) are tracked strictly in local state (e.g. Store snapshot), preventing intermediate churn commits in the published repository.
