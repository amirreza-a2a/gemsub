# 08: feat(events): implement event bus for scheduler and tester

Type: task
Status: resolved
Blocked by: 07

## Question

How do we expose real-time probe progress and cycle lifecycle events to the TUI and other external observers without tightly coupling the engine?

## What to build

Implement an `EventBus` in `internal/events` that emits typed events (`CycleStarted`, `CandidatesLoaded`, `ProbeCompleted`, `CycleFinished`). Wire `Scheduler` and `RunPool` to publish events to subscribers.

## Acceptance criteria

- [x] `EventBus` broadcasts events without blocking the test loop.
- [x] Subscribed channels receive progress metrics (`Completed`, `Total`, `Passed`, `Failed`, `Inconclusive`).
- [x] Unit tests verify event ordering and subscriber delivery.

## Answer

Implemented the event publishing mechanism in `internal/events` and wired it into `tester.RunPool` and `scheduler.Scheduler`:

1. **`internal/events`**:
   - Implemented `EventBus` with non-blocking broadcast publish semantics using per-subscriber FIFO queues and background forwarders.
   - Defined typed events: `CycleStarted`, `CandidatesLoaded`, `ProbeCompleted`, and `CycleFinished`.
   - Embedded `ProgressMetrics` (`Completed`, `Total`, `Passed`, `Failed`, `Inconclusive`) into progress events.
   - Provided thread-safe `Subscribe`, `Unsubscribe`, `Publish`, and `Close` methods.

2. **`internal/tester`**:
   - Wired `RunPool` and `RunPoolWithRunner` to accept `*events.EventBus` and publish `ProbeCompleted` events containing live cumulative `ProgressMetrics`.
   - Ensured cancelled probes are discarded and do not publish spurious completion events.

3. **`internal/scheduler`**:
   - Wired `Scheduler` to manage an `EventBus` instance and pass it to `RunPool`.
   - Published `CycleStarted` at cycle kickoff, `CandidatesLoaded` when candidates are selected, and `CycleFinished` upon cycle completion, fetch error abort, or cancellation.

4. **Verification**:
   - Unit tests in `internal/events/bus_test.go` verified delivery, strict FIFO ordering, non-blocking broadcast to slow subscribers, unsubscribe, close, and concurrent publishing under race detector.
   - Unit tests in `internal/tester/pool_test.go` verified progress metrics aggregation and cancellation behavior.
   - Unit tests in `internal/scheduler/scheduler_test.go` verified full lifecycle emission and ordering.
   - Verified `make test` and `git diff --check` pass cleanly.
