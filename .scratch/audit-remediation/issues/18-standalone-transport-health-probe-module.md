# 18: feat(tester): standalone transport health probe module

Type: feature
Status: ready-for-agent
Blocked by: 17

## Problem

Generic network transport health is currently entangled with Gemini application probing in `internal/tester/probe.go`. There is no dedicated component whose sole responsibility is verifying that a proxy outbound can establish a TCP/TLS connection and complete an HTTP transaction through sing-box without downloading or inspecting application payloads.

## Scope

1. Create a dedicated transport probe module (recommended location: `internal/tester/transport`):
   - Define `DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)` matching the sing-box dialer signature.
   - Define `Config` with `HealthURL string` and `HealthTimeout time.Duration`.
   - Define `Result` containing:
     - `OK bool` (true only on verified transport success)
     - `Latency time.Duration`
     - `StatusCode int`
     - `Category store.ErrorCategory`
     - `Error error`
2. Implement `Probe(ctx context.Context, dialFn DialFunc, cfg Config) Result`:
   - Construct `&http.Client` with `Transport: &http.Transport{DialContext: dialFn}`.
   - Enforce strict redirect disabling: `CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }`.
   - Issue minimal `GET` (or `HEAD`) request to `cfg.HealthURL` with minimal neutral headers (`User-Agent: gemsub-health/1.0`).
   - Immediately close `resp.Body.Close()` without reading or buffering response body bytes.
3. Enforce strict transport success semantics:
   - Success: Connection and TLS handshake succeed, and response status is `204 No Content` (or `2xx` if non-204 endpoint configured). Returns `OK = true`, `Category = store.ErrNone`.
   - 3xx Redirect: Treated as transport failure (`OK = false`, `Category = store.ErrProxyError`, indicating captive portal or interception).
   - 4xx/5xx Client/Server Error: Treated as transport failure (`OK = false`, `Category = store.ErrProxyError` or `ErrProxyRateLimited` if 429).
   - Dial/TLS/Timeout Errors: Mapped via `ClassifyDialError` (`ErrConnRefused`, `ErrTLS`, `ErrTimeout`, `ErrReset`, `ErrReality`).
4. Implement comprehensive unit tests utilizing local `httptest.Server` and TLS servers.

## Acceptance Criteria

- [ ] Dedicated transport probe module contains zero imports or knowledge of Gemini, Claude, `WIZ_global_data`, HTML parsing, or region codes.
- [ ] Successful `204 No Content` response yields `Result.OK == true`, `StatusCode == 204`, and `Category == store.ErrNone`.
- [ ] 3xx Redirects are never followed and return `Result.OK == false`.
- [ ] Response body is closed immediately without reading any payload bytes.
- [ ] Handshake and network errors map to correct canonical `store.ErrorCategory` values (`ErrTimeout`, `ErrTLS`, `ErrConnRefused`, `ErrReset`).
- [ ] Probe strictly respects `cfg.HealthTimeout` and parent context cancellation.
- [ ] 100% unit test coverage for success, redirect rejection, non-204 status, timeout, and dial errors.

## Explicit Non-Goals

- Do not implement sing-box Box construction in this module (it accepts an existing `DialFunc`).
- Do not download or inspect HTML or body contents.
- Do not inspect cookies or origin server headers.
- Do not integrate into `RunPool` or `executeAttempt` in this ticket (deferred to Ticket 21).

## Architectural Invariants Preserved

- `Transport Health != Target Compatibility`: The transport probe only answers whether network transport functions; it makes no judgment on whether Gemini or any other AI service is accessible or servable.
- `Zero Payload Inspection`: Transport verification incurs no body download overhead.

## Expected Files/Modules

- `internal/tester/transport/probe.go`
- `internal/tester/transport/probe_test.go`

## Test Requirements

- Unit test with TLS `httptest.Server` returning HTTP 204 (verified success).
- Unit test with mock server returning HTTP 302 redirect (verified failure, no redirect followed).
- Unit test with mock server returning HTTP 403 / 500 (verified failure).
- Unit test with slow/hanging mock server verifying `HealthTimeout` cancellation.
- Unit test with failing `dialFn` verifying `ClassifyDialError` integration.
