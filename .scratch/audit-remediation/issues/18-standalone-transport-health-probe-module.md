# 18: feat(tester): standalone transport health probe module

Type: feature
Status: resolved
Blocked by: 17 (resolved)

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

- [x] Dedicated transport probe module contains zero imports or knowledge of Gemini, Claude, `WIZ_global_data`, HTML parsing, or region codes.
- [x] Successful `204 No Content` response yields `Result.OK == true`, `StatusCode == 204`, and `Category == store.ErrNone`.
- [x] 3xx Redirects are never followed and return `Result.OK == false`.
- [x] Response body is closed immediately without reading any payload bytes.
- [x] Handshake and network errors map to correct canonical `store.ErrorCategory` values (`ErrTimeout`, `ErrTLS`, `ErrConnRefused`, `ErrReset`).
- [x] Probe strictly respects `cfg.HealthTimeout` and parent context cancellation.
- [x] 100% unit test coverage for success, redirect rejection, non-204 status, timeout, and dial errors.

## Implementation Summary

1. **Standalone Module (`internal/tester/transport/probe.go`)**:
   - Implemented `DialFunc`, `Config` (`HealthURL`, `HealthTimeout`), and `Result` (`OK`, `Latency`, `StatusCode`, `Category`, `Error`).
   - Implemented `Probe(ctx context.Context, dialFn DialFunc, cfg Config) Result` using per-call `http.Transport` with `DisableKeepAlives: true` and `defer tr.CloseIdleConnections()`.
   - Set `CheckRedirect` to return `http.ErrUseLastResponse` to prevent redirect following.
   - Sent minimal neutral headers (`User-Agent: gemsub-health/1.0`, `Accept: */*`), completely target-agnostic.
   - Closed `resp.Body.Close()` immediately without reading or buffering any bytes.

2. **Strict Success and Error Classification**:
   - Evaluated `isExpectedSuccess(statusCode, healthURL)`: For `generate_204` endpoints, strictly requires status 204. For custom endpoints, allows 2xx.
   - Mapped 3xx redirects to `ErrProxyError`.
   - Mapped 429 to `ErrProxyRateLimited`.
   - Mapped other non-2xx/unexpected statuses to `ErrProxyError`.
   - Classified dial/TLS/timeout errors into canonical `store.ErrorCategory` (`ErrTimeout`, `ErrTLS`, `ErrConnRefused`, `ErrReset`, `ErrReality`, `ErrConfig`, `ErrProxyError`).

3. **Verification & Tests (`internal/tester/transport/probe_test.go`)**:
   - Added 14 unit tests covering 204 success, 301/302/307 redirects, single request assertion, 403/500/429 status codes, connection refused, TLS handshake failure, timeout bounds, parent cancellation, non-consumption of body payload, AST inspection for target-agnostic symbols, and custom endpoint semantics. All 14 tests pass.

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

## Review Remediation

Following the code review (verdict: REQUEST CHANGES), the following remediations were implemented and verified:

### P1-1 — Genuine HTTPS Success Regression Test
- **Finding**: The original test used plain `httptest.NewServer`, omitting TLS verification and handshake.
- **Fix**: Added an optional `TLSClientConfig *tls.Config` to `transport.Config`. In production, leaving this `nil` continues to enforce default system certificate verification. In tests, the server's certificate pool (`srv.Client().Transport.(*http.Transport).TLSClientConfig`) is supplied to `transport.Config`, allowing `http.Transport` to execute a genuine TLS handshake over the dialed connection without globally disabling certificate checks or using `InsecureSkipVerify`.
- **Tests Added**:
  - `TestProbe_HTTPS204Success_TLS`: Genuine TLS negotiation against `httptest.NewTLSServer` verifying status 204, `Result.OK == true`, `store.ErrNone`, and positive latency.
  - `TestProbe_TLSFailure/UntrustedCertificateAgainstRealTLSListener`: Proves that when `TLSClientConfig` is `nil` (default production behavior), an untrusted TLS certificate is rejected and correctly classified as `store.ErrTLS`.

