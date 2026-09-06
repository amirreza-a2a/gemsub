# 04: build: enforce with_utls tags and clean config defaults

Type: task
Status: open
Blocked by: None (can start immediately)

## Question

How do we ensure reproducible compilation with uTLS/Reality support while eliminating hardcoded personal repository fallbacks from the codebase?

## What to build

Create a root `Makefile` providing `build` (with `-tags with_utls`) and `test` targets. Remove hardcoded personal repository string from `internal/config/config.go`, enforcing explicit `remote_url` configuration when publishing is enabled.

## Acceptance criteria

- [ ] `Makefile` compiles `./cmd/gemsub` with `-tags with_utls` and flags `-s -w`.
- [ ] `internal/config` does not default to `git@github.com:amirreza-a2a/...`.
- [ ] Validation errors if `publishing.enabled` is true but `remote_url` is empty.
- [ ] Unit tests pass with new configuration rules.
