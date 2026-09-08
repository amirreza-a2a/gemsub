## Destination

Harden the `gemsub` core engine by resolving all critical blockers, concurrency hazards, and architectural bottlenecks identified during the architectural audit, establishing a stable foundation for Reliability Scoring and the Terminal UI.

## Notes

- Domain: VPN proxy testing daemon and subscription server
- Execution mode: Tickets in this map carry implementation
- Tracker convention: Local markdown in `.scratch/audit-remediation/`

## Decisions so far

- [01: fix(scheduler): prevent store wipe on empty fetch](issues/01-prevent-store-wipe-on-empty-fetch.md): Abort runCycle when fetch yields 0 links and errors occurred, preventing store wiping and empty publication.
- [02: fix(tester): discard cancelled probe results](issues/02-discard-cancelled-probe-results.md): Discard probe outcomes when parent context is cancelled, preventing false inconclusive counter increments and store corruption.
- [03: fix(subserver): add context and graceful shutdown](issues/03-add-context-and-graceful-shutdown.md): Add context lifecycle and 5-second graceful shutdown draining to subserver.Server, eliminating socket and goroutine leaks.
- [04: build: enforce with_utls tags and clean config defaults](issues/04-enforce-with-utls-tags-and-clean-config-defaults.md): Provide root Makefile enforcing with_utls compilation and eliminate hardcoded personal repository fallbacks across config and publisher.
- [05: feat(source): add context support to FetchAll](issues/05-add-context-support-to-fetchall.md): Propagate context through FetchAll and fetchOne with interruptible retry sleep and in-flight request cancellation.
- [06: perf(classifier): optimize NormalizeText memory allocations](issues/06-optimize-normalizetext-memory-allocations.md): Replace word-token array allocations with streaming byte-level scanning, bounded document inspection, and zero-allocation entity decoding.
- [07: refactor(logging): decouple logging with log/slog](issues/07-decouple-logging-with-slog.md): Replace legacy logging with log/slog across engine packages and implement in-memory RingLogHandler for UI mode alongside timestamped standard handler for headless mode.
- [08: feat(events): implement event bus for scheduler and tester](issues/08-implement-event-bus.md): Implement thread-safe EventBus emitting typed progress and lifecycle events without blocking probe loop.
- [09: feat(store): implement reliability scoring engine with bounded history](issues/09-implement-reliability-scoring-engine.md): Implement bounded history circular buffer, recency-weighted scoring, four servability policy gates, absence lifecycle, and V2 persistence.
- [10: fix(store): remediate reliability scoring engine defects](issues/10-remediate-reliability-scoring-engine-defects.md): Remediate observation fabrication in legacy migration, latency ranking preservation, config aliasing, IPv6 formatting, and persistence invariants. Note on ranking policy: `PassingRanked()` prioritizes proven candidates (`HasPassed == true`) ahead of unproven ones (`HasPassed == false`) before comparing Score and Latency, preventing unproven candidates from ranking above proven ones.
- [11: fix(store): remediate history repair, capacity clamping, and cycle transactions](issues/11-remediate-v2-history-repair-and-cycle-transactions.md): Remove automatic history repair heuristic from normal Load, clamp persisted history capacity to configured maximum, and make cycle absence transitions fully transactional across StartCycle and FinishCycle.
- [12: fix(store): derive HasPassed and LastPassedLatency from authoritative history](issues/12-derive-has-passed-from-authoritative-history.md): Keep HasPassed and LastPassedLatency dynamically derived from BoundedHistory; reset when passes are evicted from active window and recompute on Load.
- [13: fix(tui): current-cycle metrics in header](issues/13-tui-current-cycle-metrics.md): Derive Pass/Fail/Incon header metrics from cycle-local lifecycle/progress events, resetting at CycleStarted and finalizing at CycleFinished, while preserving authoritative Store aggregate statistics and revision-driven projection integrity.
- [14: feat(tui): portable country flag rendering](issues/14-portable-country-flag-rendering.md): Provide portable fallback rendering for country flags on terminals without emoji support using auto, unicode, and ascii presentation modes.
- [15: audit(pipeline): preserve network-healthy and target-incompatible candidates](issues/15-preserve-network-healthy-target-incompatible-candidates.md): Trace candidate lifecycle and identify semantic collapse where Gemini target-incompatible nodes are physically retained but excluded from query projections.
- [16: feat(store): preserve and project network-healthy candidates](issues/16-preserve-network-healthy-candidates.md): Preserve and project network-healthy candidates that pass proxy transport but fail Gemini-specific verification without breaking Gemini servability or default publications.
- [17: feat(config): separate transport and gemini probe configuration](issues/17-separate-transport-and-gemini-probe-configuration.md): Separate generic transport settings from Gemini application settings, adding namespaced `gemini` config and `health_url` with transparent backward compatibility.

## Active issues

- [18: feat(tester): standalone transport health probe module](issues/18-standalone-transport-health-probe-module.md): Create an un-opinionated HTTPS transport health check (defaulting to `https://www.gstatic.com/generate_204`) with redirect disabling and zero body reading.
- [19: refactor(tester): isolate gemini application probe](issues/19-isolate-gemini-application-probe.md): Extract Gemini-specific classification, browser headers, WIZ_global_data, and brand verification into an isolated target probe module.
- [20: feat(store): integrate explicit in-memory transport evidence](issues/20-integrate-explicit-in-memory-transport-evidence.md): Add explicit `TransportOK` and `TransportLatency` to in-memory `store.Result` and prioritize explicit evidence in `networkHealthyStateLocked` with legacy fallback.
- [21: feat(tester): two-stage probe orchestrator](issues/21-two-stage-probe-orchestrator.md): Implement sequential two-stage probe execution with a single shared Box/dialer, early exit on transport failure, bounded health sub-timeout, and candidate-level retries.
- [22: test(tester): two-stage probe integration and invariant verification](issues/22-two-stage-probe-integration-and-invariant-verification.md): End-to-end integration and regression test suite verifying state matrix A–H, invariant preservation, and configuration compatibility.

## Not yet specified

- Candidate Deduplication: Upstream IP/Port grouping and round-robin worker dispatching

## Out of scope

- Subscribing to non-standard protocols outside vless/vmess/trojan/ss
- External Web UI dashboard
