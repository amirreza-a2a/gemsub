# 05: feat(source): add context support to FetchAll

Type: task
Status: resolved
Blocked by: 01

## Question

How do we make upstream fetching responsive to daemon cancellation and prevent hanging during network timeouts?

## What to build

Update `source.FetchAll` and `fetchOne` to accept `ctx context.Context`, pass it to `http.NewRequestWithContext`, and replace blocking sleeps with interruptible `select` on `ctx.Done()`.

## Acceptance criteria

- [x] `FetchAll` and `fetchOne` accept `context.Context`.
- [x] In-flight HTTP requests and retry backoff abort immediately when `ctx.Done()` fires.
- [x] Unit tests verify fast exit upon context cancellation.

## Answer

1. Refactored `source.FetchAll(ctx context.Context, urls []string)` and `fetchOne(ctx context.Context, u string)` to accept and propagate `context.Context`.
2. Replaced `http.NewRequest` with `http.NewRequestWithContext(ctx, ...)` so in-flight HTTP requests and body reads abort immediately on cancellation.
3. Replaced blocking `time.Sleep` during retry backoff with interruptible `select` on `ctx.Done()` and a timer, preventing uncancelable hangs.
4. Added immediate cancellation checks before requests, between retry attempts, and across source URLs to short-circuit processing when context is cancelled.
5. Updated `Scheduler.runCycle` in `internal/scheduler/scheduler.go` to pass `ctx` to `source.FetchAll`.
6. Added comprehensive unit tests in `internal/source/fetch_test.go` covering successful fetch/deduplication, pre-cancelled contexts, in-flight request cancellation, retry backoff interruption, and fast exit on timeout.
