# 07: refactor(logging): decouple logging with log/slog

Type: task
Status: open
Blocked by: None (can start immediately)

## Question

How do we decouple engine logging from `log.Printf` to prevent terminal UI corruption while supporting structured logs?

## What to build

Migrate logging across `scheduler`, `tester`, `publisher`, and `subserver` to `log/slog`. Provide an in-memory `RingLogHandler` for TUI mode and standard output handler for headless mode.

## Acceptance criteria

- [ ] All `log.Printf` calls replaced with structured `slog` calls.
- [ ] `RingLogHandler` captures recent log records in memory for UI viewing.
- [ ] Headless mode retains standard timestamped terminal logging.
