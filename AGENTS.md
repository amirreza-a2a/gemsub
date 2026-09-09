# Agent Guidance

This file is the repository-level entry point for AI coding agents working on `gemsub`.

The goal is to make changes small, correct, reviewable, and consistent with the repository's established architecture and engineering workflow.

## 1. Project Context

`gemsub` is a Go-based subscription/proxy testing system.

Its core pipeline is:

```text
Sources
  ↓
Scheduler
  ↓
Tester / Probe
  ↓
Store
  ├── NetworkPassing() → generic projection
  └── Passing()        → Gemini projection
        ↓
  ├── Publisher
  └── Subserver
```

The Store is the authoritative source of candidate state, health, scoring, and projection membership.

Do not duplicate Store classification or scoring logic in presentation or delivery layers.

## 2. Repository Guidance

### Issue Tracker

Engineering work is tracked in GitHub Issues for `amirreza-a2a/gemsub`.

See:

`docs/agents/issue-tracker.md`

### Triage Labels

Use the repository's defined triage vocabulary.

See:

`docs/agents/triage-labels.md`

### Domain Documentation

Domain-specific context follows the repository's single-context layout:

`CONTEXT.md` + `docs/adr/`

See:

`docs/agents/domain.md`

### Technical Debt

Non-blocking findings from architecture audits and independent code reviews are recorded in:

`.scratch/audit-remediation/technical-debt.md`

Do not create a GitHub Issue for every minor P2 finding.

Promote a debt item to `/to-tickets` when it becomes planned engineering work or requires a dedicated implementation scope.

When a debt item is resolved, record the resolving ticket and commit in the register.

## 3. Engineering Workflow

Use the repository's established workflow:

### `/to-tickets`

Use `/to-tickets` to register and scope planned engineering work before implementation.

A ticket should define:

* problem and current behavior
* desired behavior
* architectural boundaries
* scope and non-goals
* acceptance criteria
* verification requirements
* dependencies

### `/implement`

Use `/implement` only for an already-registered ticket.

Implementation must remain within the ticket's defined scope.

Do not introduce unrelated refactors, speculative abstractions, or new features into an active ticket.

### Independent Review

Before committing a non-trivial implementation, perform an independent code review against the authoritative ticket specification.

The review must distinguish:

* blocking findings
* non-blocking technical debt
* code-quality observations
* upstream dependency findings

Non-blocking findings must not silently expand the active ticket.

### Commit and Push

Do not commit or push until the implementation has passed the required review and verification.

Before commit, inspect the final diff for:

* unintended files
* scope creep
* accidental deletions
* generated artifacts
* unrelated formatting or refactoring

## 4. Technical Debt Workflow

Use this lifecycle:

```text
Audit / Independent Review
        ↓
Is it blocking?
   ├── Yes → fix before commit
   └── No
        ↓
Technical Debt Register
        ↓
Backlog triage
        ↓
Promote via /to-tickets when prioritized
        ↓
Implement + Review + Verify
        ↓
Mark resolved with ticket + commit
```

Do not fix non-blocking debt opportunistically inside an unrelated ticket.

## 5. Architectural Boundaries

Preserve these boundaries unless an explicit ticket authorizes changing them:

* `Store` is the source of truth for candidate state and projection membership.
* `Tester` owns probing and probe semantics.
* `Scheduler` owns cycle orchestration and execution scheduling.
* `Publisher` owns Git-backed subscription publication.
* `Subserver` owns local HTTP subscription delivery and presentation.
* `TUI` owns terminal presentation.
* Avoid introducing generic abstractions merely to anticipate future targets or consumers.
* Do not move domain decisions into delivery or presentation layers.

When a ticket changes one boundary, verify that adjacent layers continue to consume the same canonical semantics.

## 6. Projection Semantics

The current system has two canonical projections:

* `Store.NetworkPassing()` — transport/network-healthy candidates.
* `Store.Passing()` — Gemini-servable candidates.

Delivery layers must consume these projections directly.

Do not independently recreate projection membership logic in:

* Publisher
* Subserver
* TUI

Any change to projection semantics requires explicit Store-level scope and regression verification.

## 7. Verification

Verification must be proportional to the risk and scope of the ticket.

At minimum:

* run focused tests for modified packages
* run repository-wide tests when practical
* run `go vet ./...` for Go changes
* run relevant race tests for concurrency-sensitive changes
* run `git diff --check`
* inspect final Git status before commit

For ticket-specific verification, follow the acceptance criteria in the authoritative ticket rather than inventing a substitute test plan.

If a required check cannot be run, report that explicitly.

Never claim a test, build, race check, or verification step passed unless it actually ran and passed.

## 8. Change Discipline

Prefer the smallest correct change.

Before adding a dependency or introducing a new abstraction:

1. inspect existing repository capabilities
2. check whether the current architecture already provides the required seam
3. prefer an existing pattern when it satisfies the requirement

Do not perform unrelated cleanup merely because a file is already being modified.

When an improvement is useful but outside the active ticket, record it as technical debt instead.

## 9. Completion Criteria

An implementation is not complete merely because the code compiles.

Before declaring a ticket ready for review, confirm:

* acceptance criteria are addressed
* relevant tests pass
* required verification has been executed
* no unintended files changed
* no known blocking issue remains
* remaining non-blocking findings are recorded in the Technical Debt Register

A ticket is ready for commit only after independent review has determined that no blocking findings remain.
