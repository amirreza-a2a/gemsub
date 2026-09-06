# 05: feat(source): add context support to FetchAll

Type: task
Status: open
Blocked by: 01

## Question

How do we make upstream fetching responsive to daemon cancellation and prevent hanging during network timeouts?

## What to build

Update `source.FetchAll` and `fetchOne` to accept `ctx context.Context`, pass it to `http.NewRequestWithContext`, and replace blocking sleeps with interruptible `select` on `ctx.Done()`.

## Acceptance criteria

- [ ] `FetchAll` and `fetchOne` accept `context.Context`.
- [ ] In-flight HTTP requests and retry backoff abort immediately when `ctx.Done()` fires.
- [ ] Unit tests verify fast exit upon context cancellation.
