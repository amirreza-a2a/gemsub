# 31: feat(parser): support Hysteria2 and TUIC subscription links

Type: feature
Status: ready-for-agent
Priority: P1
Area: Parser / Transport Protocols
Blocked by: none
Research Reference: `docs/research/hysteria2-tuic-parser.md`

## Problem

`gemsub` tests subscription proxy candidates using an embedded `sing-box` instance (`v1.14.0`). Currently, `internal/parser/parser.go` only supports four protocols: `vmess://`, `vless://`, `trojan://`, and `ss://`.

In a census of 64,452 proxy candidates harvested across all 10 upstream subscription sources configured in `config.json` and `config.example.json`, **472 valid candidates** are discarded with `unsupported scheme in ...`:
- `hysteria2://`: 430 candidates (0.67%)
- `hy2://` (alias): 38 candidates (0.06%)
- `tuic://`: 4 candidates (0.01%)

Adding support for Hysteria 2 and TUIC expands candidate pool coverage from 99.22% to 99.95%.

Furthermore, `gemsub`'s `Makefile` currently builds and tests only with `-tags with_utls`:
```make
build:
	go build -tags with_utls -ldflags "-s -w" -o $(BINARY) ./cmd/gemsub

test:
	go test -tags with_utls -count=1 ./...
```
In sing-box v1.14.0, `include/quic.go` (which registers both `hysteria2` and `tuic` outbound constructors via `registerQUICOutbounds`) is gated behind `//go:build with_quic`. Without `-tags with_quic`, sing-box compiles `include/quic_stub.go` where QUIC outbound constructors are omitted. Consequently, even with a completely valid parsed outbound struct from `internal/parser`, constructing a sing-box instance via `box.New(ctx, options)` will fail at runtime with an unregistered outbound type error (`"unknown outbound type: hysteria2"` / `"unknown outbound type: tuic"`).

## Current Behavior

- `Parse(link)` returns `unsupported scheme in <link>` for all `hysteria2://`, `hy2://`, and `tuic://` links.
- `Makefile` lacks the `with_quic` build tag.
- Quota of 472 high-performance QUIC candidates in upstream sources cannot be tested or served.

## Desired Behavior

1. **Makefile Build Tags**:
   Both `build` and `test` targets in `Makefile` include `-tags "with_utls with_quic"`.
2. **Hysteria 2 Scheme Support (`hysteria2://` and `hy2://`)**:
   - `Parse(link)` recognizes both `hysteria2://` and `hy2://`.
   - Query string HTML-entity unescaping: replaces `&amp;` with `&` prior to query parsing so parameters such as `sni` (as seen in Fixture H3) are extracted cleanly rather than corrupted into `amp;sni`.
   - Populates `option.Outbound` with `Type: "hysteria2"` and `Hysteria2Options: option.Hysteria2OutboundOptions`.
   - Default port is `443` if omitted from authority.
   - Userinfo parsed into `Password`: supports single password and `<user>:<password>` (passed composite-intact to sing-box). Query parameter `?auth=` serves as fallback.
   - Port hopping ranges in authority or query (`?mport=...`, `?ports=...`) using hyphen notation (e.g. `20000-50000`) are normalized to colon notation (`20000:50000`) before assignment to `ServerPorts badoption.Listable[string]`. Single ports populate `ServerPort uint16`.
   - Obfuscation: `obfs` and `obfs-password` map to `Obfs.Type` and `Obfs.Password`.
   - TLS options: `sni` (defaults to host), `insecure` / `allow_insecure` (bool), `pinSHA256` (`CertificatePublicKeySHA256`), `alpn` map to embedded `TLS *option.OutboundTLSOptions`.
3. **TUIC Scheme Support (`tuic://`)**:
   - `Parse(link)` recognizes `tuic://`.
   - Populates `option.Outbound` with `Type: "tuic"` and `TUICOptions: option.TUICOutboundOptions`.
   - Strictly enforces TUIC v5: requires `uuid:password` userinfo where UUID is a valid RFC 4122 UUID. Rejects legacy single-token (v4) or invalid UUIDs with descriptive error: `"tuic: invalid userinfo: expected <uuid>:<password>"`.
   - Maps `congestion_control` (canonical: `bbr`, `cubic`, `new_reno`; tolerates `congestion-controller`).
   - Maps `udp_relay_mode` (`native`, `quic`), `zero_rtt_handshake`, `heartbeat`.
   - Maps TLS configuration to embedded `OutboundTLSOptionsContainer.TLS`: `sni` (defaults to host), `allow_insecure` / `insecure`, and `alpn`.
   - ALPN maps to `opts.TLS.ALPN` (`badoption.Listable[string]`), defaulting to `["h3"]`.
   - Handles `disable_sni` (`1` or `true`) by clearing SNI.
