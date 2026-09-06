## Destination

Harden the `gemsub` core engine by resolving all critical blockers, concurrency hazards, and architectural bottlenecks identified during the architectural audit, establishing a stable foundation for Reliability Scoring and the Terminal UI.

## Notes

- Domain: VPN proxy testing daemon and subscription server
- Execution mode: Tickets in this map carry implementation
- Tracker convention: Local markdown in `.scratch/audit-remediation/`

## Decisions so far

- [01: fix(scheduler): prevent store wipe on empty fetch](issues/01-prevent-store-wipe-on-empty-fetch.md): Abort runCycle when fetch yields 0 links and errors occurred, preventing store wiping and empty publication.

## Not yet specified

- Reliability Scoring Engine: Sliding-window ring buffer in `store.Result`
- Candidate Deduplication: Upstream IP/Port grouping and round-robin worker dispatching
- Terminal UI: Bubbletea dashboard integration

## Out of scope

- Subscribing to non-standard protocols outside vless/vmess/trojan/ss
- External Web UI dashboard
