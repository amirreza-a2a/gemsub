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
   *Amendment (Phase 2 - Verbatim Regex Parity and 1MB Performance Gate)*:
   Benchmarking against identical 1MB payloads demonstrated that when Gemini's
   regex patterns are ported VERBATIM (using `classifiercore.GeminiCountryCodePattern`
   with `RegexExtractIn` and `classifiercore.GeminiLocationBlockPattern`), Go's
   internal prefix search optimizations remain fully intact: `Evaluate` runs in
   ~3.0ms (4x faster than legacy Gemini's ~12.3ms, which re-normalizes every probe),
   allocating only 214 B/op vs 1.05 MB/op. An earlier synthetic benchmark that
   stripped quotes from `LOCATION_REJECTED|no_access` defeated Go's prefix scanner,
   artificially inflating latency to ~44ms.
   Therefore, Phase 5 must port the legacy regex patterns VERBATIM (never rewrite
   or simplify them). Furthermore, the Phase 5 parity suite must include this 1MB
   benchmark with an explicit performance gate: `Evaluate` on a pre-built
   `EvaluationContext` must execute within 50% of legacy latency (≤ 0.5× legacy
   ns/op), and end-to-end `BuildAndEvaluate` must execute within 40% of legacy
   latency across all payload variants (passing, worst-case non-matching, and
   early-matching).

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
