# 24: feat(subserver): serve distinct generic and gemini subscription projections

Type: feature
Status: ready-for-agent
Blocked by: 20, 22, 23

## Problem

The local HTTP Subserver (`internal/subserver`) currently serves only `Store.Passing()` candidates at its configured endpoint (default `/sub`). Consequently, local subscription consumers (such as Throne auto-updating from `http://127.0.0.1:8765/sub`) receive exclusively Gemini application-servable proxies.

However, the Tester and Store layers already maintain two distinct canonical projections:
1. `Store.NetworkPassing()`: Transport-healthy candidates whose Stage 1 probe passed, regardless of Stage 2 Gemini application status (including Gemini PASS, RegionBlocked, TargetDenied, Server/Target error, or fresh Inconclusive).
2. `Store.Passing()`: Fully servable candidates that passed both Stage 1 transport and Stage 2 Gemini application gates.

Ticket 23 resolved this architectural gap for remote delivery (Publisher) by publishing `generic/` and `gemini/` directories to Git. However, Subserver—which is the primary delivery mechanism for local daemons and Throne auto-updates—remains single-projection and completely ignores `Store.NetworkPassing()`. A user running `gemsub` locally without Git publishing cannot access network-healthy generic proxies at all.

Furthermore, `/healthz` only reports `servable: <gemini_count>`, offering zero observability into generic/transport-healthy proxy counts.

## Current Behavior

- `GET /sub`: Slices candidates strictly via `s.st.Passing()`.
- Request query parameters are completely ignored.
- No dedicated projection sub-paths exist.
- Protocol filtering is unsupported.
- `GET /healthz` reports:
  ```text
  ok
  passed: <passed>
  failed: <failed>
  inconclusive: <inconclusive>
  servable: <gemini_servable>
  cycles: <cycle_count>
  ```
  with zero generic servability metrics.

## Desired Behavior

- `GET /sub` (with no query parameters) continues to serve `Store.Passing()` by default, ensuring **100% backward compatibility** for all existing Throne clients and local scripts.
- Query-based projection selection:
  - `GET /sub?projection=generic` serves all transport/network-healthy candidates (`Store.NetworkPassing()`).
  - `GET /sub?projection=gemini` serves Gemini application-servable candidates (`Store.Passing()`).
- Dedicated projection routes mounted under the configured base path:
  - `GET <path>/generic` (e.g. `/sub/generic`) serves `Store.NetworkPassing()`.
  - `GET <path>/gemini` (e.g. `/sub/gemini`) serves `Store.Passing()`.
  - `GET <path>` (e.g. `/sub`) serves the default projection (Gemini), or the projection specified by query parameter.
- Optional protocol filtering across all projection endpoints:
  - `?protocol=vless`, `?protocol=vmess`, `?protocol=trojan` (or `?proto=...`) filters candidates to only the specified scheme.
  - Omitted or empty protocol parameter returns all candidate schemes in the projection.
- Output formatting:
  - Respects configured `format` (`raw` or `base64`), with optional query parameter override `?format=raw` or `?format=base64`.
- Enhanced `/healthz` observability:
  - Preserves all existing metrics and adds `generic_servable`:
    ```text
    ok
    passed: <passed>
    failed: <failed>
    inconclusive: <inconclusive>
    servable: <gemini_servable>
    generic_servable: <generic_servable>
    cycles: <cycle_count>
    ```
  - `Store.Stats()` is extended with `GenericServable int` so `/healthz` reads pre-computed counts without redundant candidate iteration or sorting.

## Architecture Boundaries

```text
               ┌────────────────────────┐
               │      Store State       │
               │                        │
               │  Store.NetworkPassing  │
               │     Store.Passing      │
               │      Store.Stats       │
               └───────────┬────────────┘
                           │
                           ▼
               ┌────────────────────────┐
               │    subserver.Server    │
               │                        │
               │  - handleSub           │
               │    * projection router │
               │    * protocol filter   │
               │    * format encoder    │
               │  - handleHealth        │
               │    * dual counts       │
               └───────────┬────────────┘
                           │
              HTTP / 127.0.0.1:8765
                           │
       ┌───────────────────┴───────────────────┐
       ▼                                       ▼
  Throne / Local VPN Clients             Monitoring / Health Checks
  - /sub              (default Gemini)   - /healthz (dual metrics)
  - /sub?projection=generic
  - /sub/generic
  - /sub/gemini
```

- Subserver remains a pure presentation/serving boundary: it consumes Store projections directly and performs zero candidate classification, scoring, or policy gate evaluation.
- Store remains the sole source of truth for candidate health and projection membership.

## Exact Files/Modules Expected to Change

- `internal/subserver/server.go`: Routing, projection selection, protocol filtering, and `/healthz` metrics.
- `internal/subserver/server_test.go`: Comprehensive hermetic integration tests.
- `internal/store/store.go`: Additive `GenericServable int` field on `Stats struct`, populated in `Store.Stats()`.
- `internal/store/store_test.go`: Verify `GenericServable` count in `Store.Stats()`.

