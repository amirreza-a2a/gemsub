Type: feature
Status: ready-for-agent
Priority: P2
Area: Tester / Store Telemetry
Blocked by: none
Research Reference: `docs/research/jitter-measurement.md`

## Problem

Current latency telemetry in `gemsub` (`Result.Latency` and `Result.TransportLatency`) records only a single point-in-time round-trip observation per candidate per cycle. A proxy outbound may complete an initial handshake with acceptable latency but suffer from route flapping, bufferbloat, or intermittent packet loss under continuous traffic.

To evaluate connection stability, `gemsub` needs jitter telemetry — running supplementary latency samples on candidates that pass the primary probe and computing a dispersion statistic alongside the existing latency metrics.

### Architecture & Performance Rationale
An empirical profiling spike (`docs/research/jitter-measurement.md`) resolved whether extra samples require a fresh sing-box `Box` per sample or can reuse the already-open `Box` from the main probe attempt:
- **Empirical Caveat**: The spike's specific timing delta (~+8.5% / +88.7ms per sample for a fresh Box vs. reused Box) is based on a small empirical sample (18 sample comparisons from 6 connected candidates out of 60 tested) and is a directional estimate rather than a high-precision benchmark.
- **Structural Proof**: The decision to reuse the Box does not depend on timing precision; it follows directly from eliminating the ~988 KB heap allocation across ~2,689 objects per Box spin-up (sing-box DNS freelru cache construction, router options initialization) and recurring Linux netlink route table queries (`syscall.NetlinkRIB`). Reusing the open `dialFn` closure costs < 4 KB across ~24 heap objects per sample (pure HTTP transaction).

### Documentation & Semantic Ambiguity Requirement
`Jitter == 0` is overloaded: it represents both "no measurable jitter" (identical sample latencies) and "unmeasured / insufficient successful samples" (e.g. failed candidates, or fewer than 2 successful round trips). This ambiguity must be explicitly documented in code comments on the `Jitter` field so downstream layers (sorting, export formats, future scoring algorithms) do not treat an unmeasured candidate as having perfectly stable latency.

## Current Behavior

- `internal/config/config.go`: `TestConfig` has no jitter configuration, and `TestConfig.Clone()` only deep-copies `BlockPhrases`, `Gemini.BlockPhrases`, and `MaxRetriesRaw`.
- `internal/tester/probe.go`: `executeAttempt` executes Stage 1 (Transport) and Stage 2 (Gemini), resolves `classResult`, and returns, immediately closing the sing-box instance via deferred `closeBox()`. No supplementary samples are collected.
- `internal/store/record.go` / `internal/store/store.go`: `store.Result` contains `Latency` and `TransportLatency`, but no `Jitter` field.
- `internal/store/history.go`: `store.ProbeSample` contains `Latency` and `TransportLatency`, but no `Jitter` field.
- `internal/store/store.go`: Snapshot decoder `fastDecodeSnapshot` recognizes known fields and skips unknown keys via `p.skipValue()`.
- `internal/store/store.go`: `writeSnapshotJSON` manually writes JSON fields via string appends and does not emit a `"jitter"` field.

## Desired Behavior

1. **Configuration (`internal/config`)**:
   - `TestConfig` gains `JitterSamplesRaw *int json:"jitter_samples,omitempty"` and parsed `JitterSamples int json:"-"`.
   - `Validate()` sets default `JitterSamples = 3` when omitted/nil. If explicitly set to `0`, jitter sampling is disabled.
   - `TestConfig.Clone()` explicitly deep-copies `JitterSamplesRaw`:
     ```go
     if t.JitterSamplesRaw != nil {
         v := *t.JitterSamplesRaw
         cp.JitterSamplesRaw = &v
     }
     ```
     mirroring the existing deep-copy behavior of `MaxRetriesRaw` to prevent pointer aliasing across cloned configs.
