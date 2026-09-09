# Ticket 28: Readiness and Baseline Audit

## A. Exact current persistence model

- **Entrypoints:** `Store.Save()` and `Store.Load()` use the standard `encoding/json` package. `Save()` explicitly uses `json.MarshalIndent(snap, "", "  ")`.
- **Envelope Structure:** The top-level payload is a `Snapshot` struct.
  - V1 uses the `Results` array (slice of `Result`).
  - V2 uses `Version` (2), `LastCycle`, `CycleCount`, and `Records` (slice of `*CandidateRecord`).
- **BoundedHistory:** Serialized implicitly as an object containing `Capacity`, `Count`, `Start`, and `Samples` (`[]ProbeSample`). Because `Samples` is a preallocated slice of length `Capacity`, JSON marshaling emits all `Capacity` elements, including uninitialized dummy structs beyond `Count`. There are no custom marshalers or aliases involved.
- **Derived Fields:**
  - `CandidateRecord` persists `Score`, `LastPassedLatency`, `HasPassed`.
  - `Result` persists `ConsecutiveInconclusive`, `PreviouslyPassed`.
- **JSON Tags:** Standard tags are used. Derived fields lack `-` tags, meaning they are persisted. `Result.Link` is `json:"link"`, preventing omission when empty.
- **Compatibility:** `Load()` checks if `snap.Version >= 2` or `len(snap.Records) > 0`. If so, it processes as V2. Otherwise, it processes `snap.Results` for V1.

## B. Exact invariants that MUST remain unchanged

- **Crash-Safety Atomicity:** `Store.Save()` currently writes to `<path>.tmp`, calls `f.Sync()`, `f.Close()`, and performs an atomic `os.Rename(tmp, s.path)`. Failures invoke `f.Close()` and `os.Remove(tmp)`. This sequence MUST be rigorously preserved.
- **BoundedHistory Reconstruction:** The system must continue to enforce circular buffer invariants (clamping capacity, reconstructing slice, managing `Start`/`Count`). `NormalizeAndValidate(maxCapacity)` currently handles this dynamically and must remain the authoritative mechanism.
- **Derived State Authority:** `Load()` completely overwrites persisted derived state (`Score`, `HasPassed`, `LastPassedLatency`, `Latest.Passed`, `Latest.PreviouslyPassed`, `Latest.ConsecutiveInconclusive`) by recomputing them from the valid `History` samples. This authoritative derivation must remain unchanged.
- **Legacy Migrations:** V1 `Results` arrays and V2 uncompressed JSON structures must continue to parse correctly with zero data loss.

## C. Implementation hazards

1. **Gzip File Handles:** When migrating to `<path>.gz`, the `gzip.Writer` MUST be explicitly closed *before* `f.Sync()` and `f.Close()`. Failing to `Close()` the gzip writer will truncate the payload footer, leading to silent corruption or `unexpected EOF` errors on subsequent loads.
2. **Result.Link Omission:** To satisfy `omitempty` logic on `Result.Link`, the `json:"link"` tag must be updated to `json:"link,omitempty"`. We must manually zero `Latest.Link` during `Save()` if it equals `ActiveLink`, and restore it from `ActiveLink` during `Load()`.
3. **Derived Fields Serialization:** Setting derived fields to zero before `Save()` is the safest approach to ensure they are omitted without creating complex custom JSON aliases, provided their JSON tags include `omitempty`.
4. **Custom BoundedHistory MarshalJSON:** Implementing `MarshalJSON` on a value receiver `BoundedHistory` is safe, but `UnmarshalJSON` requires a pointer receiver `*BoundedHistory`. We must ensure we decode an array of `ProbeSample` directly into `Samples`, update `Count`, and rely on `NormalizeAndValidate` to rebuild the circular buffer correctly.
5. **Magic Byte Detection:** Using `os.ReadFile` restricts our ability to peak at magic bytes before decoding. We should implement a stream inspection (e.g. `bytes.HasPrefix` with `\x1f\x8b`) to switch between gzip decompression and plain JSON decoding transparently.
6. **File Lifecycle:** `Save()` must write to `<path>.gz.tmp` and rename to `<path>.gz`. The legacy `<path>` must NOT be deleted automatically.

