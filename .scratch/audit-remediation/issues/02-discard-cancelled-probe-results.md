# 02: fix(tester): discard cancelled probe results

Type: task
Status: open
Blocked by: None (can start immediately)

## Question

How do we prevent cancelled probe attempts from being recorded as `StatusInconclusive` failures and erroneously penalizing healthy proxies on daemon shutdown/restart?

## What to build

In `RunPool` / `Probe`, if context cancellation is detected (`ctx.Err() != nil` or `errors.Is(err, context.Canceled)`), drop the probe outcome instead of invoking `Store.PutWithTransition(r)` with inconclusive status.

## Acceptance criteria

- [ ] Cancelled probes are not passed to `onResult` or `Store.PutWithTransition`.
- [ ] `ConsecutiveInconclusive` counter is not incremented on interrupted runs.
- [ ] Unit tests verify that context cancellation during probe execution preserves store state.
