# 19: refactor(tester): isolate gemini application probe

Type: refactor
Status: ready-for-agent
Blocked by: 17

## Problem

Gemini-specific application probing logic currently pollutes the generic `internal/tester` package:
- `ClassifyResponse` in `classifier.go` contains hardcoded Google AI restricted country maps (`IR`, `RU`, etc.), regex patterns for `WIZ_global_data`, and Gemini brand markers (`bardchatui`).
- `executeAttempt` in `probe.go` attaches browser impersonation headers and downloads 1MB payloads specifically for Gemini.
This prevents adding alternative target probes (such as Claude) without modifying or entangling Gemini code.

## Scope

1. Create a dedicated Gemini probe module (recommended location: `internal/tester/gemini`):
   - Define `Config` with `URL string`, `BlockPhrases []string`, and `Timeout time.Duration`.
   - Define `Result` with `Status store.Status`, `Category store.ErrorCategory`, `StatusCode int`, `Reason string`, `Latency time.Duration`, and `Retryable bool`.
   - Define `Probe(ctx context.Context, dialFn transport.DialFunc, cfg Config) Result`.
2. Encapsulate all Gemini-specific logic inside `internal/tester/gemini`:
   - Browser headers: Chrome `User-Agent`, `Accept`, `Accept-Language`, `Sec-CH-UA`.
   - Response reading: `io.LimitReader(resp.Body, 1<<20)` (1MB cap).
   - `WIZ_global_data` country extraction (`countryCodeRegex`) and `restrictedCountries` map.
   - Explicit restriction markers (`locationBlockRegex`).
   - Block phrase evaluation on normalized body text.
   - Google server origin validation (`Server: ESF|GSE|SFFE|GWS`, `google.com` cookies).
   - Brand, `<title>`, and DOM structural markers (`bardchatui`, `google gemini`).
3. Retain shared transport helpers in `internal/tester`:
   - Keep `ClassifyDialError` in `internal/tester` for network/transport error categorization.
   - Keep `NormalizeBytes` and `NormalizeText` accessible as shared text normalization utilities.
4. Move and adapt Gemini test suites:
   - Migrate Gemini test cases from `internal/tester/classifier_test.go` into `internal/tester/gemini/probe_test.go`.
   - Ensure 100% preservation of current classification decisions across all existing HTML fixtures and test cases.

## Acceptance Criteria

- [ ] All Gemini-specific regexes, country maps, and HTML markers are completely removed from `internal/tester/classifier.go` and encapsulated inside `internal/tester/gemini`.
- [ ] `gemini.Probe` executes an isolated Gemini application check through any provided `DialFunc`.
- [ ] Current Gemini classification behavior remains 100% identical (all test cases in `classifier_test.go` pass in their new location).
- [ ] `internal/tester` contains no Gemini-specific symbols, imports, or assumptions.
- [ ] No Claude-specific code or premature multi-target abstractions are introduced.

## Explicit Non-Goals

- Do not modify or relax any Gemini classification rules.
- Do not introduce a generic `Target` interface or registry.
- Do not implement Claude probing.
- Do not wire into `probe.go:ProbeWithExecutor` yet (deferred to Ticket 21).

## Architectural Invariants Preserved

- `Target Logic Isolation`: Gemini application rules and DOM expectations reside strictly behind the Gemini target probe boundary.
- `Behavioral Equivalence`: A response classified as `ErrRegionBlocked`, `ErrTargetDenied`, or `Passed` by the old classifier produces the exact same outcome in the new Gemini module.

## Expected Files/Modules

- `internal/tester/gemini/probe.go`
- `internal/tester/gemini/probe_test.go`
- `internal/tester/classifier.go` (refactored to remove Gemini-specific logic)
- `internal/tester/classifier_test.go` (refactored to focus on shared transport classification)

## Test Requirements

- Verify all existing Gemini response classifications (`LOCATION_REJECTED`, restricted countries via `WIZ_global_data`, block phrases, valid brand markers, captive portal challenges).
- Verify status code handling (429 rate limit with `Retry-After`, 403 target denial, 503 service unavailable).
- Verify 1MB body truncation boundary.
