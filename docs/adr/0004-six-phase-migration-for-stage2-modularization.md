# Six-phase migration plan for Stage 2 modularization

The transition from hardcoded per-service classifiers to the
declarative rule engine will follow a six-phase migration sequence.
The critical ordering constraint: **Gemini migrates last**, after
Claude and any Tier 2 services have been converted and validated.

## Phases

1. **Shared classifier core.** Extract the common infrastructure
   (body download and capping, text normalization, title extraction,
   Cloudflare/captive-portal detection) from `tester/gemini/` and
   `tester/claude/` into a shared classifier module. Both existing
   classifiers continue to work unchanged — they just call the
   shared module instead of reimplementing utilities.

2. **Rule engine primitives.** Implement the signal source, match
   operator, combinator, and verdict types. Ship the engine with
   unit tests but no production wiring yet.

3. **Claude migration.** Rewrite Claude's classifier as a default
   rule set consumed by the engine. Validate with golden tests
   against the existing `claude.ClassifyResponse` outputs. Switch
   the production code path to the engine-backed classifier.
   *Amendment (Phase 2 - Gating Divergence)*: In the unified pipeline, the accepted-status
   gate (default `[200]`) evaluates *before* payload inspection (matching
   Gemini). Under legacy Claude procedural logic, payload inspection ran
   on any status before the default 200 check. Therefore, a non-common,
   non-accepted status returning a region marker (e.g. HTTP 451) diverges
   from legacy `ErrRegionBlocked` to `ErrTargetOther` ("unexpected HTTP
   status 451"). This divergence is intentional, aligns Claude with
   the unified pipeline, and will be pinned by a golden test fixture in
   Phase 3.
   *Amendment (Phase 2 - Canonical Link Deduplication)*: `claude/classifier.go`
   temporarily retains its procedural `hasCanonicalLinkRegionBlock` scanner
   alongside `classifiercore.ExtractCanonicalLink`. The implementations differ:
   the legacy scanner inspects the entire `<link>` tag byte slice for the
   region slug, whereas `classifiercore.ExtractCanonicalLink` parses HTML attributes
   and extracts only the `href` attribute value for `SignalCanonicalLink`.
   Phase 3 must delete `hasCanonicalLinkRegionBlock` from `claude/classifier.go`.
   Before deleting it, Phase 3 must add a differential test executing both
   implementations over every existing Claude response fixture (including
   `examples/stage2_inspector` and unit test fixtures) asserting identical
   classification verdicts.

4. **Tier 2 service support.** Implement the config schema
   (`test.services.<name>`) and the live-only Store treatment for
   user-defined services. Users can now add arbitrary service
   targets via configuration.

5. **Gemini migration.** Rewrite Gemini's classifier as a default
   rule set. Validate with golden tests achieving full parity with
   the existing `gemini.ClassifyResponse` / `gemini.Probe` outputs.
   Switch the production code path only after parity is confirmed.
   *Amendment (Phase 2 - Verbatim Regex Parity, Performance Gate, and Pattern Deduplication)*:
   Benchmarking against identical 1MB payloads demonstrated that when Gemini's
   regex patterns are ported VERBATIM (using `classifiercore.GeminiCountryCodePattern`
   with `RegexExtractIn` and `classifiercore.GeminiLocationBlockPattern`), Go's
   internal prefix search optimizations remain fully intact.
   An earlier synthetic benchmark that stripped quotes from
   `LOCATION_REJECTED|no_access` defeated Go's prefix scanner, artificially
   inflating latency to ~44ms.
   Therefore, Phase 5 must port the legacy regex patterns VERBATIM (never rewrite
   or simplify them). When wiring Gemini to the rule engine in Phase 5, Gemini's
   duplicate local declarations (`countryCodeRegex`, `locationBlockRegex`,
   `restrictedCountries` in `gemini/probe.go`) must be deleted in favor of the shared
   `classifiercore` exports. During Phase 2, this verbatim equality is pinned by
   an automated AST parity test (`TestGeminiPatternsParityWithLegacy` in
   `internal/tester/classifiercore`).

   **End-to-End Benchmark Comparison (1MB Payload)**:
   A fair comparison measures end-to-end classification (`BuildAndEvaluate` vs `LegacyGemini`),
   as `BuildEvaluationContext` performs payload normalization, HTML title extraction,
   and canonical link parsing:
   - **Passing payload**: `BuildAndEvaluate` ~17.16ms (2.14 MB/op, 38 allocs) vs
     `LegacyGemini` ~12.18ms (1.05 MB/op, 14 allocs) — **1.41× legacy execution time**.
     (Breakdown: rule evaluation `Evaluate` over a pre-built context takes only
     ~3.07ms, 429 B/op, 7 allocs).
   - **Worst-case non-matching payload**: `BuildAndEvaluate` ~16.05ms (2.12 MB/op, 25 allocs) vs
     `LegacyGemini` ~14.87ms (1.05 MB/op, 18 allocs) — **1.08× legacy execution time**.
     (Breakdown: rule evaluation `Evaluate` over a pre-built context takes only
     ~4.96ms, 287 B/op, 7 allocs).
   - **Early-matching rejection (first 100 bytes)**: `BuildAndEvaluate` ~13.47ms (2.10 MB/op, 15 allocs) vs
     `LegacyGemini` ~0.41ms (2 B/op, 0 allocs) — **33× legacy execution time**.
     (Breakdown: rule evaluation `Evaluate` over a pre-built context takes only
     ~2.26ms, 48 B/op, 2 allocs).

   **Evaluation of Lazy Normalization and Architectural Trade-off**:
   The 33× latency gap on early-matching responses was analyzed to determine if lazy
   normalization in `BuildEvaluationContext` (e.g. normalizing only on first access)
   should be introduced.
   Lazy normalization was **rejected** for the following architectural reasons:
   1. The pipeline enforces an intentional security ordering:
      `Status Codes → Interception Preemption → Negative Rejections → Positive Verifications`.
      On any HTTP 200 response, Phase 3 (Interception Preemption) executes
      `DetectInterception(ctx.NormalizedBody)`. Because interception preemption
      must run before Phase 4 rejections to prevent spoofed/captive-portal region blocks,
      `NormalizedBody` is unconditionally accessed on every 200 OK response.
      Thus, lazy normalization does not eliminate the 13ms normalization cost for
      HTTP 200 early-rejection payloads. The legacy 0.41ms fast-path was an artifact
      of organic ordering that evaluated location regexes prior to captive-portal checks.
   2. Eager normalization keeps `EvaluationContext` a clean, immutable, and thread-safe
      data struct. Introducing lazy evaluation would require synchronization (such as
      `sync.Once` or mutexes) across concurrent rule evaluations or signal readers,
      adding overhead to all reads.
   3. In production, early-rejection payloads are rare in healthy proxy pools; the 13ms
      normalization cost is only paid when an early-rejection occurs.

   **Phase 5 Performance Gate**:
   Phase 5 must enforce ONE defensible end-to-end performance gate on `BuildAndEvaluate`:
   - **Absolute ceiling**: ≤ 25 ms/op on 1MB payloads.
   - **Relative ceiling**: ≤ 1.5× legacy execution time on full evaluation paths
     (passing and worst-case non-matching).
   *Justification*: Probe operations are network-bound across remote proxies and take
   several seconds (typically 200ms–2000ms). An end-to-end CPU evaluation latency of
   ~17ms represents < 2% of the overall probe cycle time, presenting zero throughput
   bottleneck while delivering strict interception safety and rule engine modularity.

6. **Legacy cleanup.** Remove the old classifier functions, collapse
   `test.target_url` / `test.block_phrases` / `test.claude` legacy
   config fields into the unified `test.services` schema, and update
   documentation.

## Why Gemini migrates last

Gemini is the battle-tested core of the system. The entire existing
user base depends on Gemini classification working correctly — a
false positive (declaring a captive-portal response as "Gemini
available") silently degrades every subscription consumer.

A silent regression in Gemini classification is the highest
operational risk in this project. By migrating Claude first (phase 3)
and Tier 2 services second (phase 4), we:

- Validate the rule engine against a real, already-deployed classifier
  (Claude) before touching Gemini.
- Catch engine bugs in a lower-risk context: Claude classification
  errors affect only Claude subscriptions, not the primary Gemini
  subscription that most users consume.
- Build golden-test coverage incrementally, so by the time Gemini
  migrates (phase 5), the engine has already been proven against
  production traffic patterns.

## Consequences

- Phases 1–4 can proceed without affecting Gemini classification
  at all. The existing `gemini.Probe` / `gemini.ClassifyResponse`
  code path remains untouched until phase 5.
- Phase 5 is gated on golden-test parity — it cannot proceed until
  every known Gemini response pattern (region block, block phrases,
  WIZ_global_data, location rejection, Cloudflare challenges,
  captive portals, DOM markers) produces identical verdicts from
  both the old function and the new rule set.
  *Amendment (Phase 2)*: The rule engine enforces an **interception-first**
  evaluation order across all services (`Status Codes → Interception Preemption → Negative Rejections → Positive Verifications`).
  For Gemini, this represents a deliberate, intentional behavior change
  from the legacy organic ordering (where region block phrases ran before
  interception detection). A captive portal or Cloudflare challenge is
  not authoritative about geographical region; if a response triggers
  both, the engine emits `ErrTargetOther` (interception) rather than
  `ErrRegionBlocked`. This behavior will be pinned by an explicit golden
  test fixture in Phase 5.
- The migration is not atomic. During phases 3–5, the codebase will
  contain both engine-backed and function-backed classifiers
  simultaneously. This is intentional: incremental migration reduces
  blast radius.

### Amendment (Phase 4 - Scope Formalization, Probe Orchestrator, Shared Fetcher, Dynamic Delivery, and Safety Invariants)

Phase 4 formalizes the complete infrastructure required for user-defined Stage 2 services, assigning ownership of components previously unassigned:

1. **Shared HTTP Fetcher (`internal/tester/stage2fetch`)**:
   Extracts common HTTP client construction, dialer binding (`transport.DialFunc`), Chrome 131 browser impersonation headers, payload capping, and latency clamping.
   - Per-service body cap is configurable under `test.services.<name>.max_body_bytes` (default: **256 KB**, ceiling: **1 MB**).
   - In strict mode, if target verification markers sit beyond the configured cap, evaluation fails closed (`ErrTargetOther`, `"possible interception / false positive: positive verification signals absent"`).

2. **Probe Orchestrator & Isolated Tier 2 Semaphore (`internal/tester/probe.go`)**:
   - Generalizes `executeAttempt` to fan out across a slice of `ServiceProbe` implementations (`geminiProbe`, `claudeProbe`, and `ruleEngineProbe`).
   - Decouples `failedServicesResult` to accept `serviceNames []string`.
   - Enforces a dedicated `tier2Sem` concurrency semaphore of **40** applying *strictly to Tier 2 probes*. Tier 1 probes (Gemini and Claude) are never queued behind the semaphore.
   - Enforces an explicit `tier2Budget` (default: **5s**, range: 1s..10s) derived from the cycle's parent `ctx` (never `context.Background()`), guaranteeing shutdown/cancel propagation. Config validation requires that each service timeout satisfies `timeout <= tier2Budget`.
   - Permit acquisition timeout is bounded (e.g. 1s). If permits are exhausted, Tier 2 aborts and is omitted from `Result.Services` ("not measured"), leaving store hysteresis completely untouched.
   - Peak transient in-flight memory is estimated at **~22 MB** (down from 856 MB), to be confirmed by benchmark in Ticket 2.
   - Top-level `Result.Status`, `Category`, `Score`, and attempt retries remain strictly Gemini-derived throughout Phase 4. Because Tier 1 and Tier 2 execute concurrently in parallel goroutines within the attempt, candidate attempt duration is bounded by $\max(\text{Tier1Latency}, \text{tier2Budget})$ (subject to the outer attempt `cfg.Timeout`).

3. **Transport-Only Store Ingestion & Delivery for `skip_stage2`**:
   - When `skip_stage2: true`, `Store` executes a transport-only ingestion path updating transport evidence and real status codes without pushing samples to `rec.History`. `Store.Passing()` returns empty; `/gemini` and `/claude` return 503.
   - Subserver base route (`/` or `/sub`) automatically serves `"generic"` (`Store.NetworkPassing()`) under `skip_stage2`, preventing client disruption.
   - `meta.json` records `"stage2_skipped": true` and per-projection `generated_at` timestamps to make staleness machine-readable.
   - `config.Validate()` rejects `services.gemini.enabled = false` while Stage 2 is active.

4. **One-Way Config Hydration, Cloning, and Safety Limits (`internal/config`)**:
   - Service names enforce `^[a-z0-9][a-z0-9-]{0,31}$` and reject reserved names, including Windows device names (`con`, `prn`, `aux`, `nul`, `com1`–`com9`, `lpt1`–`lpt9`) and internal routes (`generic`, `gemini`, `claude`, `healthz`, `sub`, `metrics`, `api`, `all`, `vless`, `vmess`, `trojan`, `meta`, `git`).
   - Strict mode requires at least one verification rule; fails fast otherwise.
   - Hydration resolves into `Test.ResolvedServices` (`json:"-"`). Legacy fields are not written back to JSON.
   - `TestConfig.Clone()` deep-copies raw mutable maps (`Services`, `ResolvedServices`, `AcceptedStatusCodes`, `Headers`, `Rules`), while compiled regex and rule trees remain immutable shared instances.
   - Resource limits: Maximum of 8 user-defined services, 1MB max body cap, and timeouts bounded between 1s and 30s.

5. **Dynamic Delivery, Readiness Gating, and Pruning Safety (`publisher` & `subserver`)**:
   - **Readiness Invariant**: A service is not ready until `loadedFromSnapshot || completedCyclesThisRun >= 1`.
   - **Publisher**: Emits `<service>/*.txt` and tracks created directories in `meta.json` `"managed_services"`. Skips publishing any service where `ready == false`, never committing empty lists due to cold start.
   - **Safe Pruning**: By default (`prune_removed_services: false`), removed service directories are left untouched on disk and marked deprecated in `meta.json`. When pruning is enabled, removal requires 5 consecutive absent cycles (tracked strictly in local store state, never creating intermediate churn commits in `meta.json`) before deleting files with explicit path checking.
   - **Subserver**: Dynamically routes `<base>/<service>` and `?projection=<service>`. Returns `HTTP 503 Service Unavailable` with `Retry-After: 30` during cold start, and `HTTP 404` for decommissioned services.
