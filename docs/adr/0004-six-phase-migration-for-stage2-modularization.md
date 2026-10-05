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

4. **Tier 2 service support.** Implement the config schema
   (`test.services.<name>`) and the live-only Store treatment for
   user-defined services. Users can now add arbitrary service
   targets via configuration.

5. **Gemini migration.** Rewrite Gemini's classifier as a default
   rule set. Validate with golden tests achieving full parity with
   the existing `gemini.ClassifyResponse` / `gemini.Probe` outputs.
   Switch the production code path only after parity is confirmed.

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
- The migration is not atomic. During phases 3–5, the codebase will
  contain both engine-backed and function-backed classifiers
  simultaneously. This is intentional: incremental migration reduces
  blast radius.
