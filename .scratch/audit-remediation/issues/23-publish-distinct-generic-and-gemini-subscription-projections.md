# 23: feat(publisher): publish distinct generic and gemini subscription projections

Type: feature
Status: ready-for-agent
Blocked by: 22

## Problem

The current Publisher publishes only `Store.Passing()` candidates to root-level subscription files (`all.txt`, `vless.txt`, `vmess.txt`, `trojan.txt`) alongside `meta.json`. Consequently, the publication repository exposes only Gemini application-servable proxies.

However, the Tester and Store layers already maintain two distinct canonical query projections:
1. `Store.NetworkPassing()`: Transport-healthy candidates whose Stage 1 probe passed, regardless of Stage 2 Gemini application status (including Gemini PASS, RegionBlocked, TargetDenied, Server/Target error, or fresh Inconclusive).
2. `Store.Passing()`: Fully servable candidates that passed both Stage 1 transport and Stage 2 Gemini application gates.

By ignoring `Store.NetworkPassing()`, the Publisher fails to make network-healthy, transport-verified candidates available for general proxy usage. Furthermore, leaving legacy root-level files in existing publication repositories risks serving stale frozen candidate lists to consumers.

## Scope

1. **Dual-Projection Directory Topology**:
   - Establish two top-level projection directories in the published repository:
     - `generic/`: All transport/network-healthy candidates (`Store.NetworkPassing()`).
     - `gemini/`: All Gemini application-servable candidates (`Store.Passing()`).
   - Each projection directory contains:
     - `all.txt`: All candidates in the projection (deduplicated, sorted).
     - `vless.txt`: Only `vless://` candidates in the projection.
     - `vmess.txt`: Only `vmess://` candidates in the projection.
     - `trojan.txt`: Only `trojan://` candidates in the projection.
   - Candidates with unsupported schemes (e.g. `ss://`) are included in `all.txt` but omitted from protocol-specific files, preserving existing behavior.
   - If a projection or protocol has zero candidates, its corresponding file is emptied (`""`), preventing stale candidate persistence.
   - `meta.json` resides at the repository root.

2. **Legacy Root-Level File Migration & Stale Output Cleanup (Policy A)**:
   - Legacy root-level subscription files (`all.txt`, `vless.txt`, `vmess.txt`, `trojan.txt`) must NOT remain in the repository.
   - When publishing to an existing repository containing tracked legacy files, Publisher removes them (`os.Remove`), stages their deletion in Git (`git add`), and treats their removal as a repository change.
   - Any failure during legacy file deletion (`os.Remove`) fails loudly and aborts `Publish()` with an error.
   - **Policy A (Working Tree Safety for Untracked Legacy Files)**: If any legacy root file is untracked in the working tree (i.e. created manually or left uncommitted), `checkWorkingTreeSafety()` treats it as a protected manual file and aborts publication with an error before attempting any modification.
   - This prevents consumers from mistakenly fetching stale frozen proxies from legacy URLs while protecting uncommitted user files.

3. **Store Projection Consumption**:
   - Generic subscription files MUST be sourced directly from `Store.NetworkPassing()`.
   - Gemini subscription files MUST be sourced directly from `Store.Passing()`.
   - Publisher MUST NOT reimplement, reconstruct, or second-guess filtering/classification logic.

4. **Metadata Schema Extensions**:
   - Extend `meta.json` with projection-specific counts:
     - `generic_total`, `generic_vless`, `generic_vmess`, `generic_trojan`
     - `gemini_total`, `gemini_vless`, `gemini_vmess`, `gemini_trojan`
   - Preserve legacy fields mapped directly to the Gemini projection for backward compatibility:
     - `total_servable == gemini_total`
     - `vless_count == gemini_vless`
     - `vmess_count == gemini_vmess`
     - `trojan_count == gemini_trojan`
   - Retain `cycle_number` and `generated_at`.

5. **Deterministic Alphabetical Ordering Policy**:
   - Preserve stable alphabetical sorting within all generated files (`sort.Strings(unique)`).
   - Rationale: While Store provides ranking policies (`NetworkPassingRanked`, `PassingRanked`), latency-based ordering jitters from cycle to cycle even when the candidate set is identical. Alphabetical ordering ensures 100% byte-identical file outputs across unchanged candidate sets, eliminating spurious Git commit churn.