### P1-2 — Observable Body Non-Consumption Regression Test
- **Finding**: `TestProbe_NoBodyConsumption` relied solely on timing heuristics.
- **Fix**: Added an optional `Transport http.RoundTripper` to `transport.Config`. Implemented `TestProbe_ObservableBodyNonConsumption` using an instrumented `trackingBody` with atomic counters.
- **Verification**: Definitively proved:
  - `body.readCalls.Load() == 0` (probe never called `resp.Body.Read`)
  - `body.bytesRead.Load() == 0` (0 bytes consumed from body)
  - `body.closeCalls.Load() == 1` (`resp.Body.Close()` was called exactly once)
  - Retained `TestProbe_SocketLevelBodyNonConsumption` as an end-to-end streaming check.

### P2 — Deterministic `isGenerate204URL` Semantics
- **Finding**: Substring matching (`strings.Contains`) caused false positives for URLs like `/keygen_2048/health` or `/api/generate_204_report`.
- **Fix**: Refactored `isGenerate204URL` to parse the URL using `net/url` and test canonical path equality (`cleanPath == "/generate_204" || cleanPath == "/gen_204"`, or `DefaultHealthURL`).
- **Tests Added**:
  - `TestProbe_NonGenerate204Substrings_SucceedOn200`: Proved that URLs containing substring variations (`/keygen_2048/health`, `/api/generate_204_report`, `/metrics?query=generate_204`) returning 200 OK succeed as generic endpoints with `store.ErrNone`.
  - `TestProbe_Generate204Returning200Fails`: Confirmed that canonical `/generate_204` returning 200 OK is rejected as captive portal / proxy interception (`store.ErrProxyError`).

### P2 — Error Classification Duplication Documented
- **Finding**: `classifyDialError` duplicates network classification logic from `internal/tester/probe.go`.
- **Fix**: Documented in `probe.go` and ticket architecture notes that this is an intentional transitional duplication to keep `internal/tester/transport` a leaf package without cyclic dependencies until Ticket 19/21 unifies transport and application error classification.

### P3 — Keepalive / Connection Reuse Rationale Documented
- **Finding**: Rationale for `DisableKeepAlives: true` and `tr.CloseIdleConnections()` needed formal documentation.
- **Fix**: Added explicit comments in `probe.go`: health probes are isolated, independent network verifications that must never hold idle sockets or leak connections across proxy outbounds.

### Test Suite Execution Summary (All Passed)
1. `TestProbe_HTTPS204Success_TLS` (genuine TLS 204 success)
2. `TestProbe_HTTP204Success_PlainHTTP` (plain HTTP 204 success)
3. `TestProbe_RedirectFailure` (301, 302, 307 rejection)
4. `TestProbe_HTTP403` (403 failure)
5. `TestProbe_HTTP500` (500 failure)
6. `TestProbe_HTTP429` (429 rate limit failure)
7. `TestProbe_DialConnectionRefused` (connection refused failure)
8. `TestProbe_TLSFailure` (synthetic error & real untrusted certificate failure)
9. `TestProbe_TimeoutExceeded` (timeout bound enforcement)
10. `TestProbe_ParentCancellation` (context cancellation)
11. `TestProbe_NoRedirectFollowing_SingleRequest` (single request assertion)
12. `TestProbe_ObservableBodyNonConsumption` (0 reads, 0 bytes, 1 close)
13. `TestProbe_SocketLevelBodyNonConsumption` (streaming body socket check)
14. `TestProbe_TargetAgnostic` (AST inspection for 0 target imports/keywords)
15. `TestProbe_CustomEndpointSuccess` (/healthz 200 OK success)
16. `TestProbe_Generate204Returning200Fails` (captive portal detection)
17. `TestProbe_NonGenerate204Substrings_SucceedOn200` (path-based URL matching)
18. `TestProbe_ConcurrentExecution` (parallel execution race check)

Commands executed:
- `go test -v -race ./internal/tester/transport/...` -> PASS
- `go test -v ./internal/tester/...` -> PASS
- `make test` -> PASS
- `make build` -> PASS
- `gofmt -s -w internal/tester/transport/` -> CLEAN
- `git diff --check` -> CLEAN
