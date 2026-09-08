# 20: feat(store): integrate explicit in-memory transport evidence

Type: feature
Status: ready-for-agent
Blocked by: None (can start immediately)

## Problem

The Store currently lacks any explicit record of transport health on in-memory probe results (`store.Result`). In Ticket 16, network health was implemented by *inferring* it backward from target-specific failure categories (`ErrRegionBlocked`, `ErrTargetDenied`). Now that the probing architecture produces explicit transport evidence, `store.Result` and the in-memory query projections must capture and prioritize this explicit evidence, while retaining Ticket 16's inference logic as a compatibility bridge for historical samples.

## Scope

1. Augment in-memory `store.Result`:
   - Add `TransportOK bool` (`json:"transport_ok"`).
   - Add `TransportLatency time.Duration` (`json:"transport_latency,omitempty"`).
2. Update `store.PutWithTransition(r)`:
   - Ensure `rec.Latest.TransportOK` and `rec.Latest.TransportLatency` are faithfully recorded on candidate state transitions.
   - Do NOT modify `ProbeSample` or `BoundedHistory` persistence in this ticket (strictly deferred to Phase 2).
3. Evolve `networkHealthyStateLocked(rec)` precedence hierarchy:
   - Tier 1: If `s.isServableRecordLocked(rec)` is true, return `healthy = true, servable = true`.
   - Tier 2 (New Explicit Evidence): If `rec.Latest.TransportOK` is true, return `healthy = true, servable = false`.
   - Tier 3 (Historical Compatibility Bridge): Fall back to Ticket 16's logic checking if `rec.History.LatestConclusive()` has `StatusFailed && Category.IsTargetSpecific()`.
4. Validate query projection invariance:
   - `Store.Passing()` and `Store.PassingRanked()` remain strictly Gemini-servable.
   - `Store.NetworkPassing()` and `Store.NetworkPassingRanked()` project candidates with `TransportOK == true` as network-healthy even when Gemini fails with non-target-specific errors (e.g. Gemini 500 error or application rejection).
   - `CandidateSnapshot.NetworkHealthy` reflects the updated evaluation.

## Acceptance Criteria

- [ ] `store.Result` carries `TransportOK` and `TransportLatency`.
- [ ] `PutWithTransition` preserves explicit transport fields on `rec.Latest`.
- [ ] `networkHealthyStateLocked` prioritizes `rec.Latest.TransportOK == true` over error category inference.
- [ ] Ticket 16's inference logic remains intact as a backward-compatibility fallback when `rec.Latest.TransportOK == false` or for legacy samples.
- [ ] `Store.Passing()` and `Store.PassingRanked()` return identical results to current baseline (strictly Gemini-servable).
- [ ] `Store.NetworkPassing()` successfully projects candidates where `TransportOK == true` regardless of Gemini application outcome.
- [ ] Persisted snapshot format (`gemsub_state.json` V2) remains 100% bit-compatible; no disk migration or `ProbeSample` changes occur in this ticket.

## Explicit Non-Goals

- Do not alter `ProbeSample` in `internal/store/history.go`.
- Do not bump `Snapshot.Version` to 3.
- Do not alter scoring weights, lambda decay, or policy gate rules.
- Do not alter Publisher or Subserver behavior.

## Architectural Invariants Preserved

- `Transport Health != Target Servability`: A candidate with `TransportOK == true` is network-healthy even if its Gemini application probe failed.
- `Store.Passing() Immutability`: Gemini servability gates (Gate 2 target policy override, Gate 4 score) are completely isolated from transport health evidence.
- `Persistence Isolation`: Disk serialization remains on Version 2 without structural changes.

## Expected Files/Modules

- `internal/store/store.go`
- `internal/store/network_healthy_test.go`
- `internal/store/store_test.go`

## Test Requirements

- Test `PutWithTransition` storing `TransportOK = true` and `TransportLatency`.
- Test `NetworkPassing()` including candidates with `TransportOK = true` and Gemini `StatusFailed` (e.g. `ErrTargetError`, `ErrRegionBlocked`).
- Test `Passing()` excluding candidates with `TransportOK = true` but failing Gemini servability.
- Test backward-compatibility bridge: candidate without explicit `TransportOK` but with `ErrRegionBlocked` still passes `NetworkPassing()`.
- Test snapshot load/save cycle ensuring Version 2 compatibility is maintained.
