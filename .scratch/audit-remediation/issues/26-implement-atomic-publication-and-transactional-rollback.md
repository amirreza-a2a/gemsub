# 26: feat(publisher): implement atomic publication and transactional rollback

Type: feature / reliability
Status: ready-for-agent
Blocked by: 23
Source Debt: TD-001 (Publisher atomic publication and rollback semantics)

## Problem

The Git-backed subscription publisher (`internal/publisher`) mutates files in the active publication repository working tree sequentially with in-place writes (`os.WriteFile`) and direct legacy-file removals (`os.Remove`). If an error occurs midway through this process (such as a disk-full condition `ENOSPC`, quota exhaustion, filesystem I/O error, permission glitch, process interruption, OOM kill, or container restart), the working tree is left in a partially mutated, torn state.

When the Scheduler triggers subsequent publication cycles, `Publisher.Publish` runs `checkWorkingTreeSafety`, which detects pre-existing uncommitted modifications and deletions in publisher-owned files. Because `checkWorkingTreeSafety` is designed to abort on dirty working trees, and because the current publisher provides no automated recovery or rollback mechanism, every subsequent cycle fails immediately.

In unattended production deployments, this causes a permanent publication deadlock: automated publishing halts indefinitely until an operator manually SSHes into the host to restore the working tree with manual Git commands.

## Current Behavior

1. **In-place destructive file writes:**
   `Publisher.Publish` iterates over `files` (`map[string][]byte`) in non-deterministic map order and writes directly to `repoDir` via `os.WriteFile(filePath, content, 0o644)`. This truncates the file immediately upon open (`O_TRUNC`), exposing concurrent filesystem readers to empty or partially-written files. If write fails on file $k$, files $1 \dots k-1$ have new contents while files $k \dots 9$ retain old contents or are missing.
2. **Uncheckpointed legacy file removal:**
   `legacyRootFiles` (`all.txt`, `vless.txt`, `vmess.txt`, `trojan.txt`) are deleted one by one using `os.Remove`. A failure during or after this loop leaves some legacy files deleted and others present.
3. **No transactional boundary or rollback handler:**
   There is no failure cleanup or recovery seam. Any error returned by `os.Remove`, `os.MkdirAll`, `os.WriteFile`, `git add`, `git diff`, or `git commit` immediately exits with an uncommitted dirty working tree.
4. **Persistent liveness deadlock:**
   `checkWorkingTreeSafety` sees the dirty files on every subsequent cycle and refuses to proceed, requiring human intervention.

## Desired Behavior

1. **Atomic File Replacement via Sibling Temp Files:**
   - For every publication target file (`generic/*.txt`, `gemini/*.txt`, `meta.json`), write content to a temporary sibling file in the same directory (e.g. `filepath + ".tmp"`).
   - Flush and synchronize file descriptor buffers to disk (`file.Sync()`).
   - Atomically replace the destination file using `os.Rename(tempPath, filePath)`.
   - In POSIX, `rename(2)` of a file over an existing file is atomic, eliminating the file truncation window and preventing concurrent readers from observing partially written files.
   - Clean up any temporary files on both success and failure paths.
2. **Transaction-Scoped Automated Rollback:**
   - Scope all publisher-owned mutations within a transactional execution boundary.
   - If any failure occurs after mutations begin (during legacy removal, directory creation, temp file creation, file renaming, git staging, or git diff/commit), an automated rollback executes immediately:
     - Discard all uncommitted changes to publisher-owned files (`targetFiles` and `legacyRootFiles`) by checking out pre-publish `HEAD` state: `git checkout HEAD -- <safetyFiles>`.
     - Remove any newly created or untracked publisher files or temp files.
     - Ensure the working tree is restored exactly to its clean pre-publication state.
   - Disarm the rollback once `git commit` succeeds.
3. **Strict Path Scoping & Working-Tree Safety:**
   - Rollback operations must be strictly scoped to publisher-owned paths (`targetFiles` and `legacyRootFiles`).
   - Do NOT use repository-wide destructive commands like `git reset --hard` or untargeted `git clean -fd`.
   - Unrelated user files or pre-existing modifications must never be touched, modified, or deleted by rollback.
   - `checkWorkingTreeSafety` must remain intact and must not be weakened.
4. **Self-Healing Automation:**
   - Transient write or staging failures in cycle $N$ must cleanly revert the working tree so that cycle $N+1$ encounters a clean working tree and can successfully publish.

## Exact Scope

- `internal/publisher/publisher.go`
- `internal/publisher/publisher_test.go`

## Out of Scope

- Changes to `Store`, `Scheduler`, `Subserver`, or `TUI`.
- Changes to publication directory topology (`generic/` and `gemini/`) or file naming.
- Changes to Git remote push retry semantics (`ahead` logic).
- Introduction of external dependencies or multi-process lock managers.

## Acceptance Criteria

- [ ] Publication writes proceed through sibling temporary files (`.tmp`), call `Sync()`, and atomically rename over target files.
- [ ] No temporary files remain in the repository after either successful or failed publication.
- [ ] Direct filesystem readers never observe truncated or empty target files during publication.
- [ ] Any failure during legacy removal, file writing, git staging, git diff, or git commit triggers automated rollback.
- [ ] Rollback cleanly restores all modified and deleted publisher-owned files to `HEAD` without manual intervention.
- [ ] Subsequent publication cycles are not blocked by `checkWorkingTreeSafety` following a transient publication failure.
- [ ] Rollback is strictly confined to publisher-owned paths; unrelated working tree files are never modified, reverted, or deleted.
- [ ] `checkWorkingTreeSafety` continues to detect pre-existing manual edits to publisher-owned files and abort without overwriting them.
- [ ] Existing successful publication behavior (dual projection output, protocol partitioning, meta.json counts, git commit, git push) remains 100% backward-compatible.
- [ ] All unit, integration, and race tests pass with zero regressions.

## Dependencies

- **Ticket 23**: Dual generic and gemini subscription projections and directory topology.
