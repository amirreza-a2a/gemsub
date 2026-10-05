# Gemsub

Gemsub discovers, probes, and publishes proxy subscription links. It evaluates whether each candidate proxy can reach specific target services by running a two-stage probe pipeline: first verifying network transport, then verifying application-level service compatibility.

## Language

### Probing Pipeline

**Stage 1 (transport health)**:
The first phase of the probe pipeline. Tests whether a candidate proxy can establish a working network connection and complete an HTTP round-trip through a neutral health endpoint (e.g. `gstatic.com/generate_204`). Stage 1 has no knowledge of any target service.
_Avoid_: health check, connectivity test, ping

**Stage 2 (service probing)**:
The second phase of the probe pipeline. Tests whether a candidate proxy that passed Stage 1 can successfully reach one or more specific target services (e.g. Gemini, Claude). Stage 2 runs only after Stage 1 succeeds.
_Avoid_: application test, target test, service check

**Positive verification**:
A Stage 2 classification policy that requires affirmative evidence of service availability (e.g. the presence of a known DOM marker or brand element) before declaring a probe "passed." Without positive verification, a simple HTTP 200 is accepted as proof of availability.
_Avoid_: strict check, deep inspection

**Strict mode**:
A positive-verification policy configuration where the classifier requires affirmative evidence. A proxy returning HTTP 200 from a captive portal or transparent proxy is correctly classified as failed under strict mode.
_Avoid_: strict verification, enhanced mode

**Permissive mode**:
A positive-verification policy configuration where the classifier accepts HTTP 200 without requiring affirmative evidence. Appropriate only for services or network environments where captive-portal interception is not a realistic threat.
_Avoid_: lenient mode, basic mode

### Rule Engine

**Signal source**:
A named function that extracts a single observable fact from a probe response. Examples: HTTP status code, presence of a DOM marker, response body substring match, redirect URL path. A signal source produces a typed value; it does not decide pass/fail.
_Avoid_: check, detector, extractor

**Match operator**:
A predicate applied to the output of a signal source. Examples: `equals`, `contains`, `oneOf`, `regex`. A match operator returns true/false for a single signal.
_Avoid_: comparator, matcher, filter

**Combinator**:
A logical connector that composes multiple match results into a single boolean. Examples: `all` (AND), `any` (OR), `not`. Combinators can nest to express complex classification logic.
_Avoid_: aggregator, logic gate, composer

**Verdict**:
The terminal output of a rule evaluation: a `(Status, ErrorCategory, Reason)` triple. Each rule maps a combinator result to a verdict. The classifier evaluates rules in priority order and emits the first matching verdict.
_Avoid_: result, outcome, decision

### Store Model

**Tier 1 service**:
A target service (currently Gemini and Claude) that receives full Store treatment: per-candidate `BoundedHistory` entries, reliability scoring, ranked projections, and durable persistence. Tier 1 status exists because `BoundedHistory` is a fixed-capacity O(1) circular buffer — adding a Tier 1 service multiplies the per-candidate memory footprint linearly.
_Avoid_: primary service, core service, tracked service

**Tier 2 service**:
A user-defined target service that receives live-only Store treatment: current-cycle pass/fail status without historical tracking, scoring, or ranked projections. Tier 2 exists to allow users to add arbitrary service targets without the O(N) memory cost per candidate that Tier 1 history tracking requires.
_Avoid_: secondary service, custom service, lightweight service

**Projection**:
A filtered, possibly ranked view of candidates that satisfy a specific servability policy. `Store.Passing()` is the Gemini projection; `Store.NetworkPassing()` is the transport-health projection. Delivery layers (Publisher, Subserver) consume projections; they never independently recreate membership logic.
_Avoid_: view, subscription list, output

**BoundedHistory**:
A fixed-capacity circular buffer of `ProbeSample` entries per candidate, providing strict O(1) memory per candidate regardless of system uptime. The history is authoritative for reliability scoring and pass/fail derivation.
_Avoid_: history ring, sample buffer, probe log
