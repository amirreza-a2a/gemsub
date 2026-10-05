# Two-tier Store model for service targets

The Store will distinguish between **Tier 1** and **Tier 2** service
targets, providing different levels of state management to each.

**Tier 1** (Gemini, Claude): full `BoundedHistory` per candidate,
reliability scoring, ranked projections, and durable persistence.

**Tier 2** (user-defined services): current-cycle pass/fail status
only, without per-candidate history, scoring, or ranked projections.

## Why the split exists

`BoundedHistory` is a fixed-capacity circular buffer
(`internal/store/history.go`) that provides strict O(1) memory per
candidate. Each `ProbeSample` in the buffer contains the full probe
outcome including per-service `ClaudeStatus`, `ClaudeCategory`,
`ClaudeLatency`, transport evidence, and jitter. For N candidates,
memory usage is O(N × capacity × sizeof(ProbeSample)).

When issue #35 generalized the Store to support per-service
projections, benchmarks measuring the addition of the `Services
map[string]TargetResult` field to `CandidateRecord` showed:

- **3.9× increase in memory consumption** per record (from the map
  header, bucket array, and per-entry overhead).
- **21× increase in allocations** per record snapshot operation (from
  map initialization and deep-copy in `CandidateRecord.Clone()`).

These numbers are for a *single* additional map field. If every
user-defined service received its own `BoundedHistory` entry (a
second circular buffer per service per candidate), the memory
multiplier would be even larger. For an operator running 20,000+
candidates — a normal operating scale — giving every user-added
service full history tracking would break the O(1)-per-candidate
memory invariant that `BoundedHistory` was designed to enforce.

The two-tier model preserves the memory invariant:

- Tier 1 services are baked into the `ProbeSample` struct fields
  (e.g. `ClaudeStatus`, `ClaudeLatency`) — they add a constant
  per-sample cost, independent of the number of services.
- Tier 2 services use the `Services map[string]TargetResult` on
  `CandidateRecord` for current-cycle status only. No history buffer,
  no scoring, no persistence overhead.

## Considered options

1. **Flat model: every service gets full history.** Rejected because
   of the benchmark evidence above. The map-per-record cost is already
   measurable at 3.9× memory; adding a per-service `BoundedHistory`
   would be worse.

2. **Two-tier model (chosen).** Tier 1 services get full treatment
   because they are the project's core targets and the data is needed
   for scoring-based ranked subscriptions. Tier 2 services get
   live-only treatment, which is sufficient for binary "is this
   service reachable through this proxy right now?" questions.

3. **Configurable tier promotion.** Allow users to promote a Tier 2
   service to Tier 1 via config. Deferred: this is possible as a
   future extension but adds implementation complexity (dynamic
   `ProbeSample` field allocation or per-service history slices) that
   isn't justified until a user actually needs historical scoring for
   a custom service.

## Consequences

- User-defined Tier 2 services cannot provide ranked subscription
  output (there is no scoring data to rank by). Their projections are
  unranked pass/fail lists.
- Promoting a service from Tier 2 to Tier 1 requires a code change
  (adding fields to `ProbeSample` and `CandidateRecord`), not just
  a config change. This is intentional: Tier 1 status carries a
  permanent per-candidate memory cost.
- Anyone proposing to change this split should first reproduce the
  benchmarks from issue #35's analysis (commit `690509b`) and verify
  that the memory cost is acceptable at the target candidate scale.
