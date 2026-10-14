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