4. **End-to-End Validation**:
   - Outbounds created by `internal/parser` instantiate successfully in a real sing-box `Box` under `-tags "with_utls with_quic"`.

## Architecture Boundaries

```text
Subscription Sources
        │
        ▼
internal/parser/parser.go  ── (Parses URI & Query Params into option.Outbound)
        │
        ▼
internal/tester            ── (Instantiates sing-box Box; dials proxy endpoint)
```

- `internal/parser`: Sole owner of share link URI parsing and sing-box outbound option struct generation.
- `Makefile`: Configures compiler build tags (`with_utls`, `with_quic`) for binary and test execution.
- `internal/tester`, `internal/store`, `internal/subserver`, `internal/publisher`: Unchanged. They consume the canonical `Candidate{ Outbound: ... }` model without protocol-specific branching.

## Scope & Non-Goals

### In Scope
- Makefile update: add `with_quic` tag to `build` and `test` targets.
- Parser implementation in `internal/parser/parser.go` for:
  - `hysteria2://` and `hy2://`
  - `tuic://` (v5)
- Query parameter normalization and alias toleration documented in `docs/research/hysteria2-tuic-parser.md`.
- Unit test fixtures in `internal/parser/parser_test.go` matching authentic upstream links.
- Integration test instantiating a real sing-box `Box` with parsed Hysteria2 and TUIC candidates to guarantee constructor registration under `with_quic`.

### Non-Goals
- `hysteria2+realm://`: Excluded (0 occurrences in 64k links, conflicts structurally with `Server`/`ServerPort` in sing-box, incompatible with 4s probe timeout).
- TUIC v4 (token-only): Excluded (deprecated, 0 occurrences, unsupported by sing-box).
- ShadowsocksR (`ssr://`): Excluded (obsolete).
- Modifying `internal/tester` probe mechanics or timeouts.
- Modifying `internal/store` classification, scoring, or projection logic.

## Acceptance Criteria

- [ ] `Makefile`: `build` target includes `-tags "with_utls with_quic"`.
- [ ] `Makefile`: `test` target includes `-tags "with_utls with_quic"`.
- [ ] `Parse("hysteria2://...")` and `Parse("hy2://...")` correctly produce `option.Outbound` with `Type: "hysteria2"` and populated `Hysteria2Options`.
- [ ] Hysteria 2 port hopping ranges using hyphens (e.g. `20000-50000` or `?mport=20000-50000`) normalize to colons (`20000:50000`) in `ServerPorts`.
- [ ] Hysteria 2 userpass authentication (`<user>:<password>`) passes composite-intact to `Hysteria2OutboundOptions.Password`.
- [ ] Query string normalization unescapes HTML entity `&amp;` to `&` prior to query parsing so parameters (such as `sni` in Fixture H3) parse cleanly without key corruption.
- [ ] `Parse("tuic://...")` correctly produces `option.Outbound` with `Type: "tuic"` and populated `TUICOptions`.
- [ ] TUIC parser strictly requires valid RFC 4122 UUID and password; rejects legacy single-token v4 links with exact error `"tuic: invalid userinfo: expected <uuid>:<password>"`.
- [ ] TUIC ALPN correctly maps to embedded `TLS.ALPN` on `OutboundTLSOptionsContainer`, defaulting to `["h3"]`.
- [ ] Unit test suite in `internal/parser/parser_test.go` verifies all real fixtures from `docs/research/hysteria2-tuic-parser.md` (H1–H4, T1–T3).
- [ ] Integration test verifies that a real sing-box `Box` (`box.New(ctx, options)`) instantiates successfully with parsed Hysteria2 and TUIC outbounds under `-tags "with_utls with_quic"`.
- [ ] `make test` passes cleanly.
- [ ] `go test -tags "with_utls with_quic" -race ./...` passes with zero race warnings.
- [ ] `git diff --check` passes with zero whitespace errors.

## Verification Requirements

1. `make test`
2. `go test -tags "with_utls with_quic" -v ./internal/parser/...`
3. `go test -tags "with_utls with_quic" -race ./...`
4. `make build && ./gemsub -help` (verifies binary compiles and links cleanly)
5. `git diff --check`