## Explicit Non-Goals

- Do NOT modify Store scoring, recency decay, or servability gate evaluation logic.
- Do NOT modify Tester, probe execution, or sing-box configurations.
- Do NOT modify Publisher or Git repository publishing logic.
- Do NOT modify TUI or viewmodels in this ticket (handled in a separate TUI observability ticket).
- Do NOT introduce a generic Target abstraction.
- Do NOT break backward compatibility for existing `/sub` consumers.

## Acceptance Criteria

- [ ] `GET /sub` (no query params) returns identical output to pre-Ticket-24 behavior (`Store.Passing()`).
- [ ] `GET /sub?projection=gemini` and `GET /sub/gemini` return Gemini-servable candidates (`Store.Passing()`).
- [ ] `GET /sub?projection=generic` and `GET /sub/generic` return all transport-healthy candidates (`Store.NetworkPassing()`).
- [ ] Candidates with Stage 1 Transport OK and Gemini RegionBlocked/TargetDenied appear in generic responses and are absent from Gemini responses.
- [ ] Candidates with Stage 1 Transport failures appear in neither projection response.
- [ ] Protocol filtering (`?protocol=vless`, `vmess`, `trojan` or `?proto=...`) restricts response to the specified scheme; invalid/unmatched schemes return an empty list without crashing.
- [ ] `GET /healthz` reports both `servable:` (Gemini) and `generic_servable:` (Network-healthy) counts while preserving all existing lines.
- [ ] Both plain-text (`raw`) and `base64` formats are supported across all projection routes and query combinations.
- [ ] Subserver graceful shutdown remains clean with 0 leaked goroutines or open listeners.

## Test Matrix

| Test Case | Request | Candidate Conditions in Store | Expected Output |
|---|---|---|---|
| Default Route Backward Compat | `GET /sub` | Mixed pool: 1 Gemini pass, 1 RegionBlocked, 1 Transport fail | Only the Gemini pass candidate |
| Query Projection Generic | `GET /sub?projection=generic` | Mixed pool: 1 Gemini pass, 1 RegionBlocked, 1 Transport fail | Both Gemini pass and RegionBlocked candidates; Transport fail excluded |
| Query Projection Gemini | `GET /sub?projection=gemini` | Mixed pool: 1 Gemini pass, 1 RegionBlocked, 1 Transport fail | Only the Gemini pass candidate |
| Dedicated Route Generic | `GET /sub/generic` | Mixed pool: 1 Gemini pass, 1 RegionBlocked, 1 Transport fail | Both Gemini pass and RegionBlocked candidates |
| Dedicated Route Gemini | `GET /sub/gemini` | Mixed pool: 1 Gemini pass, 1 RegionBlocked, 1 Transport fail | Only the Gemini pass candidate |
| Protocol Filtering | `GET /sub/generic?protocol=vless` | Generic pool with 1 vless, 1 vmess, 1 trojan | Only the vless candidate |
| Empty Projection | `GET /sub/gemini` | Zero Gemini-servable candidates | Empty 200 OK response (or empty base64 string) |
| Format Override | `GET /sub?format=raw` & `?format=base64` | Configured format is opposite | Correctly encoded raw text or base64 |
| Healthz Metrics | `GET /healthz` | 2 generic candidates, 1 Gemini candidate | Contains `servable: 1` and `generic_servable: 2` |
| Context Cancellation / Shutdown | Server cancellation | Running server | Graceful shutdown within timeout, port released |

## Compatibility Requirements

1. **Throne Auto-Update:** Existing Throne subscriptions targeting `http://<host>:<port>/sub` must continue receiving Gemini-servable candidates without any configuration update.
2. **Format Default:** If `cfg.Serve.Format` is `base64`, `/sub`, `/sub/generic`, and `/sub/gemini` default to base64 encoding unless explicitly overridden.
3. **Healthz Monitoring:** Existing health monitoring scrapers looking for `ok`, `passed:`, `failed:`, `servable:`, and `cycles:` continue parsing without failure due to the additive nature of `generic_servable:`.

## Dependencies

- **Ticket 20:** `Store.NetworkPassing()` and in-memory transport evidence.
- **Ticket 22:** Two-stage probe integration and invariant verification.
- **Ticket 23:** Authoritative dual-projection domain semantics established for Publisher.

## Implementation Notes

- Use `http.ServeMux` with exact prefix handling. When registering `cfg.Serve.Path` (e.g. `/sub`), also register `<path>/` or separate handlers for `<path>/generic` and `<path>/gemini` so both exact match and sub-paths route cleanly.
- Keep protocol filtering helper simple and consistent with `publisher.partitionLinks` logic (`strings.HasPrefix(link, proto + "://")`).
- `Store.Stats()` can compute `GenericServable` inside its existing `s.records` iteration loop using `s.isNetworkHealthyRecordLocked(rec)` with minimal CPU overhead.
