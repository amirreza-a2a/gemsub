# 01: fix(scheduler): prevent store wipe on empty fetch

Type: task
Status: resolved
Blocked by: None (can start immediately)

## Question

How do we guarantee that transient upstream fetch failures or network outages do not cause `scheduler.runCycle` to call `st.StartCycle(emptySet)` and wipe out the entire persistent proxy store?

## What to build

In `Scheduler.runCycle`, verify the link count returned by `source.FetchAll`. If `len(links) == 0` and errors occurred (`len(fetchErrs) > 0`), log the failure and abort the cycle immediately. `Store.StartCycle` must not be called, probe testing must not run, and `Publisher.Publish` must not execute. All previously verified proxies in `Store` remain intact.

## Acceptance criteria

- [x] When `source.FetchAll` returns 0 links and at least 1 fetch error, `runCycle` returns early without modifying store state.
- [x] Existing entries in `Store` are preserved without being pruned or deleted.
- [x] Publishing is not invoked on an aborted cycle.
- [x] Unit tests in `internal/scheduler` verify that existing passing links remain intact after a failed fetch cycle.

## Answer

In `internal/scheduler/scheduler.go`, an empty-fetch guard was added immediately following `source.FetchAll(s.cfg.Sources)`. If `len(links) == 0 && len(fetchErrs) > 0`, the scheduler logs the errors and returns early. `Store.StartCycle` is bypassed, candidate testing is skipped, and `Publisher.Publish` is not invoked. Verified with `TestScheduler_FetchFailurePreservesStoreAndSkipsPublishing` in `internal/scheduler/scheduler_test.go`.
