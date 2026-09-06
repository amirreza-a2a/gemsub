# 02: fix(tester): discard cancelled probe results

Type: task
Status: resolved
Blocked by: None (can start immediately)

## Question

How do we prevent cancelled probe attempts from being recorded as `StatusInconclusive` failures and erroneously penalizing healthy proxies on daemon shutdown/restart?

## What to build

In `RunPool` / `Probe`, if context cancellation is detected (`ctx.Err() != nil` or `errors.Is(err, context.Canceled)`), drop the probe outcome instead of invoking `Store.PutWithTransition(r)` with inconclusive status.

## Acceptance criteria

- [x] Cancelled probes are not passed to `onResult` or `Store.PutWithTransition`.
- [x] `ConsecutiveInconclusive` counter is not incremented on interrupted runs.
- [x] Unit tests verify that context cancellation during probe execution preserves store state.

## Answer

1. In `internal/tester/pool.go`, added `if ctx.Err() != nil { return }` immediately after `runner` completes to drop cancelled probe outcomes before invoking `onResult(result)`.
2. In `internal/tester/classifier.go`, added explicit handling for `context.Canceled` (standard, wrapped, and stringified) in `ClassifyDialError`, returning `StatusInconclusive` with `Reason: "context canceled"` instead of misclassifying as `ErrProxyError`.
3. Verified via regression tests in `internal/tester/pool_test.go` and `internal/tester/classifier_test.go`.
