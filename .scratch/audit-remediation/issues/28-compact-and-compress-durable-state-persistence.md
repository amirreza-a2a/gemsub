# 28: perf(store): compact and compress durable state persistence

Type: performance / persistence
Priority: P1
Area: Store / Persistence / Performance
Status: ready-for-agent
Blocked by: None (can start immediately)
Source Debt: TD-016 (State persistence compaction and compression)

## Problem

The current JSON persistence representation in `Store.Save()` is excessively bloated and expensive at real-world scale.

Measured production deployment:
- 56,799 candidate records
- 171.11 MB state file (`gemsub_state.json`)
- 57.22 MB consumed purely by `json.MarshalIndent` whitespace
- 45.25 MB consumed by uninitialized `BoundedHistory` empty sample padding (~477,000 dummy structs)
- ~15 MB duplicated URL strings (`latest.link` repeating `active_link`)
- ~10 MB redundant derived state (`score`, `has_passed`, `last_passed_latency` recomputed on load)
- ~610 MB heap allocation burst during serialization
- ~1.85 s startup load time
- ~1.40 s cycle-end save time (including 150 ms `fsync` block)

The whole-state JSON snapshot approach is simple and crash-safe, but persisting formatted empty data introduces massive memory spikes and slow disk I/O.

## Current Behavior

1. **Circular Buffer Serialization Bloat**: `BoundedHistory` preallocates `make([]ProbeSample, capacity)` (capacity=10). In 74.2% of records (`count = 1`), 9 empty dummy sample structs with zero-values (`cycle_id: 0`, `tested_at: 0001-01-01...`) are serialized to disk for every record.
2. **Indented Formatting Overhead**: `Store.Save()` invokes `json.MarshalIndent(snap, "", "  ")`, adding 57 MB of space indentation and newlines to disk.
3. **Redundant String Duplication**: `CandidateRecord.Latest.Link` is serialized verbatim even though it is identical to `ActiveLink`.
4. **Uncompressed Disk Footprint**: The state file is stored as raw text (171 MB), causing high physical write volume and slow cold-start reads.

## Desired Behavior

1. **Retain JSON Architecture (No SQLite)**:
   - Preserve the document-oriented snapshot model without introducing database drivers, CGO dependencies, or schema migration frameworks.
2. **Compact BoundedHistory Serialization**:
   - Serialize only valid probe samples (`samples[:count]`).
   - Eliminate all empty dummy sample structs from the serialized representation.
   - On load, reconstruct the circular buffer invariants cleanly via `BoundedHistory.NormalizeAndValidate`.
3. **Eliminate Redundant Field Serialization & Reconstruct Derived State**:
   - `Latest.Link`:
     - If `Latest.Link == ActiveLink`, omit `Latest.Link` from serialized output (`json:"link,omitempty"`).
     - If they differ, preserve both.
     - On load, if `Latest.Link` is empty, restore it from `ActiveLink`.
   - The following derived fields are reconstructed on load and need not be persisted:
     - `Score` (recomputed via `History.ComputeScore`)
     - `HasPassed` (recomputed via `History.LastPassedLatency`)
     - `LastPassedLatency` (recomputed via `History.LastPassedLatency`)
     - `Latest.Passed` (derived from `Latest.Status == StatusPassed`)
     - `Latest.PreviouslyPassed` (derived via `History.PreviouslyPassed`)
     - `Latest.ConsecutiveInconclusive` (derived via `History.ConsecutiveInconclusive`)
4. **Compact Serialization**:
   - Replace `json.MarshalIndent` with compact standard `json.Marshal` in the production save path.
5. **Primary Persistence File Format (<configured_path>.gz)**:
   - The primary persistence file is `<configured_path>.gz` (e.g. `gemsub_state.json` → `gemsub_state.json.gz`).
   - The configured Store path string remains backward-compatible.
   - Transparent gzip compression is implemented using standard library `compress/gzip`.
   - Gzip applies ONLY to internal Store persistence snapshots. Do NOT compress subscription output files or Subserver raw/base64 payloads (unless existing HTTP content-encoding independently provides compression).
