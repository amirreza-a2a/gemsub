# 15: audit(pipeline): preserve network-healthy and target-incompatible candidates

Type: audit
Status: ready-for-agent
Blocked by: None (can start immediately)

## Question

If a candidate passes network/proxy health but fails Gemini-specific probing, does the current Source -> Scheduler -> Tester -> Store -> Publisher -> Subserver pipeline retain that candidate, or discard it?

## Required investigation

1. Trace candidate lifecycle through the actual code.
2. Identify the exact point(s) where Gemini failure causes retention, rejection, exclusion, or publication filtering.
3. Distinguish:
   - proxy/network health
   - target compatibility
   - servability for a particular target
4. Determine whether the current Store model already retains enough information to reuse such candidates for other targets.
5. Determine whether Publisher currently filters them out globally or only for the Gemini-oriented published subscription.
6. Propose the minimum future-safe architecture needed to preserve these candidates without prematurely introducing a generic Target abstraction.

## Desired invariant

Gemini failure must not imply that the underlying network configuration is globally dead.

## Acceptance criteria

- [ ] Trace candidate lifecycle through Source -> Scheduler -> Tester -> Store -> Publisher -> Subserver in code.
- [ ] Identify exact point(s) where Gemini failure causes retention, rejection, exclusion, or publication filtering.
- [ ] Document explicit distinction between proxy/network health, target compatibility, and servability for a target.
- [ ] Assess whether current Store model retains enough information to reuse candidates for other targets.
- [ ] Determine whether Publisher filters candidates globally or only for Gemini-oriented published subscription.
- [ ] Deliver minimal future-safe architecture recommendation and migration implications without redesigning unrelated reliability logic.