2. **In-Process Sampling & Lifecycle Insertion Window (`internal/tester/probe.go`)**:
   - The insertion point is in `executeAttempt`: between where `classResult` is finalized (after the `gemini.Probe` branch resolves, around line 229) and the function's return (line 236) — the window where the Box is still alive but classification is already known.
   - If `classResult.Status == store.StatusPassed` and `cfg.JitterSamples > 0`:
     - Execute up to $N$ (`cfg.JitterSamples`) lightweight HTTP GET requests against `cfg.HealthURL` (`generate_204`) reusing the open `dialFn`.
     - Bound each sample with a short timeout (min(2s, cfg.HealthTimeout)).
     - Initial transport probe latency (`tr.Latency`) serves as sample $t_0$. Successful extra samples serve as $t_1, \dots, t_k$.
3. **Statistical Calculation (`internal/tester`)**:
   - Compute sample standard deviation over all successful samples $[t_0, t_1, \dots, t_m]$ ($m \ge 2$):
     $$\bar{t} = \frac{1}{m}\sum_{i=0}^{m-1} t_i, \quad s = \sqrt{\frac{1}{m-1}\sum_{i=0}^{m-1} (t_i - \bar{t})^2}$$
   - Store the result in `classResult.Jitter = time.Duration(s)`.
4. **Best-Effort Resilience & Non-Interference**:
   - If a supplementary jitter sample times out or fails, the candidate's `StatusPassed` remains untouched. Auxiliary telemetry must never fail or delay an otherwise-passed candidate.
   - If total successful samples $m < 2$ (e.g. initial probe succeeded but all extra samples failed), record `Jitter = 0`.
5. **Store Schema & Persistence (Additive within Snapshot Version 2)**:
   - `store.Result` and `store.ProbeSample` gain `Jitter time.Duration json:"jitter,omitempty"`.
   - `fastDecodeSnapshot` in `internal/store/store.go` adds `case "jitter":` to parse integer nanoseconds for both `rec.Latest` and `smp`.
   - `writeSnapshotJSON` in `internal/store/store.go` manually appends `,"jitter":` for both `rec.Latest` (when `rec.Latest.Jitter > 0`) and each history entry in `rec.History.Samples` (when `smp.Jitter > 0`), mirroring how `"latency"` and `"transport_latency"` are written:
     ```go
     if rec.Latest.Jitter > 0 {
         scratch = append(scratch, `,"jitter":`...)
         scratch = strconv.AppendInt(scratch, int64(rec.Latest.Jitter), 10)
     }
     ```
   - Snapshot Version remains `2`.
6. **Field Documentation**:
   - Add explicit doc-comment on `Result.Jitter` and `ProbeSample.Jitter`:
     `// Jitter is the sample standard deviation of probe round-trip latencies. Note: a value of 0 is ambiguous and indicates either zero measurable variation or that jitter was not measured / had insufficient samples (< 2).`

## Architecture Boundaries

```text
internal/config  ── (Loads & validates test.jitter_samples, default 3; deep-copies in Clone)
        │
        ▼
internal/tester  ── (Reuses dialFn during executeAttempt window, computes sample stddev)
        │
        ▼
internal/store   ── (Persists Jitter in writeSnapshotJSON & fastDecodeSnapshot within Version 2)
```

- `internal/tester`: Owns probe execution, Box lifecycle, sample gathering, and standard deviation computation.
- `internal/store`: Owns persistence, history ring buffer, and snapshot serialization/deserialization.
- `internal/store/score.go`: **STRICT BOUNDARY**: Jitter is NOT incorporated into `ComputeScore`, scoring decay, or `MinServableScore` filtering in this ticket. It is stored/reported telemetry only.

## Scope & Non-Goals

