# 03: fix(subserver): add context and graceful shutdown

Type: task
Status: open
Blocked by: None (can start immediately)

## Question

How do we provide graceful lifecycle control to `subserver.Server` to prevent listening socket and goroutine leaks in production and tests?

## What to build

Refactor `subserver.Server` to hold an underlying `*http.Server`, accept `ctx context.Context` in `Run(ctx)`, and shut down cleanly via `http.Server.Shutdown()` upon context cancellation.

## Acceptance criteria

- [ ] `Server.Run` accepts `context.Context`.
- [ ] Listening socket is closed and active connections are drained when context is cancelled.
- [ ] Unit test verifies server starts, serves requests, and terminates cleanly on context cancellation without hanging.
