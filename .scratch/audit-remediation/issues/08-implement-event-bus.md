# 08: feat(events): implement event bus for scheduler and tester

Type: task
Status: open
Blocked by: 07

## Question

How do we expose real-time probe progress and cycle lifecycle events to the TUI and other external observers without tightly coupling the engine?

## What to build

Implement an `EventBus` in `internal/events` that emits typed events (`CycleStarted`, `CandidatesLoaded`, `ProbeCompleted`, `CycleFinished`). Wire `Scheduler` and `RunPool` to publish events to subscribers.

## Acceptance criteria

- [ ] `EventBus` broadcasts events without blocking the test loop.
- [ ] Subscribed channels receive progress metrics (`Completed`, `Total`, `Passed`, `Failed`, `Inconclusive`).
- [ ] Unit tests verify event ordering and subscriber delivery.