6. **Change Detection, Working Tree Safety & Push Recovery**:
   - Change detection checks all 8 subscription files across both projections.
   - A change in either projection (generic-only, Gemini-only, or both), or the presence of legacy root files to delete, triggers a commit and push.
   - If neither projection changed, no legacy files exist, and metadata counts are identical, publication is a no-op (no commit).
   - `checkWorkingTreeSafety` protects all publisher-owned files (`generic/*`, `gemini/*`, `meta.json`) as well as existing untracked legacy root files under Policy A.
   - Push failure recovery handles unpushed commits across cycles without creating duplicate commits.

## Required Publication Semantics

| Candidate Status & Evidence | `generic/` | `gemini/` | Rationale |
|---|---|---|---|
| Stage 1 Transport OK + Stage 2 Gemini PASS | YES | YES | Passes both transport and target application checks |
| Stage 1 Transport OK + Stage 2 RegionBlocked | YES | NO | Network is healthy; Gemini application blocked by location |
| Stage 1 Transport OK + Stage 2 TargetDenied (403) | YES | NO | Network is healthy; Gemini application denied |
| Stage 1 Transport OK + Stage 2 TargetError (5xx) | YES | NO | Network is healthy; Gemini target service error |
| Stage 1 Transport OK + Stage 2 Inconclusive (fresh) | YES | NO | Network is healthy; inconclusive Gemini probe without prior pass |
| Stage 1 Transport OK + Stage 2 Inconclusive (LKG) | YES | YES | Retains last-known-good servability under Store policy |
| Stage 1 Transport Failure (`TransportOK == false`) | NO | NO | Dead proxy; rejected at Stage 1, never servable |

## Repository Topology

```text
<repository>/
├── generic/
│   ├── all.txt
│   ├── vless.txt
│   ├── vmess.txt
│   └── trojan.txt
├── gemini/
│   ├── all.txt
│   ├── vless.txt
│   ├── vmess.txt
│   └── trojan.txt
└── meta.json
```

## Legacy Root-Level Output Migration & Cleanup (Policy A)

1. Legacy subscription files (`all.txt`, `vless.txt`, `vmess.txt`, `trojan.txt`) located at repository root are explicitly deprecated and deleted.
2. During `Publish()`, if any legacy file is tracked in the repository, Publisher deletes it from the filesystem and stages the deletion with Git (`git add`). If deletion fails, `Publish()` fails loudly with an error.
3. If any legacy root file exists as an untracked file in the working tree, `checkWorkingTreeSafety()` treats it under Policy A as a protected uncommitted working-tree file and aborts publication immediately.
4. Staged deletions contribute to `hasStagedChanges`, triggering a commit so legacy files are permanently removed from the remote repository branch.
5. Non-publisher files (e.g. `README.md`, `.gitignore`, `notes.txt`) are untouched.

## Metadata Schema

```json
{
  "generated_at": "2026-09-09T10:50:00Z",
  "cycle_number": 42,

  "generic_total": 7,
  "generic_vless": 2,
  "generic_vmess": 2,
  "generic_trojan": 2,

  "gemini_total": 4,
  "gemini_vless": 1,
  "gemini_vmess": 1,
  "gemini_trojan": 1,

  "total_servable": 4,
  "vless_count": 1,
  "vmess_count": 1,
  "trojan_count": 1
}
```

## Ordering Policy & Determinism

- Publication outputs use deterministic alphabetical ordering (`slices.Sort` / `sort.Strings`).
- **Rationale vs Store Ranking**:
  `Store.NetworkPassingRanked()` and `Store.PassingRanked()` order candidates by reliability score and probe latency. In live environments, latency jitter of a few milliseconds between cycles would continuously reorder lines in `all.txt`, causing Git diffs and commits on every single cycle even when zero candidates joined or dropped.
  Alphabetical ordering ensures byte stability, meaning identical candidate sets produce 0 Git diffs. Subserver and consumer applications that require ranked ordering can query the subserver API or sort client-side.

## Git Publication Safety & Change Detection

- **Target Files**: All 8 projection files plus `meta.json` (`targetFiles`).
- **Uncommitted Safety**: `checkWorkingTreeSafety` validates all 9 publisher-owned files.
- **Change Detection**:
  - Compares on-disk contents of all 8 files against freshly rendered projections.
  - If any subscription file differs, or if any legacy root file exists, `subscriptionUnchanged = false`.
  - When `subscriptionUnchanged == true`, existing `meta.json` is preserved only if all 8 counts (`Generic*` and `Gemini*`) match exactly.
