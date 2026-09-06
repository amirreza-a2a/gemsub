# 03: fix(subserver): add context and graceful shutdown

Type: task
Status: resolved
Blocked by: None (can start immediately)

## Question

How do we provide graceful lifecycle control to `subserver.Server` to prevent listening socket and goroutine leaks in production and tests?

## What to build

Refactor `subserver.Server` to hold an underlying `*http.Server`, accept `ctx context.Context` in `Run(ctx)`, and shut down cleanly via `http.Server.Shutdown()` upon context cancellation.

## Acceptance criteria

- [x] `Server.Run` accepts `context.Context`.
- [x] Listening socket is closed and active connections are drained when context is cancelled.
- [x] Unit test verifies server starts, serves requests, and terminates cleanly on context cancellation without hanging.

## Answer

1. Refactored `subserver.Server.Run(ctx context.Context) error` to manage an owned `*http.Server` instance.
2. When `ctx.Done()` fires, an independent 5-second timeout context triggers `httpSrv.Shutdown()`.
3. Synchronized shutdown completion via `shutdownDone` channel to eliminate data races between `ListenAndServe()` unblocking and `Shutdown()` finishing, returning `nil` on clean `http.ErrServerClosed` shutdown.
4. Protected early startup exit (bind failures) via `serverStopped` coordination channel to prevent goroutine leaks.
5. Updated callers in `cmd/gemsub/main.go` and `internal/tester/e2e_positive_test.go`.
6. Added full unit test coverage in `internal/subserver/server_test.go`.