## D. Recommended implementation sequence

1. Update `Result` and `CandidateRecord` JSON struct tags to ensure derived fields (e.g. `score,omitempty`) and `link,omitempty` are defined.
2. Implement custom `MarshalJSON` and `UnmarshalJSON` for `BoundedHistory` to only serialize the valid `Samples[:Count]` ordered chronologically.
3. Update `Store.Save()` to:
   - Zero out derived fields and duplicate `Latest.Link` strings in the outbound snapshot.
   - Use `json.Marshal` instead of `json.MarshalIndent`.
   - Write to `<path>.gz.tmp` wrapped in `gzip.NewWriter`.
   - Ensure `gw.Close()` -> `f.Sync()` -> `f.Close()` -> `os.Rename`.
4. Update `Store.Load()` to:
   - First attempt to open `<path>.gz`. If `os.IsNotExist`, open legacy `<path>`.
   - Inspect the file header for gzip magic bytes (`\x1f\x8b`).
   - Route through `gzip.NewReader` if compressed, else raw bytes.
   - Restore `Latest.Link` from `ActiveLink` if omitted.

## E. Benchmark methodology & baseline numbers

A scratch benchmark (`scratch_bench_test.go`) was written to generate 56,799 candidates with realistic data structures (1 history entry, 9 empty capacity padding).

**Baseline Results:**
- **Serialized Size:** 137.65 MB (approaching production reported 171 MB)
- **Save() Duration:** ~1.34 seconds
- **Save() Allocations:** ~621 MB, 738,451 allocs/op
- **Load() Duration:** ~1.51 seconds
- **Load() Allocations:** ~397 MB, 682,005 allocs/op

## F. Acceptance matrix

| Criterion | Status | Notes |
|-----------|--------|-------|
| Primary persistence writes `<path>.gz` with stdlib gzip | NEEDS IMPLEMENTATION | Requires updating `Save()` file handling. |
| Gzip only applies to internal Store | NEEDS IMPLEMENTATION | Must ensure we do not touch Publisher or Subserver APIs. |
| `Store.Load()` checks `.gz`, fallback, magic bytes | NEEDS IMPLEMENTATION | Requires updated read logic. |
| Legacy files not deleted, info log emitted | NEEDS IMPLEMENTATION | Requires `slog.Info` trigger after successful `.gz` write. |
| Empty samples omitted | NEEDS IMPLEMENTATION | Needs custom `BoundedHistory` JSON marshalers. |
| `Latest.Link` omitted if matches `ActiveLink` | NEEDS IMPLEMENTATION | Requires pre-save zeroing and tag update. |
| Derived state reconstructed on load | PASS | Already guaranteed natively by existing `Load()` architecture. |
| Compact serialization replaces indent | NEEDS IMPLEMENTATION | Switch `json.MarshalIndent` to `json.Marshal`. |
| State size reduced >90% | NEEDS IMPLEMENTATION | Expected drop from ~137MB to <10MB. |
| Startup load time drops | NEEDS IMPLEMENTATION | |
| Save duration drops, allocations reduced >80% | NEEDS IMPLEMENTATION | |
| Atomic crash-safe replacement preserved | PASS / NEEDS IMPLEMENTATION | Architecture exists, but must be adapted for gzip. |
| Round-trip preserves 100% fidelity | PASS / NEEDS IMPLEMENTATION | `NormalizeAndValidate` makes this viable. |
| Corrupted files fail safely | PASS | Standard errors returned to caller. |

## G. FINAL VERDICT

**READY FOR IMPLEMENTATION**