- **Directory Creation**: Ensures `os.MkdirAll` is called for `generic/` and `gemini/` before file writes.
- **Empty Projections**: Renders 0-byte file (`[]byte("")`) when candidate count is 0, wiping stale contents.

## Acceptance Criteria

- [ ] Generic subscription files (`generic/all.txt`, `generic/vless.txt`, etc.) are populated exclusively from `Store.NetworkPassing()`.
- [ ] Gemini subscription files (`gemini/all.txt`, `gemini/vless.txt`, etc.) are populated exclusively from `Store.Passing()`.
- [ ] Candidates with healthy transport and Gemini pass appear in both `generic/` and `gemini/`.
- [ ] Candidates with healthy transport and Gemini RegionBlocked/TargetDenied appear in `generic/` but NOT in `gemini/`.
- [ ] Candidates with Stage 1 transport failures appear in neither projection.
- [ ] Protocol-specific files in both directories strictly contain only their designated protocol scheme (`vless://`, `vmess://`, `trojan://`).
- [ ] Candidates with other/unsupported schemes appear in `all.txt` of the respective projection and are excluded from protocol files.
- [ ] Legacy root-level subscription files (`all.txt`, `vless.txt`, `vmess.txt`, `trojan.txt`) are deleted on disk and staged for removal in Git.
- [ ] Emptied projections or protocol sets write empty files (`""`), eliminating stale entries.
- [ ] Independent changes in `generic/` trigger a Git commit even if `gemini/` is unchanged.
- [ ] Independent changes in `gemini/` trigger a Git commit even if `generic/` is unchanged.
- [ ] Synchronized cycles with zero candidate changes produce no new Git commit (safe no-op).
- [ ] Working tree safety check protects all 9 publisher-owned files against uncommitted edits.
- [ ] `meta.json` carries both `generic_*` and `gemini_*` counts, while legacy fields match Gemini counts for backward compatibility.
- [ ] All existing Store and Tester semantics remain 100% untouched; no generic Target abstraction is introduced.

## Explicit Non-Goals

- Do not redesign Store or Tester.
- Do not introduce a generic Target abstraction.
- Do not alter `Passing()` or `NetworkPassing()` semantics in Store.
- Do not add subserver multi-projection routing in this ticket (handled separately).
- Do not change persistence schema.

## Architectural Invariants Preserved

- `Store As Sole Source of Truth`: Projections are read directly from Store methods (`NetworkPassing` and `Passing`); Publisher contains zero candidate classification or policy logic.
- `Projection Independence`: Generic and Gemini subscriptions update and commit independently based on their respective Store populations.
- `Deterministic Publication`: Alphabetical ordering ensures byte-identical file generation across cycles with unchanged candidate sets.
- `Clean Repository State`: No stale legacy root-level subscription files linger to misrepresent current outputs.

## Expected Files/Modules

- `internal/publisher/publisher.go`
- `internal/publisher/publisher_test.go`
- `internal/scheduler/scheduler_two_stage_integration_test.go`

## Test Requirements

1. **Test A**: Transport healthy + Gemini pass → appears in both `generic/` and `gemini/`.
2. **Test B**: Transport healthy + Gemini RegionBlocked → appears in `generic/`, absent in `gemini/`.
3. **Test C**: Transport healthy + Gemini TargetDenied → appears in `generic/`, absent in `gemini/`.
4. **Test D**: Transport healthy + Gemini Inconclusive → appears in `generic/`; absent in `gemini/` for fresh candidate, present in `gemini/` if LKG applies; transport timeout absent in both.
5. **Test E**: Transport failure → absent in both `generic/` and `gemini/`.
6. **Test F**: Protocol-specific separation for both projections (`vless.txt`, `vmess.txt`, `trojan.txt`, `all.txt`).
7. **Test G**: Empty projection cleanup: candidate removal empties file (`[]byte("")`) without leaving stale lines.
8. **Test H**: Legacy root-level file cleanup: existing root `all.txt`, `vless.txt`, etc. are removed on disk and staged as deletions in Git.
9. **Test I**: Change detection: generic-only change triggers commit; Gemini-only change triggers commit; identical state produces no commit.
10. **Test J**: Working tree safety: verifies uncommitted changes in `generic/*`, `gemini/*`, or `meta.json` abort publication.
11. **Test K**: Ordering determinism: reverse-input candidate lists sort alphabetically across all files.
