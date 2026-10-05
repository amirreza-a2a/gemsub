# Declarative rule engine for Stage 2 service classification

We will replace the current hardcoded Go classifier functions
(`gemini.ClassifyResponse`, `claude.ClassifyResponse`, and the
per-service switch statements in `tester/probe.go`) with a composable,
declarative rule engine built from four primitives: **signal sources**,
**match operators**, **combinators**, and **verdicts**.

Each service's classification logic will be expressed as an ordered list
of rules rather than an imperative function. The engine evaluates rules
top-to-bottom and emits the first matching verdict.

## Considered options

1. **Continue with per-service Go functions.** Every new target service
   requires a new Go file with hand-written response inspection logic.
   The Gemini classifier is already ~350 lines; Claude's is ~200 lines.
   Adding a third service (or letting users define their own targets)
   would mean duplicating pattern-matching boilerplate, and every
   classification change requires a recompile.

2. **Declarative rule engine (chosen).** Classification rules are data:
   signal sources extract observables from probe responses, match
   operators test individual signals, combinators compose boolean
   results, and verdicts map composite matches to `(Status,
   ErrorCategory, Reason)` triples. The built-in Gemini and Claude
   classifiers ship as default rule sets. User-defined Tier 2 services
   configure their own rules through `test.services.<name>.rules` in
   the config file.

3. **External plugin / scripting.** Embedding Lua, Wasm, or a similar
   runtime for classification logic. Rejected as disproportionate
   complexity for the actual problem: the signal sources and operators
   needed are finite and well-understood (HTTP status, body substring,
   redirect path, DOM marker presence).

We chose option 2 because:

- It enables user-defined Tier 2 services without recompilation.
- The shared classifier core remains a single well-tested Go module
  that the built-in Gemini and Claude rule sets also use, preventing
  the existing pattern where each service reimplements `NormalizeText`,
  body-length capping, and title extraction independently.
- Classification logic becomes auditable data rather than opaque
  procedural code, which matters for a tool operating in adversarial
  network conditions where false positives have operational cost.

## Consequences

- The existing `gemini.ClassifyResponse` and `claude.ClassifyResponse`
  functions will be rewritten as default rule sets consumed by the
  shared engine. This is a breaking internal refactor but not a
  user-facing API change.
- Golden tests for the built-in classifiers must achieve parity before
  the old functions are removed.
- The rule engine must support the full expressiveness of the current
  Gemini classifier, including nested conditions (e.g. "if body
  contains X *and* URL path contains Y, then region-blocked").
  Combinators provide this via nesting.
