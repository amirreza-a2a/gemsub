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

### Amendment (Phase 4 - Bounded Hysteresis with Probation, Complete Deduplication, and Fast Persistence)

The live-only model for Tier 2 services is refined to support single-cycle flap dampening, cold-start probation, transport observation tracking, and crash recovery without violating the O(1) per-candidate storage invariant:

1. **Store-Owned Bit-Packed Flap Hysteresis with Probation**:
   - `store.TargetResult` incorporates an inline `Hysteresis uint8` field (`json:"-"`, 0 heap allocation overhead):
     - **Probation Phase (Bit 7 == 0)**: While unestablished, Bits 0–6 count consecutive passing cycles. The candidate becomes established when this pass streak reaches `cfg.MinObservationsForServing` (default 1). Any failure or inconclusive outcome during probation resets the streak to 0.
     - **Established Phase (Bit 7 == 1)**: Once established, Bit 7 remains set, and Bits 0–6 switch to counting consecutive failure cycles (0–127, saturating at 127).
   - Servability rule for Tier 2 services in `servabilityGateLockedFor`:
     `AbsentCycles == 0 && established && (res.Status == StatusPassed || failures <= tolerance)` with `tolerance = 1`.
   - Transitions execute strictly in `Store.PutWithTransition` under the store lock using the candidate's prior entry.
   - Definitive policy categories (`ErrRegionBlocked`, `ErrTargetDenied`) reset `Hysteresis = 0` (unestablished, streak 0), evicting the candidate immediately in Cycle 1. Inconclusive outcomes count as failures subject to the 1-cycle tolerance.
   - Stage 1 transport failures write failure records into each service's `TargetResult`, subjecting transport blips to the same 1-cycle tolerance without checking a separate `TransportOK` gate.
   - **Local Congestion ("Not Measured") Contract**: When a service probe is omitted from `Result.Services` (e.g. aborted due to local semaphore congestion or attempt cancellation), `Store.PutWithTransition` treats it as a strict no-op: the candidate's existing entry and hysteresis counter remain untouched.

2. **TransportObservations for Transport-Only Servability**:
   - `CandidateRecord.TransportObservations uint8` (saturating at 255) is added to record cumulative transport observations. Incremented on every ingestion carrying transport evidence (both `PutWithTransition` and `PutTransportOnly`), initialized from `History.Count` when loading legacy snapshots, and persisted additively.
   - `networkHealthyStateLocked` Gate 3 evaluates `rec.TransportObservations >= cfg.MinObservationsForServing` instead of `rec.History.Count`, allowing transport-only (`test.skip_stage2`) candidates to enter `Store.NetworkPassing()` while `Store.Passing()` remains strictly empty.

3. **Tier Routing in Servability and Projections**:
   - `servabilityGateLockedFor`, `PassingFor`, and `PassingForRanked` route strictly by tier:
     - Built-in Tier 1 services (`gemini`, `claude`) preserve existing scoring, history gates, and ranking byte-for-byte.
     - Registered Tier 2 services evaluate Gate 1 (source presence) plus the Tier 2 probation/hysteresis gate.
     - Unregistered service names return empty projections and fail servability immediately.
     - `PassingForRanked` for Tier 2 services returns deterministic alphabetical links matching `PassingFor` (no historical scoring).

4. **Service Readiness Latch (`ServiceReady`)**:
   - `Store.ServiceReady(name) bool` reports true if the service was loaded from a snapshot containing valid data for that service OR if at least one full cycle (`FinishCycle`) completed during the current process run. Tier 1 readiness behavior is preserved.

5. **Complete In-Memory Deduplication by Deletion**:
   - `rec.Latest.Services` is permanently removed from stored candidate records across all store methods (`PutWithTransition`, `Load`, V1 migration, and `Clone`), routing all service lookups through `rec.Services`. `Result.Services` remains strictly transient transport data between the tester and store. This eliminates map aliasing in `Clone()` and reclaims **41.18 MB (45.46%)** of heap allocations across 56,799 candidates.

6. **Fast Snapshot Persistence & Local Pruning Grace Tracking**:
   - `writeSnapshotJSON` serializes dynamic Tier 2 entries under an optional `"tier2"` record key (`"tier2":{"svc":{"s":"passed","h":128}}`) using `appendJSONString`.
   - `fastDecodeSnapshot` parses `"tier2"` directly with `ok == true` (avoiding `json.Unmarshal` fallback).
   - Decommissioned services are pruned via a decoupled `SetActiveServices([]string)` Store API without importing `internal/config`.
   - Local absence counters (`serviceAbsentCycles map[string]int`) are tracked strictly in local store state (`Snapshot`), avoiding intermediate churn commits in the published Git repository.