### In Scope
- Add `jitter_samples` to `TestConfig` with validation, default of 3, and deep-copy in `TestConfig.Clone()`.
- In-process sampling in `internal/tester/probe.go` reusing `dialFn` on passed candidates.
- Sample standard deviation calculation helper with unit tests for numerical edge cases ($N=0, 1, 2$, identical values, large variances).
- Best-effort fault handling (sample drops do not affect candidate status).
- Add `Jitter time.Duration` to `store.Result` and `store.ProbeSample` with doc-comments explaining `Jitter == 0` ambiguity.
- Update `fastDecodeSnapshot` (read path) and `writeSnapshotJSON` (write path) in `internal/store/store.go` to support `"jitter"`.
- Round-trip integration test verifying `store.Save()` followed by `store.Load()` asserts `Jitter` survives across snapshots.
- Unit and integration tests verifying Box reuse, sampling, resilience, config cloning, and persistence.

### Non-Goals
- Incorporating jitter into candidate scoring (`ComputeScore`), ranking, or serving thresholds.
- Adding jitter to subserver or publisher output formats (presentation layer).
- Bumping store snapshot version to Version 3.
- Retrying failed jitter samples.
- Measuring jitter on failed or inconclusive candidates.

## Acceptance Criteria

- [ ] `config.TestConfig` recognizes `test.jitter_samples` in `config.json`, defaulting to `3` when omitted or nil.
- [ ] Setting `test.jitter_samples = 0` disables jitter sampling entirely.
- [ ] `TestConfig.Clone()` explicitly deep-copies `JitterSamplesRaw` pointer so modifications to cloned instances do not mutate the original.
- [ ] For candidates that achieve `StatusPassed`, exactly `jitter_samples` extra probes are executed against `cfg.HealthURL` reusing the open `dialFn` from the primary attempt.
- [ ] No fresh sing-box `Box` instances are created (`box.New`) for jitter sampling.
- [ ] Jitter statistic is computed as the sample standard deviation ($s = \sqrt{\frac{1}{m-1}\sum (t_i - \bar{t})^2}$) across the initial transport probe latency and successful extra samples.
- [ ] If fewer than 2 successful latency measurements are available, `Jitter` is recorded as `0`.
- [ ] A timeout or failure on an extra jitter sample does not alter the candidate's `StatusPassed` status or error classification.
- [ ] `store.Result` contains `Jitter time.Duration json:"jitter,omitempty"`.
- [ ] `store.ProbeSample` contains `Jitter time.Duration json:"jitter,omitempty"`.
- [ ] `store.Result.Jitter` and `store.ProbeSample.Jitter` contain doc-comments explicitly documenting the `Jitter == 0` ambiguity.
- [ ] `writeSnapshotJSON` in `internal/store/store.go` writes `,"jitter":` for `rec.Latest` and each history sample in `rec.History.Samples`.
- [ ] `fastDecodeSnapshot` in `internal/store/store.go` correctly deserializes `"jitter"` for both `rec.Latest` and `rec.History.Samples`.
- [ ] A persistence test verifies round-trip persistence by calling `store.Save()` followed by `store.Load()` and asserting `Jitter` values on `rec.Latest` and `rec.History.Samples` survive intact.
- [ ] Existing Version 2 snapshots without `"jitter"` load cleanly with `Jitter == 0`.
- [ ] Store snapshot Version remains `2`.
- [ ] `ComputeScore` and `networkHealthyStateLocked` are unmodified.
- [ ] Unit tests verify standard deviation calculation with edge cases ($m=0, 1, 2$, identical values).
- [ ] Integration test verifies end-to-end execution of jitter sampling on passed candidates.
- [ ] `make test` passes cleanly.
- [ ] `go test -race ./...` passes with zero race warnings.
- [ ] `git diff --check` passes with zero whitespace errors.

## Verification Requirements

1. `go test -v ./internal/config/...`
2. `go test -v ./internal/tester/...`
3. `go test -v ./internal/store/...`
4. `make test`
5. `go test -race ./...`
6. `git diff --check`
