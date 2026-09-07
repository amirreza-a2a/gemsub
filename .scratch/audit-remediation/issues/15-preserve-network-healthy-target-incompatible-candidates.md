# 15: audit(pipeline): preserve network-healthy and target-incompatible candidates

Type: audit
Status: resolved
Blocked by: None (can start immediately)

## Question

If a candidate passes network/proxy health but fails Gemini-specific probing, does the current Source -> Scheduler -> Tester -> Store -> Publisher -> Subserver pipeline retain that candidate, or discard it?

## Answer Summary (Conclusion B)

The current pipeline **physically retains** the candidate in `Store` (`CandidateRecord` persisted in state file), but **semantically collapses** target incompatibility into generic failure (`StatusFailed`).

Key findings:
1. **Source & Scheduler**: Retains and parses all valid links. Does not discard candidates based on target compatibility.
2. **Tester**: Clearly separates network connectivity from target inspection in execution (`executeAttempt` only calls `ClassifyResponse` after proxy dial and HTTP round-trip succeed), but collapses target rejections (`ErrRegionBlocked`, `ErrTargetDenied`) into `Status: store.StatusFailed`.
3. **Store**: Persists the record and all bounded samples in `rec.History`. However, `Gate 2 (Target Policy Override)` in `servabilityGateLocked()` unconditionally disqualifies candidates with `ErrRegionBlocked` or `ErrTargetDenied`, and `rec.HasPassed` remains false.
4. **Publisher & Subserver**: Both consume `Store.Passing()`, which filters strictly on servability. Neither exposes target-incompatible nodes, treating them as globally unusable.

## Required investigation

1. Trace candidate lifecycle through the actual code.
2. Identify the exact point(s) where Gemini failure causes retention, rejection, exclusion, or publication filtering.
3. Distinguish:
   - proxy/network health
   - target compatibility
   - servability for a particular target
4. Determine whether the current Store model already retains enough information to reuse such candidates for other targets.
5. Determine whether Publisher currently filters them out globally or only for the Gemini-oriented published subscription.
6. Propose the minimum future-safe architecture needed to preserve these candidates without prematurely introducing a generic Target abstraction.

## Desired invariant

Gemini failure must not imply that the underlying network configuration is globally dead.

## Acceptance criteria

- [x] Trace candidate lifecycle through Source -> Scheduler -> Tester -> Store -> Publisher -> Subserver in code.
- [x] Identify exact point(s) where Gemini failure causes retention, rejection, exclusion, or publication filtering.
- [x] Document explicit distinction between proxy/network health, target compatibility, and servability for a target.
- [x] Assess whether current Store model retains enough information to reuse candidates for other targets.
- [x] Determine whether Publisher filters candidates globally or only for Gemini-oriented published subscription.
- [x] Deliver minimal future-safe architecture recommendation and migration implications without redesigning unrelated reliability logic.

## Detailed Audit Findings

### 1. Source -> Scheduler
- `internal/source/fetch.go:22-51` (`FetchAll`): Fetches and deduplicates raw links across upstream sources.
- `internal/scheduler/scheduler.go:120-138` (`runCycle`): Iterates over fetched links, parses candidates with `parser.Parse(link)`. Candidates with malformed link syntax are skipped. All valid candidates are loaded into `linkSet` and tracked in `Store.StartCycle(linkSet)`.

### 2. Scheduler -> Tester
- `internal/tester/probe.go:118-168` (`executeAttempt`):
  - Step 1: Dials target through sing-box outbound (`buildDialer`). Network errors (refused, TLS, timeout, reset) trigger `ClassifyDialError` (`ErrConnRefused`, `ErrTLS`, `ErrTimeout`, `ErrReset`, `ErrProxyError`).
  - Step 2: If proxy connection succeeds and HTTP exchange completes (`client.Do(req)` succeeds), proxy network health is proven.
  - Step 3: `ClassifyResponse` (`classifier.go:431-609`) inspects payload. Regional restriction (WIZ_global_data, block phrases) produces `Status: StatusFailed, Category: ErrRegionBlocked`. 403 Forbidden produces `Status: StatusFailed, Category: ErrTargetDenied`.
  - Semantic Loss: Even though proxy transport succeeded, `Status` is set to `StatusFailed`.

### 3. Tester -> Store
- `internal/store/store.go:463-528` (`PutWithTransition`):
  - Every tested candidate is upserted into `s.records[canonical]`.
  - Samples are appended to `rec.History` (`BoundedHistory`).
  - Retention: Candidates are NEVER deleted upon failure; eviction only happens in `FinishCycle` if absent from sources for `> MaxAbsentCycles` (`store.go:575`).
  - Degradation:
    - `rec.HasPassed` (`store.go:508-514`) is derived strictly from `History.LastPassedLatency()`. Without `StatusPassed`, `HasPassed` remains `false`.
    - `rec.Score` decays to 0 because `CategoryWeights[ErrRegionBlocked]` defaults to 0.0.
    - `servabilityGateLocked` (`store.go:151-159` Gate 2) hard-disqualifies the candidate if the latest conclusive sample is `ErrRegionBlocked` or `ErrTargetDenied`.

### 4. Store -> Publisher
- `internal/publisher/publisher.go:100` (`RenderSubscriptionFiles`):
  - Queries `p.st.Passing()`, which returns candidates satisfying `isServableRecordLocked(rec)`.
  - Publisher publishes `all.txt`, `vless.txt`, `vmess.txt`, and `trojan.txt`.
  - Target-incompatible candidates are excluded at this query boundary.

### 5. Publisher -> Subserver
- `internal/subserver/server.go:68` (`handleSub`):
  - Also queries `s.st.Passing()`.
  - Both publication surfaces strictly serve Gemini-passing candidates. Target-incompatible candidates are completely omitted.

### 6. Architecture Recommendation
- Recommendation: **Conclusion B**.
- Minimum future-safe architecture:
  - Add explicit boolean or category discriminator separating `TransportHealth` (network dial success) from `TargetStatus`.
  - Introduce target-scoped or network-health query projection on Store (e.g. `NetworkPassing()` or target-aware filter) while preserving default `Passing()` for Gemini backward compatibility.
  - Do not redesign the BoundedHistory circular buffer or Store reliability model.
