# 16: feat(store): preserve and project network-healthy candidates

Type: feature
Status: resolved
Blocked by: 15 (resolved)

## Problem

Following the Ticket 15 audit, candidates that pass proxy/network transport health but fail Gemini-specific verification (due to `ErrRegionBlocked` or `ErrTargetDenied`) are retained in the `Store` state file and history, but are excluded from all query projections. `Store.Passing()` strictly requires Gemini servability (Gate 2 rejects `ErrRegionBlocked` and `ErrTargetDenied`). There is currently no way to query or project candidates whose underlying network transport is proven healthy.

## Required contract

1. Preserve the existing Gemini-oriented `Store.Passing()` and `Store.PassingRanked()` semantics.
2. Do NOT make `ErrRegionBlocked` or `ErrTargetDenied` Gemini-servable in `Store.Passing()`.
3. Provide an explicit classifier/method to distinguish target-specific rejections (where transport succeeded) from transport/network failures.
4. Expose a `Store.NetworkPassing()` (and `Store.NetworkPassingRanked()`) projection that returns candidates with proven transport health:
   - Candidates that passed Gemini verification (`Passing()`), OR
   - Candidates whose latest conclusive test succeeded in transport (completed proxy dial & HTTP exchange) but were rejected solely by target-specific restrictions (`ErrRegionBlocked`, `ErrTargetDenied`), with active presence (not absent/pruned).
5. Do not alter existing Publisher and Subserver default subscription behavior.
6. Preserve backward compatibility with existing persisted state (`gemsub_state.json`).
7. Do not redesign generic Target architecture or modify unrelated scoring/history/scheduler/probe behavior.

## Acceptance criteria

- [x] Existing `Store.Passing()` and `Store.PassingRanked()` semantics remain strictly Gemini-servable (`ErrRegionBlocked` and `ErrTargetDenied` remain non-servable for Gemini).
- [x] Error category classification clearly identifies target-specific errors where proxy transport succeeded.
- [x] `Store.NetworkPassing()` returns candidates with proven network transport health without corrupting Gemini servability.
- [x] `Store.NetworkPassingRanked()` returns network-healthy candidates ranked consistently with latency/score.
- [x] Existing persisted state files (V2) load cleanly and support network-healthy projections without data migration or format breakage.
- [x] Publisher and Subserver continue to serve only Gemini-passing candidates by default.
- [x] Focused unit tests added for network-healthy query projections, edge cases, and gate invariants.

## Implementation Summary

1. **ErrorCategory Classification**:
   - Added `ErrorCategory.IsTargetSpecific() bool` (`ErrRegionBlocked`, `ErrTargetDenied`).
   - Removed dead API `ErrorCategory.IsNetworkHealthy()`.

2. **Authoritative History Query (`LastNetworkHealthyLatency`)**:
   - Scans backward from newest to oldest.
   - If a network-healthy sample is found, returns its latency and `true`.
   - If a conclusive transport failure (`StatusFailed && !IsTargetSpecific()`) is encountered before any healthy sample, scanning immediately halts and returns `(0, false)`, ensuring a later transport failure cleanly invalidates prior network-health evidence.

3. **Store Projections & Lifecycle Integration**:
   - Added `Store.networkHealthyStateLocked(rec) (healthy bool, servable bool)` resolving both properties in a single evaluation without redundant calls.
   - Decoupled timeout handling: removed the artificial `ConsecutiveInconclusive() > MaxAbsentCycles` coupling from `isNetworkHealthyRecordLocked()`, relying strictly on the candidate's latest authoritative conclusive observation and source presence gates.
   - Added `Store.IsNetworkHealthyRecord(rec) bool`.
   - Added `Store.NetworkPassing() []string` returning active links of all network-healthy candidates in stable alphabetical order.
   - Added `Store.NetworkPassingRanked() []string`:
     - Tier 1: Gemini-servable candidates strictly outrank target-incompatible candidates (Score desc, Latency asc, Link asc).
     - Tier 2: Target-incompatible candidates are ranked strictly by **network-healthy latency ascending**, then **active link ascending**. Gemini-specific `rec.Score` is **not** used in Tier 2, preventing candidates with timeouts from outranking clean target-blocked nodes.
   - Augmented `CandidateSnapshot` with `NetworkHealthy bool` populated in `Snapshots()`.

4. **Zero-Migration State Compatibility**:
   - Network health is dynamically derived on demand from `BoundedHistory` and presence tracking. The persisted schema of `gemsub_state.json` V2 remains completely backward-compatible.