6. **Robust Dual-Format Loader & Legacy Lifecycle**:
   - `Store.Load()` first attempts to read `<configured_path>.gz`. If absent, it falls back to legacy `<configured_path>`.
   - Additionally inspects magic bytes (`0x1f 0x8b` for gzip) to select decompression regardless of file extension.
   - Full backward compatibility is preserved for:
     - Legacy V1 state (`results` array)
     - Legacy V2 raw uncompressed JSON (`records` array with 10-element circular buffer)
     - New V2 compact compressed state
   - Legacy file lifecycle:
     - Do NOT automatically delete the legacy JSON file.
     - Do NOT automatically rename it during `Load()`.
     - After a successful compressed save, emit an informational log that the legacy state file remains available for manual archival/removal.
7. **Atomic Crash-Safe Write Guarantee**:
   - Atomic replacement pattern preserved: write to sibling temporary file `<configured_path>.gz.tmp` in same directory → write compressed content → `f.Sync()` → `f.Close()` → `os.Rename(tmp, "<configured_path>.gz")`.
   - Ensure temporary files are unconditionally removed on write/sync/close failure.
8. **Preserve Complete Domain Invariants**:
   - Candidate identities, active/canonical links, timestamps, cycle counters, error categories, reasons, and transport evidence flags must be preserved with 100% fidelity.

## Exact Scope

- `internal/store/store.go`
- `internal/store/history.go`
- `internal/store/store_test.go`
- `internal/store/history_test.go`

## Out of Scope

- SQLite or embedded database engines.
- Scheduler, Tester, TUI, Publisher, or Subserver changes.
- Configuration syntax breaking changes.
- Compressing subscription output files or Subserver HTTP endpoint responses.

## Acceptance Criteria

- [ ] Primary persistence path writes `<configured_path>.gz` using transparent standard library `compress/gzip` compression.
- [ ] Gzip compression applies strictly to internal Store persistence; subscription files and Subserver outputs remain uncompressed.
- [ ] `Store.Load()` transparently loads `<configured_path>.gz` (falling back to legacy `<configured_path>`), inspecting magic bytes `0x1f 0x8b`.
- [ ] Legacy uncompressed state files are not deleted or renamed automatically; an informational log is emitted on successful compressed save.
- [ ] Uninitialized dummy sample padding in `BoundedHistory` is completely omitted from the persisted payload.
- [ ] `Latest.Link` is omitted when equal to `ActiveLink` and restored from `ActiveLink` on load.
- [ ] Derived state (`Score`, `HasPassed`, `LastPassedLatency`, `Latest.Passed`, `Latest.PreviouslyPassed`, `Latest.ConsecutiveInconclusive`) is deterministically reconstructed on load.
- [ ] Compact serialization replaces indented formatting in the production persistence path.
- [ ] Persisted state size on the reference 56,799-record deployment dataset is reduced from 171 MB to under 10 MB (a >90% reduction), verified by benchmark.
- [ ] Startup `Store.Load()` time on the reference dataset drops from ~1.85 s to under 250 ms, verified by benchmark.
- [ ] `Store.Save()` duration on the reference dataset drops from ~1.40 s to under 150 ms, with heap allocations during save reduced by over 80%, verified by benchmark.
- [ ] Atomic crash-safe replacement (`<configured_path>.gz.tmp` → `fsync` → `close` → `rename`) is preserved; failure cleanup removes temporary files.
- [ ] Round-trip save/load preserves 100% of candidate health, scoring, history, and transport evidence across V1 state, V2 raw JSON, and V2 compressed state.
- [ ] Corrupted files (truncated gzip, malformed JSON) fail safely with informative errors without panicking.
- [ ] Full repository unit and race tests pass with zero regressions.

## Dependencies

- None (can be implemented immediately).
