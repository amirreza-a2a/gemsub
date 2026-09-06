# 07: refactor(logging): decouple logging with log/slog

Type: task
Status: resolved
Blocked by: None (can start immediately)

## Question

How do we decouple engine logging from `log.Printf` to prevent terminal UI corruption while supporting structured logs?

## What to build

Migrate logging across `scheduler`, `tester`, `publisher`, and `subserver` to `log/slog`. Provide an in-memory `RingLogHandler` for TUI mode and standard output handler for headless mode.

## Acceptance criteria

- [x] All `log.Printf` calls replaced with structured `slog` calls.
- [x] `RingLogHandler` captures recent log records in memory for UI viewing.
- [x] Headless mode retains standard timestamped terminal logging.

## Answer

1. Created `internal/logging` package implementing thread-safe `RingLogHandler` with circular in-memory buffer, attribute/group nesting preservation, dynamic level filtering, and human-readable string formatting for terminal UI display.
2. Implemented `NewStandardHandler` and `Setup(headless, ringCapacity, w)` providing standard timestamped terminal logging via `slog.NewTextHandler` in headless mode and in-memory ring capture in TUI mode.
3. Migrated all logging in `scheduler`, `publisher`, `subserver`, `tester`, and `cmd/gemsub` from `log.Printf`/`log.Println`/`log.Fatalf` to structured `slog` calls with typed attributes and zero legacy log dependencies.
4. Added structured `slog.Debug` diagnostics in `tester` for probe attempts, retries with backoff diagnostics, and pool lifecycle.
5. Added unit and race tests in `internal/logging/logging_test.go` verifying record retention, ring wrapping, clearing, attribute/group propagation, and high-concurrency safety.
