# 21: feat(tester): two-stage probe orchestrator

Type: feature
Status: ready-for-agent
Blocked by: 18, 19, 20

## Problem

`executeAttempt` in `internal/tester/probe.go` currently executes a monolithic Gemini-only probe attempt. With the transport health probe (Ticket 18), Gemini application probe (Ticket 19), and in-memory transport evidence (Ticket 20) in place, the testing engine needs an orchestrator that coordinates two-stage probing through a single shared sing-box Box and dialer, enforcing early exit on transport failure and preserving existing retry, rate limiting, and cancellation semantics.

## Scope

1. Refactor `internal/tester/probe.go`:
   - Replace the body of `executeAttempt` with two-stage orchestration.
   - Maintain the `buildDialer` Box lifecycle: instantiate the sing-box instance once per candidate attempt and register `defer closeBox()` immediately at the top of the attempt.
2. Implement Stage 1 (Transport Health):
   - Derive bounded health context: `healthCtx, cancelHealth := context.WithTimeout(attemptCtx, cfg.HealthTimeout)`.
   - Call `transport.Probe(healthCtx, dialFn, transportCfg)`.
   - Call `cancelHealth()` immediately when Stage 1 completes to release timer resources.
   - If `!tr.OK`:
     - Record `TransportOK = false`, `TransportLatency = tr.Latency`.
     - Assign category and status from transport outcome (`ErrTimeout`, `ErrTLS`, `ErrProxyError`, etc.).
     - **Early Exit**: Do NOT execute Stage 2. Return immediately so the Box is torn down.
3. Implement Stage 2 (Gemini Application):
   - Executed if and only if Stage 1 yields `tr.OK == true`.
   - Record `TransportOK = true`, `TransportLatency = tr.Latency`.
   - Pass remaining `attemptCtx` and the same `dialFn` to `gemini.Probe(attemptCtx, dialFn, geminiCfg)`.
   - Merge Gemini outcome into `ClassificationResult`: `Status`, `Category`, `StatusCode`, `Reason`, `Retryable`.
4. Preserve Rate Limiting, Retry, and Cancellation Contracts:
   - Rate limiting remains strictly 1 token per candidate attempt in `ProbeWithExecutor` before `buildDialer`.
   - Retries remain at the candidate-attempt level: each retry spins up a fresh Box and restarts from Stage 1.
   - Propagate `Retry-After` returned by either Stage 1 or Stage 2 back to `ProbeWithExecutor`.
   - Respect parent `ctx` cancellation: if cancelled during Stage 1 or Stage 2, tear down the Box and report `StatusInconclusive` with `ErrTimeout`.

## Acceptance Criteria

- [ ] Each candidate probe attempt spins up exactly one sing-box Box instance, reusing its `dialFn` across both stages.
- [ ] Stage 1 failure completely skips Stage 2 (no HTTP request is ever sent to Gemini on transport failure).
- [ ] Stage 1 failure sets `TransportOK = false` on the resulting `store.Result`.
- [ ] Stage 1 success sets `TransportOK = true` and `TransportLatency` on the resulting `store.Result`.
- [ ] Stage 2 failures (e.g. `ErrRegionBlocked`, `ErrTargetDenied`, `ErrTargetError`) preserve `TransportOK = true`.
- [ ] Timeout during Stage 1 cannot exceed `cfg.HealthTimeout` (4s default) and aborts early.
- [ ] Timeout during Stage 2 preserves `TransportOK = true` and attributes timeout to Gemini (`StatusInconclusive`).
- [ ] Exactly 1 rate limit token is consumed per candidate attempt regardless of whether Stage 2 executes.
- [ ] Existing retry behavior (honoring `Retry-After`, backoff with jitter) functions identically.
- [ ] Unit tests in `internal/tester/retry_test.go` and `internal/tester/probe_test.go` pass.

## Explicit Non-Goals

- Do not implement multiple concurrent target probes in Stage 2.
- Do not add independent retry loops inside Stage 1 or Stage 2.
- Do not create a persistent sing-box Box pool across candidate attempts.
- Do not implement Claude probing.

## Architectural Invariants Preserved

- `Shared Outbound Seam`: One sing-box Box and one `dialFn` per attempt, eliminating duplicate tunnel initialization.
- `Sequential Gating`: Stage 2 runs if and only if Stage 1 succeeds; dead proxies never contact Gemini.
- `Transport Health != Target Compatibility`: Gemini rejection leaves `TransportOK = true` intact.
- `Rate Limiting Invariance`: Rate limiting governs candidate attempts, not individual HTTP sub-probes.

## Expected Files/Modules

- `internal/tester/probe.go`
- `internal/tester/retry_test.go`
- `internal/tester/pool_test.go`

## Test Requirements

- Test candidate where transport fails (mocked dial error): verify Stage 2 is not called, `TransportOK == false`.
- Test candidate where transport succeeds and Gemini succeeds: verify `StatusPassed`, `TransportOK == true`.
- Test candidate where transport succeeds and Gemini returns 200 with regional block: verify `ErrRegionBlocked`, `TransportOK == true`.
- Test candidate where transport succeeds and Gemini returns 403: verify `ErrTargetDenied`, `TransportOK == true`.
- Test candidate where transport succeeds and Gemini returns 429: verify retry backoff triggers and honors `Retry-After`.
- Test health timeout expiry: verify attempt aborts within `HealthTimeout` without executing Stage 2.
