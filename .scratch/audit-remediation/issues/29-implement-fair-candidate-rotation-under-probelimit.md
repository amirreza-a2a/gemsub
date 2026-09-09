# 29: feat(scheduler): implement fair candidate rotation under ProbeLimit

Type: feature / scheduling fairness
Priority: P1
Area: Scheduler / Scheduling Fairness
Status: ready-for-agent
Blocked by: None (can start immediately)
Source Debt: TD-002 (Scheduler candidate rotation under ProbeLimit)

## Problem

In real-world deployments with public subscription aggregators, the active candidate population reaches approximately 56,000 nodes. Probing all 56,000 candidates unrestricted across 40 parallel workers with 4–10s timeouts takes approximately 3.5 to 4 hours per cycle. This prevents the system from publishing fresh, timely subscription updates.

Configuring `SchedulerConfig.ProbeLimit` (e.g. 1,000 candidates per cycle) bounds cycle duration to approximately 4 minutes. However, `internal/scheduler/scheduler.go` currently executes static head truncation (`candidates[:s.cfg.ProbeLimit]`). Because upstream source aggregators typically return candidates in a stable deterministic order, candidates past the `ProbeLimit` cutoff remain unprobed indefinitely. In a 56,000 candidate pool with `ProbeLimit = 1,000`, 98% of the candidate population is permanently starved of verification opportunities.

## Current Behavior

1. **Static Head Truncation**: When `ProbeLimit > 0` and `len(candidates) > ProbeLimit`, `scheduler.runCycle` naively slices `candidates = candidates[:s.cfg.ProbeLimit]`.
2. **Tail Starvation**: The first $K$ candidates are re-tested every cycle while the remaining $N - K$ candidates are never probed.
3. **Store Absence Disconnect**: `Store.StartCycle` receives the full source list and correctly marks unprobed candidates as present (`skipped != absent`). While this protects unprobed candidates from premature eviction, they never receive probe opportunities to become servable.

## Desired Behavior

1. **Preserve Store Presence Semantics**:
   - `Store.StartCycle(linkSet)` continues to receive the COMPLETE upstream candidate raw-link set, preserving the invariant that `unprobed != absent`.
   - Candidate selection happens from the parsed `[]parser.Candidate` candidate set only AFTER the full linkSet has been established and dispatched to `StartCycle`.
   - Skipped candidates remain present and are not marked failed or absent in the Store.
2. **Canonical Scheduling Strategy: Least-Recently-Tested (LRT)**:
   - Implement Least-Recently-Tested (LRT) as the sole canonical scheduling policy.
   - Candidates are ordered by their last probe timestamp ascending.
   - Never-tested candidates have the zero timestamp (`time.Time{}`) and receive highest priority.
   - The first `ProbeLimit` candidates in this ordered sequence are selected for probing.
   - When a candidate is tested, its last-probed timestamp is updated from actual probe execution/results.
   - Skipped candidates must NOT have their last-tested timestamp advanced.
3. **Ephemeral Scheduler-Local Rotation State**:
   - Rotation state (e.g. `map[string]time.Time` keyed by canonical link) MUST remain ephemeral scheduler-local state.
   - Do NOT modify Store persistence.
   - Do NOT add a new state file or create a new persistence subsystem.
   - Daemon restarts reset this ephemeral state gracefully without permanent candidate starvation; transient re-testing of recently probed candidates on the first post-restart cycle is acceptable.
4. **Robustness to Churn & Reordering**:
   - LRT naturally and deterministically handles churn:
     - Newly added candidates (zero timestamp) receive immediate probe priority.
     - Removed candidates naturally age out of the upstream list without leaving stale index dependencies.
     - Upstream source reordering does not affect priority (timestamp-ordered, not index-based).
     - Failing candidates rotate naturally: updating their timestamp on completion moves them behind unprobed candidates, preventing monopoly.
5. **Explicit Fairness Guarantee**:
   - For a stable population of $N$ candidates and `ProbeLimit` $K$, every candidate must be probed within at most $2 \times \lceil N / K \rceil$ cycles.
6. **Boundary Invariants**:
   - `ProbeLimit == 0`: Legacy unlimited behavior (all candidates probed).
   - `ProbeLimit >= len(candidates)`: All candidates probed in the cycle.
   - `ProbeLimit < len(candidates)`: Exactly `ProbeLimit` candidates selected via the fair LRT rotation policy.
7. **Zero Impact on Downstream Layers**:
   - Tester probe mechanics, Store scoring formulas, TUI presentation, Subserver delivery, and Publisher mechanics remain unchanged.

## Exact Scope

- `internal/scheduler/scheduler.go`
- `internal/scheduler/scheduler_test.go`
- `internal/scheduler/rotation.go` (if extracted as a dedicated scheduling helper)

## Out of Scope

- Modifying `Store.StartCycle` or absence eviction rules.
- Modifying Store persistence, adding state files, or creating new persistence subsystems.
- Modifying Tester probe execution or timeout bounding.
- Modifying TUI, Publisher, or Subserver.

## Acceptance Criteria

- [ ] Canonical scheduling strategy is Least-Recently-Tested (LRT): candidates ordered by last probe timestamp ascending, never-tested candidates prioritized first.
- [ ] Rotation state is maintained strictly as ephemeral scheduler-local state (`map[string]time.Time`) without disk persistence or Store modifications.
- [ ] When `ProbeLimit > 0`, the number of candidates scheduled for probing in a cycle never exceeds `ProbeLimit`.
- [ ] For a stable population of $N$ candidates and `ProbeLimit` $K$, every candidate is guaranteed to be probed within at most $2 \times \lceil N / K \rceil$ cycles.
- [ ] Daemon restarts do not cause permanent candidate starvation. Transient re-testing of recently probed candidates on the first post-restart cycle is acceptable.
- [ ] Candidate additions, deletions, and upstream feed reordering between cycles do not cause index-out-of-bounds panics, duplicate testing within a single cycle, or skipped epochs.
- [ ] When `ProbeLimit == 0`, all candidates are probed, maintaining full backward compatibility.
- [ ] When `ProbeLimit >= len(candidates)`, all candidates are probed.
- [ ] `Store.StartCycle` receives the complete upstream raw-link set before candidate selection; skipped candidates are not marked failed or absent.
- [ ] Skipped candidates do not have their last-probed timestamp advanced.
- [ ] Deterministic unit tests verify eventual coverage, churn resilience, restart behavior, and boundary conditions.
- [ ] All scheduler tests and repository race checks pass cleanly.

## Dependencies

- None (can be implemented independently).
