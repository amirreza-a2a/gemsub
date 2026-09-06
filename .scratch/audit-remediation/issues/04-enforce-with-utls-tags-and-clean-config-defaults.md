# 04: build: enforce with_utls tags and clean config defaults

Type: task
Status: resolved
Blocked by: None (can start immediately)

## Question

How do we ensure reproducible compilation with uTLS/Reality support while eliminating hardcoded personal repository fallbacks from the codebase?

## What to build

Create a root `Makefile` providing `build` (with `-tags with_utls`) and `test` targets. Remove hardcoded personal repository string from `internal/config/config.go`, enforcing explicit `remote_url` configuration when publishing is enabled.

## Acceptance criteria

- [x] `Makefile` compiles `./cmd/gemsub` with `-tags with_utls` and flags `-s -w`.
- [x] `internal/config` does not default to `git@github.com:amirreza-a2a/...`.
- [x] Validation errors if `publishing.enabled` is true but `remote_url` is empty.
- [x] Unit tests pass with new configuration rules.

## Answer

1. Created root `Makefile` with `build` (`go build -tags with_utls -ldflags "-s -w" -o gemsub ./cmd/gemsub`), `test` (`go test -tags with_utls -count=1 ./...`), and `clean` targets.
2. Removed hardcoded personal repository default from `internal/config/config.go`. Enforced that when `publishing.enabled` is true, both `repository` and `remote_url` must be non-empty. When disabled, empty fields remain valid and are not populated.
3. Removed hardcoded fallback from `internal/publisher/publisher.go` and enforced that `Publish()` returns an error if `RemoteURL` is empty.
4. Added validation in `cmd/gemsub/main.go` when `-publish` CLI flag explicitly enables publishing.
5. Replaced personal repository mock test URLs in `internal/publisher/publisher_test.go` and `internal/scheduler/scheduler_test.go` with generic example fixtures.
6. Updated `config.example.json` and `README.md` to document explicit `remote_url` configuration with a generic example.
7. Added unit tests for explicit publishing config, missing `remote_url`, missing `repository`, disabled config defaults, and publisher empty `RemoteURL` rejection.
