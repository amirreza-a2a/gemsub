# Implementation Readiness Review — Tickets 27, 28, 29

Date: 2026-09-09
Reviewer: Post-Ticket-26 Architecture Review
Status: Pre-implementation readiness gate

---

## Ticket 27 — `perf(tui): virtualize candidate table rendering and eliminate full-store snapshot thrashing`

### A. Readiness: **READY WITH SPEC CHANGES**

### B. Ambiguities / Blockers

#### B1. Cross-boundary Store API requirement (BLOCKER)

The ticket scope restricts changes to `internal/tui/` only (spec lines 57–61), but the
root cause of the allocation thrashing is `Store.Snapshots()` which deep-clones every
`CandidateRecord` including its `BoundedHistory.Samples` backing slice on every call:

```go
// store.go:962-979
func (s *Store) Snapshots() []CandidateSnapshot {
    ...
    out = append(out, CandidateSnapshot{
        Record: rec.Clone(),   // ← deep copy of all 56,799 records
        ...
    })
}
```

Source: [`store.go:962-979`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L962-L979)

The TUI adapter calls `Snapshots()` in two places:

1. **Hot path** — [`adapter.go:290`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go#L290) in `CandidateRows()`: called on every dirty tick (~10 Hz).
2. **Cold path** — [`adapter.go:187`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go#L187) in `reconcileCycleStateLocked()`: called only on missed `CycleFinished` events (rare).

**The hot path cannot be fixed within `internal/tui/` alone.** The adapter needs to sort
all ~56,799 candidates to determine viewport position, but sorting requires knowing
tier (Servable, NetworkHealthy), Score, HasPassed, and LastPassedLatency for every
candidate. The current Store API provides this only through `Snapshots()` (full clone)
or individual `GetRecord()` calls (also clones, and O(N) lookups would be worse).

**Two viable approaches, both requiring a minimal Store-side addition:**

| Approach | Store change | TUI change |
|:---|:---|:---|
| A. Lightweight index method | Add `Store.CandidateIndex() []CandidateSortKey` returning ~40-byte structs (link, servable, networkHealthy, score, hasPassed, latency) under RLock without cloning BoundedHistory | Adapter sorts the lightweight index at 1 Hz, materializes only viewport rows via `GetRecord()` |
| B. Throttle-only (no Store change) | None | Throttle `Snapshots()` calls to 1 Hz, cache sorted index, materialize only viewport viewmodels |

Approach A achieves >99% allocation reduction. Approach B achieves ~90% (10x frequency
reduction) but still allocates ~80 MB per second at 1 Hz.

**Recommendation:** The ticket should explicitly permit adding one new read-only Store
method that returns a lightweight sortable index. This is a non-breaking addition that
preserves Store as the sole authority. It does not move domain semantics into TUI.

#### B2. reconcileCycleStateLocked also calls Snapshots()

[`adapter.go:187`](file:///home/amirreza-a2a/gemsub/internal/tui/adapter/adapter.go#L187)
calls `Snapshots()` during cycle reconciliation. This cold path should also be converted
to use `Store.Stats()` and per-record queries, or the lightweight index.

#### B3. Acceptance criteria phrasing

Spec lines 73-74:
> "Heap allocations per TUI refresh tick are reduced by at least 95%"
> "TUI CPU usage remains low (< 5% of a core)"

These are proportional/relative and reasonable, but should be explicitly stated as
**benchmark-verified** against the reference 56k dataset, not unconditional CI gates.
An implementation on a 100-candidate test dataset cannot meaningfully verify a 95%
allocation reduction claim.

### C. Recommended Wording Changes

1. **Scope expansion** — Add to "Exact Scope":
   ```
   - `internal/store/store.go` (one new read-only method only: lightweight candidate
     index for presentation sorting; no changes to domain logic, scoring, projections,
     or persistence)
   ```

2. **Acceptance criteria qualification** — Change lines 73-74 to:
   ```
   - [ ] Heap allocations per TUI refresh tick are reduced by at least 95% under a 50k+
         candidate dataset, verified by benchmark.
   - [ ] TUI CPU usage remains below 5% of a core during active probing cycles under
         50k+ candidates, verified by benchmark.
   ```

3. **reconcileCycleStateLocked** — Add to "Desired Behavior" or implementation notes:
   ```
   - The `reconcileCycleStateLocked` cold path must also be converted to avoid full
     Store.Snapshots() cloning (use Store.Stats() and targeted per-record queries).
   ```

4. **Cursor invariants** — The existing `applySnapshot()` cursor clamping logic
   ([`model.go:312-346`](file:///home/amirreza-a2a/gemsub/internal/tui/model.go#L312-L346))
   must be preserved in the virtualized path. Add explicit acceptance criterion:
   ```
   - [ ] Cursor position is clamped correctly when the filtered candidate count changes
         between 1 Hz index rebuilds.
   ```

### D. Scope Change

**Expand** — Add `internal/store/store.go` as a permitted modification target, limited
to one new read-only method.

### E. Can Proceed After Corrections?

**Yes.** After adding the Store scope expansion and acceptance criteria qualifications,
the ticket can proceed directly to `/implement`.

---

## Ticket 28 — `perf(store): compact and compress durable state persistence`

### A. Readiness: **READY WITH SPEC CHANGES**

### B. Ambiguities / Blockers

#### B1. File naming ambiguity (AMBIGUITY — must resolve before implementation)

Spec line 49:
> "The primary persistence path writes `.json.gz` (or transparently compressed `.json`)."

This "or" is unresolved. Two interpretations:

| Strategy | File on disk | Pro | Con |
|:---|:---|:---|:---|
| A. New extension | `gemsub_state.json.gz` | File extension matches content; `file` / `zcat` work out of the box | `Store.path` config field needs derived `.gz` sibling logic |
| B. Same filename | `gemsub_state.json` (but gzip content) | No config change needed | Extension lies about content; confusing for operators |

**Recommendation:** Strategy A — write to `<path>.gz` (derived from configured path).

- Save always writes `<path>.gz` via temp-file + fsync + rename.
- Load tries `<path>.gz` first; falls back to `<path>` (legacy).
- Detect gzip by magic bytes `0x1f 0x8b` regardless of extension for robustness.
- Do NOT auto-rename or delete the legacy file. Log an informational message:
  `"legacy uncompressed state file exists; can be removed after verifying compressed state"`

#### B2. Legacy file lifecycle (AMBIGUITY — must resolve)

The spec says "Existing installations upgrade seamlessly on first load and re-save in
compact compressed format" (line 52) but does not specify what happens to the legacy
`gemsub_state.json` after the first compressed save.

**Recommendation:** Do NOT auto-delete or auto-rename the legacy file. Reasons:
- If the new compressed format has a serialization bug, the legacy file is the only backup.
- Operators can manually delete after verifying the compressed state loads correctly.
- Auto-rename during Load is unsafe because the new Save hasn't been proven yet.

#### B3. `Latest.Link` deduplication safety (AMBIGUITY — verify before implementation)

Spec line 43:
> "Avoid serializing duplicate URL strings (`Latest.Link` can default to `ActiveLink` on load)."

`Latest` is of type [`Result`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L54-L70)
and its `Link` field is the raw probe URL. `ActiveLink` on
[`CandidateRecord`](file:///home/amirreza-a2a/gemsub/internal/store/record.go#L6-L15)
is the current active link. These are usually identical but are NOT guaranteed identical:

- `ActiveLink` is updated by `PutWithTransition` when a new raw link maps to the same
  canonical link but has different query parameters or casing.
- `Latest.Link` is the exact link string from the most recent probe result.

**If they can differ, omitting `Latest.Link` loses information.** The implementation must
verify whether `Latest.Link == ActiveLink` holds as an invariant, or conditionally omit
(`json:"link,omitempty"` + set to empty string when equal to `ActiveLink`, restore on load).

**Recommendation:** Use conditional omission: serialize `Latest.Link` as `omitempty`,
clear it before serialization if equal to `ActiveLink`, and restore it on Load:
```go
if rec.Latest.Link == "" {
    rec.Latest.Link = rec.ActiveLink
}
```

#### B4. BoundedHistory serialization change

The spec says to serialize only `samples[:count]` (spec line 39). Currently,
[`BoundedHistory`](file:///home/amirreza-a2a/gemsub/internal/store/history.go#L22-L28)
serializes the full `Samples` slice (capacity=10). With 56,799 records at 74.2% having
count=1, this means serializing 1 sample instead of 10 per record.

The implementation approach should be:
- Custom `MarshalJSON` on `BoundedHistory` that serializes `ChronologicalSamples()` (only valid samples, linearized from circular buffer).
- Custom `UnmarshalJSON` that reads the linear samples and reconstructs the circular buffer via `NormalizeAndValidate`.
- OR: add a `CompactSamples []ProbeSample` field with `json:"samples"` tag and use the existing `Samples` field only at runtime.

**Recommendation:** Custom `MarshalJSON`/`UnmarshalJSON` is cleanest. The existing
[`NormalizeAndValidate`](file:///home/amirreza-a2a/gemsub/internal/store/history.go#L56-L99)
already handles reconstruction from arbitrary sample slice lengths.

#### B5. Acceptance thresholds as benchmark gates

Spec lines 78-80:
> "Persisted state size ... reduced from 171 MB to under 10 MB"
> "Store.Load() time ... drops from ~1.85 s to under 250 ms"
> "Store.Save() duration drops from ~1.40 s to under 150 ms, with heap allocations ... reduced by over 80%"

These thresholds are reasonable estimates but depend on the specific 56k deployment
dataset. They should be stated as **benchmark-verified on the reference dataset**, not
as unconditional acceptance criteria that must hold for all possible dataset sizes.

#### B6. Derived field omission is already safe

Verified that [`Store.Load()`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L312-L372)
already recomputes all derived fields on load:

| Field | Recomputed at | Source |
|:---|:---|:---|
| `Score` | [`store.go:351`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L351) | `History.ComputeScore()` |
| `HasPassed` | [`store.go:355-358`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L355-L358) | `History.LastPassedLatency()` |
| `LastPassedLatency` | [`store.go:356`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L356) | `History.LastPassedLatency()` |
| `Latest.Passed` | [`store.go:364`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L364) | Derived from `Latest.Status` |
| `Latest.PreviouslyPassed` | [`store.go:366`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L366) | `History.PreviouslyPassed()` |
| `Latest.ConsecutiveInconclusive` | [`store.go:367`](file:///home/amirreza-a2a/gemsub/internal/store/store.go#L367) | `History.ConsecutiveInconclusive()` |

Omitting `Score`, `HasPassed`, `LastPassedLatency`, `Latest.Passed`,
`Latest.PreviouslyPassed`, and `Latest.ConsecutiveInconclusive` from serialization is
safe. Use `json:",omitempty"` or custom marshaling.

### C. Recommended Wording Changes

1. **Resolve file naming** — Replace spec line 49 with:
   ```
   - The primary persistence path writes `<configured_path>.gz`
     (e.g. `gemsub_state.json.gz`). The configured `path` field in Store is unchanged.
   ```

2. **Legacy file policy** — Add after line 52:
   ```
   - The legacy uncompressed state file is NOT auto-deleted or auto-renamed.
     After the first successful compressed save, an informational log message is emitted.
     Operators may manually remove the legacy file after verification.
   ```

3. **Latest.Link conditional omission** — Replace line 43 with:
   ```
   - `Latest.Link` is conditionally omitted when identical to `ActiveLink` (set to empty
     string before serialization, restored to `ActiveLink` on load). If they differ, both
     are preserved.
   ```

4. **Acceptance thresholds** — Qualify lines 78-80:
   ```
   - [ ] Persisted state size on the reference 56,799-record deployment dataset is reduced
         from 171 MB to under 10 MB, verified by benchmark.
   - [ ] Startup Store.Load() time on the reference dataset drops to under 250 ms, verified
         by benchmark.
   - [ ] Store.Save() duration on the reference dataset drops to under 150 ms with heap
         allocations reduced by over 80%, verified by benchmark.
   ```

### D. Scope Change

**No scope change needed.** All changes remain within `internal/store/`.

### E. Can Proceed After Corrections?

**Yes.** After resolving the file naming, legacy file lifecycle, and `Latest.Link`
conditional omission, the ticket can proceed directly to `/implement`.

---

## Ticket 29 — `feat(scheduler): implement fair candidate rotation under ProbeLimit`

### A. Readiness: **READY WITH SPEC CHANGES**

### B. Ambiguities / Blockers

#### B1. Rotation state persistence (AMBIGUITY — must resolve)

Spec acceptance criterion line 65:
> "Daemon restarts do not cause candidate starvation."

This implies rotation state must survive restart. But the spec scope (lines 48-51)
restricts changes to `internal/scheduler/` only — no Store persistence changes.

If rotation state is ephemeral (reset to zero on restart), the first `ProbeLimit`
candidates are re-tested immediately after restart, causing transient unfairness but
NOT permanent starvation (subsequent cycles advance the cursor). Whether this satisfies
"do not cause candidate starvation" depends on interpretation.

**Three options:**

| Option | Persistence | Restart behavior | Store change? |
|:---|:---|:---|:---|
| A. Ephemeral cursor | None | Cursor resets; first K candidates re-tested; rotation resumes from there | No |
| B. Store-persisted cursor | Add `RotationCursor string` to `Snapshot` | Full continuity | Yes (minimal) |
| C. Sidecar file | Scheduler writes `rotation_state.json` | Full continuity | No |

**Recommendation:** Option A (ephemeral) is simplest and sufficient. Transient re-testing
of the first K candidates after a restart is not "starvation" — it's a one-cycle delay.
Permanent starvation only occurs if the cursor never advances, which doesn't happen with
ephemeral state. The acceptance criterion should be clarified:

> "Daemon restarts do not cause **permanent** candidate starvation. Transient re-testing
> of recently-probed candidates on the first post-restart cycle is acceptable."

If Option B is preferred, the scope must expand to include `internal/store/store.go`
for adding a `RotationCursor` field to `Snapshot`.

#### B2. Fairness invariant precision (AMBIGUITY)

Spec line 63:
> "No candidate in a stable population is starved of probe opportunities across
> ⌈N / ProbeLimit⌉ cycles."

This is the theoretical minimum for perfect round-robin on a static population. Under
churn (additions/removals), the bound may not hold exactly. Candidate additions insert
new never-tested candidates that may take priority, and removals may cause the cursor
to skip positions.

**Recommendation:** Weaken to:
> "No candidate in a stable population is starved of probe opportunities across
> 2 × ⌈N / ProbeLimit⌉ cycles."

This provides slack for edge cases while still guaranteeing eventual coverage.

#### B3. Rotation strategy selection

The spec lists three possible strategies (line 28):
> "stateful round-robin cursor, least-recently-tested prioritization, or epoch-based
> windowing"

**Recommendation:** Specify **least-recently-tested** as the canonical strategy. Reasons:
- Naturally handles churn (new candidates are "never tested" = highest priority).
- No cursor drift when candidates are added/removed.
- Failing candidates rotate naturally (tested once, then deprioritized until all others
  are tested).
- Simple to implement: sort candidates by `Latest.TestedAt` ascending, take first K.

Round-robin with a link-based cursor is also viable but has edge cases with churn that
require careful cursor management.

#### B4. Input to rotation: parsed candidates vs raw links

Looking at [`scheduler.go:124-137`](file:///home/amirreza-a2a/gemsub/internal/scheduler/scheduler.go#L124-L137):

```go
var candidates []parser.Candidate
...
if s.cfg.ProbeLimit > 0 && len(candidates) > s.cfg.ProbeLimit {
    candidates = candidates[:s.cfg.ProbeLimit]
}
```

The rotation operates on `[]parser.Candidate` (after parsing), not on raw links. The
rotation must select WHICH parsed candidates to probe. The `linkSet` (all raw links)
is passed to `StartCycle` separately and must remain the complete set.

This is already clear in the spec but should be made explicit in implementation notes:
rotation selects from `candidates` (parsed), while `linkSet` (all raw links) goes to
`StartCycle` unchanged.

#### B5. Interaction with `CandidatesLoaded` event

At [`scheduler.go:139-142`](file:///home/amirreza-a2a/gemsub/internal/scheduler/scheduler.go#L139-L142),
the `CandidatesLoaded` event reports `Total: len(candidates)` AFTER truncation. The TUI
uses this for progress display. After rotation, `Total` should reflect the number of
candidates selected for probing in THIS cycle, not the total population.

This is already correct in the current code and should remain so. No spec change needed,
but the implementation must preserve this semantic.

#### B6. Verified: StartCycle receives full linkSet

Confirmed at [`scheduler.go:125-127`](file:///home/amirreza-a2a/gemsub/internal/scheduler/scheduler.go#L125-L127):
```go
linkSet := make(map[string]struct{}, len(links))
for _, link := range links {
    linkSet[link] = struct{}{}
}
```

And [`scheduler.go:147`](file:///home/amirreza-a2a/gemsub/internal/scheduler/scheduler.go#L147):
```go
s.st.StartCycle(linkSet)
```

`linkSet` is built from ALL raw links, before any ProbeLimit truncation. This is correct
and must remain unchanged. Skipped candidates are in `pendingPresent`, not `pendingAbsent`.

#### B7. Least-recently-tested requires Store read access

If the implementation uses least-recently-tested, the scheduler needs to query
`rec.Latest.TestedAt` for each candidate. Currently, the scheduler doesn't read Store
records directly — it passes candidates to the tester and receives results.

Options:
- Use `Store.GetRecord(canonical)` per candidate to read `TestedAt`. This requires
  N individual Store lookups (under RLock, each fast).
- Add a `Store.TestedAtIndex() map[string]time.Time` bulk method.
- Track `TestedAt` in scheduler-local state from probe results received via callback.

**Recommendation:** Track last-tested timestamps in scheduler-local state
(`map[string]time.Time`), updated from each probe result callback. This avoids
Store API changes and keeps rotation logic entirely within the scheduler.

### C. Recommended Wording Changes

1. **Restart behavior** — Replace acceptance criterion line 65 with:
   ```
   - [ ] Daemon restarts do not cause permanent candidate starvation. Transient
         re-testing of recently probed candidates on the first post-restart cycle
         is acceptable.
   ```

2. **Fairness bound** — Replace line 63 with:
   ```
   - [ ] No candidate in a stable population is starved of probe opportunities across
         2 × ⌈N / ProbeLimit⌉ cycles.
   ```

3. **Strategy selection** — Replace line 28 with:
   ```
   - Implement a least-recently-tested scheduling strategy: candidates are sorted by
     their last probe timestamp (ascending, never-tested first), and the first
     ProbeLimit candidates are selected. This naturally handles churn, prioritizes
     untested candidates, and prevents monopolization by failing nodes.
   ```

4. **Implementation note for TestedAt** — Add:
   ```
   - Rotation state (last-tested timestamps) is maintained in scheduler-local ephemeral
     state, populated from probe result callbacks. It does not require Store API changes
     or new persistence.
   ```

### D. Scope Change

**No scope change needed** if ephemeral least-recently-tested is chosen. All changes
remain within `internal/scheduler/`.

### E. Can Proceed After Corrections?

**Yes.** After resolving the restart semantics, fairness bound, and strategy selection,
the ticket can proceed directly to `/implement`.

---

## Summary

| Ticket | Readiness | Blockers | Scope Change | Proceed After Fix? |
|:---|:---|:---|:---|:---|
| **27** | READY WITH SPEC CHANGES | Store API scope, acceptance criteria phrasing | Expand: add `internal/store/store.go` (one read-only method) | Yes |
| **28** | READY WITH SPEC CHANGES | File naming, legacy file lifecycle, `Latest.Link` safety | None | Yes |
| **29** | READY WITH SPEC CHANGES | Restart persistence, fairness bound, strategy selection | None (if ephemeral) | Yes |

No ticket is NOT READY. All three can proceed to `/implement` after the recommended
spec corrections are applied.
