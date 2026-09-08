# 22: test(tester): two-stage probe integration and invariant verification

Type: test
Status: ready-for-agent
Blocked by: 21

## Problem

With the Phase 1 components implemented across configuration (Ticket 17), transport probing (Ticket 18), Gemini isolation (Ticket 19), Store evidence (Ticket 20), and orchestration (Ticket 21), a comprehensive suite of integration and regression tests is required to verify end-to-end execution, cycle lifecycle consistency, and strict preservation of all architectural invariants.

## Scope

1. Create comprehensive two-stage integration tests (e.g. in `internal/tester/two_stage_integration_test.go` and `internal/scheduler/scheduler_test.go`):
   - **Case A (Transport Failure)**: Mock dialer / transport server returns connection refused or times out. Verify mock Gemini server receives 0 requests, `Result.TransportOK == false`, and candidate is excluded from both `Store.Passing()` and `Store.NetworkPassing()`.
   - **Case B (Transport Pass + Gemini Pass)**: Mock transport returns 204; mock Gemini returns 200 with valid brand markers. Verify `Result.TransportOK == true`, `Result.Status == StatusPassed`, and candidate is present in both `Store.Passing()` and `Store.NetworkPassing()`.
   - **Case C (Transport Pass + Gemini Region Blocked)**: Mock transport returns 204; mock Gemini returns 200 with `LOCATION_REJECTED` or block phrase. Verify `Result.TransportOK == true`, `Result.Status == StatusFailed`, `Result.Category == ErrRegionBlocked`. Candidate is **excluded** from `Store.Passing()` but **included** in `Store.NetworkPassing()`.
   - **Case D (Transport Pass + Gemini Target Denied)**: Mock transport returns 204; mock Gemini returns 403 Forbidden. Verify `Result.TransportOK == true`, `Result.Status == StatusFailed`, `Result.Category == ErrTargetDenied`. Candidate is excluded from `Store.Passing()`, included in `Store.NetworkPassing()`.
   - **Case E (Transport Pass + Gemini Server Error)**: Mock transport returns 204; mock Gemini returns 503 Service Unavailable. Verify `Result.TransportOK == true`, `Result.Status == StatusInconclusive`. Candidate is excluded from `Store.Passing()`, included in `Store.NetworkPassing()`.
   - **Case H (Gemini Timeout)**: Transport check succeeds; Gemini request times out. Verify `Result.TransportOK == true`, `Result.Status == StatusInconclusive`, `Result.Category == ErrTimeout`.
2. Verify Transport Protocol Invariants:
   - Transport server returns HTTP 302 redirect: verify transport probe fails without following redirect; Gemini is not requested.
   - Transport server returns HTTP 204: verify zero body read operations occur.
3. Scheduler & Publication Invariance:
   - Execute full `scheduler.runCycle()` across mixed mock candidate pool.
   - Verify `Store.Passing()` feeds the publisher and subserver exclusively with Gemini-passing candidates.
   - Verify `Store.Save()` generates a snapshot that reloads cleanly without data loss.
4. Legacy Configuration Compatibility:
   - Run end-to-end test initializing from a legacy configuration containing only `target_url` and `block_phrases`.
   - Verify identical cycle execution and classification outcomes.

## Acceptance Criteria

- [ ] All 8 fundamental state combinations (States A–H from architecture spec §9) are explicitly tested and assert exact `Status`, `Category`, `TransportOK`, and `NetworkPassing()` outcomes.
- [ ] Transport failure guarantees zero HTTP traffic to the Gemini endpoint.
- [ ] Gemini regional blocking and target denial never invalidate `TransportOK = true`.
- [ ] Transport probe never follows redirects or buffers response bodies.
- [ ] Full scheduler cycle runs to completion and updates Store, Publisher, and Subserver without regression.
- [ ] Existing `Store.Passing()` and `PassingRanked()` outputs remain 100% strictly Gemini-servable.
- [ ] `Store.NetworkPassing()` successfully projects transport-healthy candidates rejected by Gemini.
- [ ] Existing configurations load seamlessly with zero breaking behavior.

## Explicit Non-Goals

- Do not implement Claude tests.
- Do not test or assert Version 3 persistence (deferred to Phase 2).
- Do not benchmark sing-box execution speeds.

## Architectural Invariants Preserved

- `Transport Health != Gemini Compatibility != Target Servability`: All three dimensions are independently verified across the test matrix.
- `Publisher/Subserver Stability`: External subscriptions continue serving strictly Gemini-servable candidates.
- `Complete End-to-End Regression Protection`: No changes to existing public behavior for end users.

## Expected Files/Modules

- `internal/tester/two_stage_integration_test.go`
- `internal/scheduler/scheduler_test.go`
- `internal/tester/e2e_positive_test.go`

## Test Requirements

- Deterministic mock HTTP and TLS servers.
- Assertions on HTTP request counts to transport vs Gemini endpoints.
- Verification of Store queries (`Passing`, `NetworkPassing`, `Snapshots`).
