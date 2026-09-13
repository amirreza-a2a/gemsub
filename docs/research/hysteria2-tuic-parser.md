# Research & Architecture Decision: Hysteria2 & TUIC Support for `internal/parser`

| Metadata | Details |
| :--- | :--- |
| **Document Target** | `/home/amirreza-a2a/gemsub/docs/research/hysteria2-tuic-parser.md` |
| **Status** | Approved Research & Architecture Decision |
| **Upstream Sing-Box Target** | `github.com/sagernet/sing-box v1.14.0` |
| **Authoritative Upstream Reference** | `/home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-box@v1.14.0/` |
| **Target Implementation Package** | `internal/parser` (`internal/parser/parser.go`) |

---

## 1. Executive Summary & Scope Sizing

`gemsub` tests subscription proxy candidates against transport endpoints and Gemini web properties using an embedded, in-process `sing-box` instance (`github.com/sagernet/sing-box v1.14.0`). Currently, `internal/parser/parser.go` only supports four legacy and modern proxy schemes: `vmess://`, `vless://`, `trojan://`, and `ss://`.

QUIC-based next-generation transport protocols—specifically **Hysteria 2** (`hysteria2://`, `hy2://`) and **TUIC** (`tuic://`)—have experienced widespread adoption due to their UDP-based congestion control, 0-RTT handshakes, native port-hopping resilience, and superior behavior under adversarial packet loss.

### Empirical Source Analysis

To establish empirical scope and prioritization, a complete harvest and deduplication was performed across all 10 upstream subscription sources configured in `config.json` and `config.example.json` (harvest executed 2026-09-13).

```text
Total Upstream Sources Polled:       10
Total Unique Parsed Proxy Links:     64,452
```

#### Protocol Distribution Breakdown

| Protocol / Scheme | Unique Candidate Count | Percentage of Total Pool | Current `gemsub` Status |
| :--- | :---: | :---: | :--- |
| **`vless://`** | 38,590 | 59.87% | Supported (`parseURIStyle`) |
| **`trojan://`** | 8,880 | 13.78% | Supported (`parseURIStyle`) |
| **`ss://`** | 8,700 | 13.50% | Supported (`parseShadowsocks`) |
| **`vmess://`** | 7,777 | 12.07% | Supported (`parseVMess`) |
| **`hysteria2://`** | 430 | 0.67% | **Unsupported (Candidate for addition)** |
| **`hy2://`** (alias) | 38 | 0.06% | **Unsupported (Candidate for addition)** |
| **`ssr://`** | 28 | 0.04% | Obsolete ShadowsocksR (Excluded) |
| **`tuic://`** | 4 | 0.01% | **Unsupported (Candidate for addition)** |
| *Metadata / Comments* | 5 | 0.00% | Subscription header comments (Ignored) |
| **Total** | **64,452** | **100.00%** | **Current Coverage: 99.22%** |

#### Key Takeaways

1. **Hysteria 2 is active and established**: With 468 unique candidates across `hysteria2://` (430) and `hy2://` (38), Hysteria 2 constitutes the single largest unsupported protocol family in the candidate pool. Adding Hysteria 2 parser support immediately unlocks 468 high-performance UDP/QUIC candidates.
2. **Both `hysteria2` and `hy2` schemes exist in the wild**: 8.1% of Hysteria 2 candidates use the short `hy2://` scheme alias. A parser must treat `hy2://` as a direct synonym for `hysteria2://`.
3. **TUIC presence in free public aggregates is emerging**: While currently representing 4 links in these specific bulk aggregate subscriptions, TUIC v5 is heavily deployed in private and premium providers due to its low CPU overhead and high performance. Support is straightforward because sing-box has native TUIC v5 outbound support.
4. **Combined Coverage Expansion**: Adding Hysteria 2 (`hysteria2://`, `hy2://`) and TUIC (`tuic://`) increases theoretical candidate coverage from 99.22% to **99.95%** of all valid links across all sources.

---

## 2. Hysteria 2 Specification & Parameter Decision

### 2.1. Primary Source Specification

* **Official Documentation**: [Hysteria 2 URI Scheme](https://v2.hysteria.network/docs/developers/URI-Scheme/)
* **Official Repository**: [apernet/hysteria](https://github.com/apernet/hysteria)
* **Sing-box Source Reference**:
  * Options: [`option.Hysteria2OutboundOptions`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-box@v1.14.0/option/hysteria2.go#L202-L219)
  * Outbound Adapter: [`protocol/hysteria2/outbound.go`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-box@v1.14.0/protocol/hysteria2/outbound.go#L48-L164)
  * Port Parsing: [`sing-quic/hysteria/client.go:ParsePorts`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-quic@v0.7.0-beta.4/hysteria/client.go#L115-L145)

### 2.2. URI Format & Schemes

The official Hysteria 2 URI format is defined as:
```text
hysteria2://[auth@]hostname[:port]/?[key=value]&[key=value]...[#name]
hy2://[auth@]hostname[:port]/?[key=value]&[key=value]...[#name]
```

#### Authority & Authentication
* **Scheme**: Either `hysteria2://` or `hy2://`. Both must be dispatched to the same parsing function.
* **Authentication**: Specified in the `auth` (userinfo) component before the `@`.
  * *Standard*: A single password string (e.g., `hysteria2://secret_password@server.com:443`).
  * *Userpass*: Formatted as `<username>:<password>` when the server enables multi-user userpass authentication (e.g., `hysteria2://alice:secret_password@server.com:443`).
  * *Sing-box Behavior*: Sing-box's `Hysteria2OutboundOptions.Password` field receives the raw password string. For `userpass`, sing-box treats the entire `<username>:<password>` composite string as the `password` field (documented in sing-box's [hysteria2.md](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-box@v1.14.0/docs/configuration/outbound/hysteria2.md#L65-L72): *"The official Hysteria2 supports an authentication method called userpass, which essentially uses a combination of `<username>:<password>` as the actual password, while sing-box does not provide this alias. Fill the combination as the password"*).
  * *Query Parameter Fallback*: If the userinfo component is missing, some subscription tools emit `?auth=<password>`. The parser should inspect `q.Get("auth")` as a fallback.

#### Port & Port Hopping (`mport` / `server_ports`)
* **Default Port**: If port is omitted from the authority, it defaults to `443` ([Hysteria 2 Spec](https://v2.hysteria.network/docs/developers/URI-Scheme/#hostname)).
* **Multi-Port Format**:
  * In Hysteria 2, port hopping specifies multiple ports or port ranges:
    1. In the authority: e.g., `hostname:1234,5000-6000,7000`
    2. In a query parameter: e.g., `?mport=20000-50000` or `?ports=20000-50000`
  * **Sing-box Representation**:
    * `option.Hysteria2OutboundOptions` contains `ServerPort uint16` (used when a single port is dialed) and `ServerPorts badoption.Listable[string]` (used when port hopping is configured).
    * `sing-quic`'s [`ParsePorts`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-quic@v0.7.0-beta.4/hysteria/client.go#L118-L121) strictly requires colon-delimited port ranges: `"20000:50000"` (or single ports `"443"`).
    * **Normalizer Rule**: If a port range is detected using hyphen syntax (e.g. `20000-50000` in authority or `?mport=20000-50000`), it MUST be normalized to colon notation `20000:50000` before assigning to `ServerPorts`. If only a single port is present, populate `ServerPort` (and leave `ServerPorts` empty).

### 2.3. Query Parameters & Normalization Matrix

| Query Parameter | Target Field in `Hysteria2OutboundOptions` | Type / Value Mapping | Details & Tolerances |
| :--- | :--- | :--- | :--- |
| `sni` | `TLS.ServerName` | `string` | SNI for TLS. Defaults to `hostname` if omitted. |
| `insecure` / `allow_insecure` / `allowInsecure` | `TLS.Insecure` | `bool` | Tolerates `"1"`, `"true"`, `"yes"`. Default is `false`. |
| `obfs` | `Obfs.Type` | `string` (`"salamander"`, `"gecko"`) | QUIC obfuscation type. |
| `obfs-password` | `Obfs.Password` | `string` | Required if `obfs` is non-empty. |
| `pinSHA256` | `TLS.CertificatePublicKeySHA256` | `badoption.Listable[string]` | Certificate pinned SHA-256 fingerprint. Hex string. |
| `alpn` | `TLS.ALPN` | `badoption.Listable[string]` | Comma-separated or repeated. In sing-box, defaults to `["h3"]` if omitted. |
| `ech` | `TLS.ECH.Config` | `badoption.Listable[string]` | Base64-encoded ECH config list. |
| `mport` / `ports` | `ServerPorts` | `badoption.Listable[string]` | Port hopping list; normalize hyphens `-` to colons `:`. |
| `auth` | `Password` | `string` | Fallback if URI authority has no userinfo. |

### 2.4. `hysteria2+realm://` Architectural Decision

#### What is Hysteria Realm?
Hysteria Realm is a rendezvous / UDP hole-punching protocol introduced in Hysteria 2.5.0 and sing-box 1.14.0 ([Hysteria Realm Documentation](https://v2.hysteria.network/docs/advanced/Realms/)). It is designed for server operators whose Hysteria 2 servers sit behind symmetric or restricted NAT without public IP addresses or port-forwarding privileges.
The URI format is:
```text
hysteria2+realm://<token>@<rendezvous-host>[:port]/<realm-name>?auth=<password>&stun=<stun-server>...
```

#### Sing-box Incompatibility & Pipeline Conflicts
1. **Structural Conflict**: In sing-box (`option.Hysteria2OutboundOptions`), setting `Realm` explicitly conflicts with `Server`, `ServerPort`, and `ServerPorts`. Sing-box throws an explicit initialization error if both are populated:
   [`protocol/hysteria2/outbound.go:171`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-box@v1.14.0/protocol/hysteria2/outbound.go#L171):
   ```go
   if options.Server != "" || options.ServerPort != 0 || len(options.ServerPorts) > 0 {
       return "", tlsOptions, E.New("realm conflicts with server, server_port, and server_ports")
   }
   ```
2. **Operational Conflict with `gemsub` Probing Model**:
   * `gemsub`'s probe engine dials each candidate with a strict `dial_timeout` (default 4 seconds).
   * Realm rendezvous requires querying a remote HTTP rendezvous service, querying multiple external STUN servers, performing gateway UPnP/NAT-PMP mapping, and coordinating UDP hole punching before any QUIC handshake can begin.
   * This adds hundreds or thousands of milliseconds of non-deterministic latency and external dependency on STUN servers, making fast automated probing unstable.
3. **Empirical Absence**:
   * Across all 64,452 polled links, `hysteria2+realm://` occurred **0 times** (0.00%).

#### Decision: Exclude `hysteria2+realm://` from Parser Scope
* **Scope**: Do not implement `hysteria2+realm://`.
* **Behavior**: Links with scheme `hysteria2+realm://` or `hy2+realm://` will return `unsupported scheme`.
* **Risk**: Zero risk of excluding legitimate subscription proxies, zero regression impact, prevents high-complexity stateful rendezvous code from polluting `internal/parser`.

---

## 3. TUIC Specification & Parameter Decision

### 3.1. Primary Source Specification

* **Official Protocol Repository**: [tuic-protocol/tuic](https://github.com/tuic-protocol/tuic)
* **Official Specification**: [SPEC.md (v5)](https://github.com/tuic-protocol/tuic/blob/master/SPEC.md)
* **Legacy v4 Implementation**: [`tuic-protocol/tuic` branch `impl-0.8`](https://github.com/tuic-protocol/tuic/tree/impl-0.8)
* **De-facto URI Standard Discussion**: [daeuniverse/dae Discussion #182](https://github.com/daeuniverse/dae/discussions/182)
* **Client Reference Implementations**:
  * [v2rayN `TuicFmt.cs`](https://github.com/2dust/v2rayN/blob/master/v2rayN/ServiceLib/Handler/Fmt/TuicFmt.cs)
  * [3x-ui `service_tuic_test.go`](https://github.com/MHSanaei/3x-ui/blob/main/internal/sub/service_tuic_test.go)
  * [Nekoray `Link2Bean.cpp`](https://github.com/MatsuriDayo/nekoray/blob/main/fmt/Link2Bean.cpp#L259-L277)
  * [Mihomo `adapter/outbound/tuic.go`](https://github.com/MetaCubeX/mihomo/blob/Alpha/adapter/outbound/tuic.go#L36-L65)
* **Sing-box Source Reference**:
  * Options: [`option.TUICOutboundOptions`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-box@v1.14.0/option/tuic.go#L22-L35)
  * Protocol Implementation: [`sing-quic/tuic/protocol.go`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-quic@v0.7.0-beta.4/tuic/protocol.go#L3-L5)
  * Outbound Adapter: [`protocol/tuic/outbound.go`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-box@v1.14.0/protocol/tuic/outbound.go#L42-L98)

### 3.2. TUIC v4 vs TUIC v5 Authentication & Architecture

The TUIC protocol underwent a breaking architectural change between version 4 (`0x04`) and version 5 (`0x05`):

| Protocol Attribute | TUIC v4 (`0x04`) | TUIC v5 (`0x05`) |
| :--- | :--- | :--- |
| **Status** | Obsolete (deprecated June 2023) | Current standard ([SPEC.md](https://github.com/tuic-protocol/tuic/blob/master/SPEC.md)) |
| **Authentication Wire Command** | `0x00` Authenticate: `[TKN: 32 bytes]` | `0x00` Authenticate: `[UUID: 16 bytes][TOKEN: 32 bytes]` |
| **Credentials Required** | Single Token / Password (hashed with BLAKE3) | **UUID** (128-bit RFC 4122) **AND** **Password** |
| **Key Derivation** | BLAKE3 hash of user token | [RFC 5705](https://www.rfc-editor.org/rfc/rfc5705) TLS Keying Material Exporter: label = `UUID`, context = `password` |
| **Sing-box Support** | **None** (Not supported in sing-box) | **Native** (`sing-quic/tuic` hardcodes `const Version = 5`) |
| **Sing-box UUID Constraint** | N/A | Strictly enforces valid UUID via `uuid.FromString(options.UUID)` |
| **Real Subscription Links** | 0% (0 / 4 observed) | **100%** (4 / 4 observed) |

#### Sing-box Validation Requirement
In [`protocol/tuic/outbound.go:51-54`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-box@v1.14.0/protocol/tuic/outbound.go#L51-L54):
```go
userUUID, err := uuid.FromString(options.UUID)
if err != nil {
    return nil, E.Cause(err, "invalid uuid")
}
```
Sing-box will fail to instantiate the outbound box if `options.UUID` is not a syntactically valid RFC 4122 UUID.

#### Decision: TUIC v5 Exclusivity
* The parser will strictly require a valid UUID and password for TUIC.
* If a link provides a single token or non-UUID credentials (legacy v4 format), the parser will reject it with a clear, descriptive error (`"tuic: invalid userinfo: expected <uuid>:<password>"`).

### 3.3. Standard De-facto URI Format for TUIC

Established in [daeuniverse/dae Discussion #182](https://github.com/daeuniverse/dae/discussions/182) and implemented in v2rayN, 3x-ui, and Nekoray:
```text
tuic://<uuid>:<password>@<hostname>:<port>/?congestion_control=<cc>&udp_relay_mode=<mode>&alpn=<alpn>&sni=<sni>&allow_insecure=<0|1>#<remark>
```

### 3.4. Query Parameter Normalization Matrix

| Query Parameter | Canonical Name | Tolerated Aliases | Target in `TUICOutboundOptions` | Default Fallback |
| :--- | :--- | :--- | :--- | :--- |
| Congestion Control | `congestion_control` | `congestion-controller` | `CongestionControl` (`string`) | `"cubic"` (or omitted, sing-box default) |
| UDP Relay Mode | `udp_relay_mode` | `udp-relay-mode` | `UDPRelayMode` (`string`: `"native"` or `"quic"`) | `"native"` |
| UDP Over Stream | `udp_over_stream` | `udp-over-stream` | `UDPOverStream` (`bool`) | `false` (conflicts with `udp_relay_mode`) |
| Zero-RTT Handshake | `zero_rtt_handshake` | `reduce_rtt`, `zero-rtt-handshake` | `ZeroRTTHandshake` (`bool`) | `false` |
| Heartbeat | `heartbeat` | `heartbeat-interval` | `Heartbeat` (`badoption.Duration`) | `10s` (sing-box default) |
| Server Name (SNI) | `sni` | `peer` | `TLS.ServerName` (`string`) | `hostname` |
| Disable SNI | `disable_sni` | `disable-sni` | `TLS.ServerName = ""` | If `1` or `true`, forces empty SNI |
| Insecure TLS | `allow_insecure` | `insecure`, `allowInsecure`, `skip-cert-verify` | `TLS.Insecure` (`bool`) | `false` |
| ALPN | `alpn` | - | `TLS.ALPN` (`badoption.Listable[string]`) | `["h3"]` |

#### ALPN & TLS Container Mapping Detail
In sing-box's struct hierarchy, `alpn` does **not** map to a top-level field on `TUICOutboundOptions`. Instead, `TUICOutboundOptions` embeds `OutboundTLSOptionsContainer`, which houses `TLS *OutboundTLSOptions`.
* In `OutboundTLSOptions`, ALPN is defined as `ALPN badoption.Listable[string]`.
* The parser must initialize `opts.TLS = &option.OutboundTLSOptions{ Enabled: true, ... }`.
* If query parameter `alpn` is present (e.g. `alpn=h3`), parse and populate `opts.TLS.ALPN = badoption.Listable[string]{"h3"}` (or split comma-separated values if present).
* If omitted, default `opts.TLS.ALPN` to `badoption.Listable[string]{"h3"}` because TUIC operates over HTTP/3 (QUIC) and requires ALPN negotiation.

### 3.5. Key Parsing & Sing-box Mapping Rules

1. **CRITICAL: Makefile Build Tag Prerequisite (`with_quic`)**:
   * Both `build` and `test` targets in `Makefile` MUST add `-tags with_quic` alongside `with_utls` (`-tags "with_utls with_quic"`).
   * This is a **hard prerequisite** for the ticket, not optional cleanup.
   * *Rationale*: In sing-box, `include/quic.go` (which registers the `hysteria2` and `tuic` outbound constructors via `registerQUICOutbounds`) is gated behind `//go:build with_quic`. Without this build tag, sing-box compiles `include/quic_stub.go` instead, omitting QUIC outbounds from the registry. Even with a completely valid parsed outbound struct from `internal/parser`, constructing a sing-box instance via `box.New(ctx, options)` will fail at runtime with an unregistered outbound type error.
2. **End-to-End Verification Requirement**:
   * A parser-only unit test does NOT construct a sing-box `Box` and therefore will NOT catch a missing `with_quic` build tag.
   * An e2e-level acceptance criterion is required that actually constructs a real Box with a parsed Hysteria2 and TUIC candidate (e.g. dialing through `internal/tester` or a dedicated test harness) to verify that sing-box initializes the outbound without runtime errors.
3. **TUIC ALPN Mapping**:
   * `alpn` query parameter routes to `OutboundTLSOptionsContainer -> TLS.ALPN` (`badoption.Listable[string]`), defaulting to `["h3"]`.
4. **Hysteria 2 Port Ranges**:
   * Hyphenated port ranges (e.g. `20000-50000` in authority or `?mport=20000-50000`) must be normalized to colon notation (`20000:50000`) before assigning to `option.Hysteria2OutboundOptions.ServerPorts`.
5. **Hysteria 2 Userpass**:
   * Official Hysteria 2 `<username>:<password>` authentication is passed composite-intact into `Hysteria2OutboundOptions.Password`.
6. **Query String HTML-Entity Unescaping (`&amp;` -> `&`)**:
   * Prior to parsing query parameters via `url.ParseQuery` (or when inspecting `u.RawQuery`), the raw query string MUST normalize HTML-escaped ampersands by replacing `&amp;` with `&`.
   * *Rationale*: Community aggregators and scrapers frequently emit links with HTML entity encoding (`&amp;`). Without normalization, Go's `net/url` parses keys as `amp;sni`, failing to extract `sni` and causing unnecessary fallback. Replacing `&amp;` with `&` ensures Fixture H3 cleanly extracts `ServerName = "vpn-uk-002.fastervpn.world"`.

---

## 4. Specific Research Questions & Empirical Answers

### Question 1: Are there real `hysteria2://`, `hy2://`, or `tuic://` links in upstream sources?
**Answer**: **Yes, emphatically.**
* Real `hysteria2://` links: **430** unique links found.
* Real `hy2://` links: **38** unique links found.
* Real `tuic://` links: **4** unique links found.
* In total, **472** candidate proxy links in the current upstream subscription pool are directly unlocked by implementing Hysteria 2 and TUIC parsers.

### Question 2: What is the total candidate count across all upstream sources, and what is the count and percentage breakdown for each protocol?
**Answer**: Across the 10 sources from `config.json` and `config.example.json`, exactly **64,452** unique candidates were discovered.

```text
Protocol Breakdown:
  - vless:     38,590  (59.87%)
  - trojan:     8,880  (13.78%)
  - ss:         8,700  (13.50%)
  - vmess:      7,777  (12.07%)
  - hysteria2:    430  ( 0.67%)
  - hy2:           38  ( 0.06%)
  - ssr:           28  ( 0.04%)
  - tuic:           4  ( 0.01%)
  - comments:       5  ( 0.00%)
```

### Question 3: In real `tuic://` links found (or in community subscription sources): what parameters do they actually carry? What naming is used?
**Answer**: Every single real `tuic://` link found across the subscription sources adheres 100% to the dae / 3x-ui standard:
* **Authority format**: Always `uuid:password@host:port` where the username portion is a valid RFC 4122 UUID (e.g. `56d8a8fd-68b8-47f8-8c7f-98cc675902db`).
* **Query Parameters observed in 100% of real links**:
  * `congestion_control=bbr` (100%)
  * `udp_relay_mode=native` (100%)
  * `sni=<domain>` (100%)
  * `alpn=h3` (100%)
  * `allow_insecure=1` (50% of links)
* No exotic or legacy parameters were observed in wild subscription links.

### Question 4: Is TUIC v4 (token-based) seen in any real links, or is it exclusively v5 (uuid:password)? Is `hysteria2+realm://` seen anywhere in the sources?
**Answer**:
1. **TUIC v4 presence**: **0%**. Zero token-based TUIC v4 links were found. All observed TUIC links are strictly TUIC v5 (`uuid:password`). Furthermore, sing-box does not support TUIC v4, so any v4 link would fail sing-box outbound instantiation.
2. **`hysteria2+realm://` presence**: **0%**. Exactly **0** occurrences of `hysteria2+realm://` or `hy2+realm://` were detected across 64,452 links.

---

## 5. Real Test Fixture Links (for Unit Tests)

These authentic links collected during empirical analysis should be used directly as test fixtures in `internal/parser/parser_test.go`:

### 5.1. Hysteria 2 Test Fixtures

#### Fixture H1: Standard Hysteria2 with Salamander Obfuscation & Insecure
```text
hysteria2://80747420-96c4-4a2f-83e6-eea4e46beb09@drhystuichdfy.samanidempire.org:20335?insecure=1&sni=drhystuichdfy.samanidempire.org&obfs=salamander&obfs-password=U1wBrYQyFm#⚡ b2n.ir/v2ray-configs | 481
```
* **Expected Outbound Type**: `"hysteria2"`
* **Expected Server**: `"drhystuichdfy.samanidempire.org"`
* **Expected Port**: `20335`
* **Expected Password**: `"80747420-96c4-4a2f-83e6-eea4e46beb09"`
* **Expected Obfs**: Type `"salamander"`, Password `"U1wBrYQyFm"`
* **Expected TLS**: ServerName `"drhystuichdfy.samanidempire.org"`, Insecure `true`

#### Fixture H2: `hy2://` Scheme Alias with Default Port 443 & No Obfs
```text
hy2://f317b5d6-d399-4d3d-a051-89d674ae953c@msk.frkn.org:443/#EPODONIOS
```
* **Expected Outbound Type**: `"hysteria2"`
* **Expected Server**: `"msk.frkn.org"`
* **Expected Port**: `443`
* **Expected Password**: `"f317b5d6-d399-4d3d-a051-89d674ae953c"`
* **Expected Obfs**: `nil`
* **Expected TLS**: ServerName `"msk.frkn.org"`, Insecure `false`

#### Fixture H3: HTML-Entity Encoded Query (`&amp;`) & Fallback Insecure
```text
hysteria2://H7mP2xY9kJ4nQ8wR5tF6vB3z@18.175.236.136:443/?insecure=1&amp;sni=vpn-uk-002.fastervpn.world# By EbraSha 🛜
```
* **Normalization Requirement**: Query string contains `&amp;`, which must be normalized to `&` before query parameter parsing so that `sni` is extracted cleanly rather than corrupted into `amp;sni`.
* **Expected Server**: `"18.175.236.136"`, Port `443`
* **Expected TLS**: ServerName `"vpn-uk-002.fastervpn.world"`, Insecure `true`

#### Fixture H4: Multi-Port / Port Hopping Range
```text
hysteria2://password123@example.com:443,20000-30000/?sni=example.com&insecure=1
```
* **Expected ServerPorts**: `["443", "20000:30000"]` (colon normalized)

---

### 5.2. TUIC Test Fixtures

#### Fixture T1: Standard TUIC v5 with `allow_insecure=1`
```text
tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:56d8a8fd-68b8-47f8-8c7f-98cc675902db@us02.uzifan.monster:8443/?congestion_control=bbr&udp_relay_mode=native&sni=www.bing.com&alpn=h3&allow_insecure=1# By EbraSha 🌌
```
* **Expected Outbound Type**: `"tuic"`
* **Expected Server**: `"us02.uzifan.monster"`
* **Expected Port**: `8443`
* **Expected UUID**: `"56d8a8fd-68b8-47f8-8c7f-98cc675902db"`
* **Expected Password**: `"56d8a8fd-68b8-47f8-8c7f-98cc675902db"`
* **Expected CongestionControl**: `"bbr"`
* **Expected UDPRelayMode**: `"native"`
* **Expected TLS**: ServerName `"www.bing.com"`, Insecure `true`, ALPN `["h3"]`

#### Fixture T2: Standard TUIC v5 with Strict TLS
```text
tuic://9a198016-fdb7-4510-acca-e2c68c1083e5:9a198016-fdb7-4510-acca-e2c68c1083e5@singapore.ruixing.fun:58441/?congestion_control=bbr&udp_relay_mode=native&sni=singapore.ruixing.fun&alpn=h3# By EbraSha 🪐
```
* **Expected Server**: `"singapore.ruixing.fun"`, Port `58441`
* **Expected UUID**: `"9a198016-fdb7-4510-acca-e2c68c1083e5"`
* **Expected Password**: `"9a198016-fdb7-4510-acca-e2c68c1083e5"`
* **Expected TLS**: ServerName `"singapore.ruixing.fun"`, Insecure `false`

#### Fixture T3 (Negative Test): TUIC v4 Token Link (Must Fail Parse)
```text
tuic://token_only_secret@singapore.ruixing.fun:58441/?congestion_control=bbr
```
* **Expected Result**: Error (`"tuic: invalid userinfo: expected <uuid>:<password>"`)

---

## 6. Recommendations for Engineering Work (`/to-tickets`)

When promoting this research to an engineering ticket via `/to-tickets`, the scope must adhere to these defined boundaries:

1. **Hard Prerequisite — Makefile Build Tags (`with_quic`)**:
   * **The Gap**: In sing-box, [`include/quic.go:1`](file:///home/amirreza-a2a/go/pkg/mod/github.com/sagernet/sing-box@v1.14.0/include/quic.go#L1) (which registers both `hysteria2` and `tuic` outbound constructors via `registerQUICOutbounds`) is strictly gated behind the build tag `//go:build with_quic`.
   * **Current State**: Gemsub's [Makefile](file:///home/amirreza-a2a/gemsub/Makefile) only builds and tests with `-tags with_utls`:
     ```make
     build:
         go build -tags with_utls -ldflags "-s -w" -o $(BINARY) ./cmd/gemsub

     test:
         go test -tags with_utls -count=1 ./...
     ```
   * **Failure Mode**: Without `-tags with_quic`, sing-box falls back to `include/quic_stub.go` where QUIC outbound constructors are omitted. Even with a completely valid parsed outbound struct from `internal/parser`, constructing a sing-box instance via `box.New(ctx, options)` will fail at runtime with an unregistered outbound type error (`"unknown outbound type: hysteria2"` / `"unknown outbound type: tuic"`).
   * **Mandatory Fix**: Both `build` and `test` targets in `Makefile` MUST be updated to include `with_quic` alongside `with_utls`:
     ```make
     build:
         go build -tags "with_utls with_quic" -ldflags "-s -w" -o $(BINARY) ./cmd/gemsub

     test:
         go test -tags "with_utls with_quic" -count=1 ./...
     ```
   * This is a hard prerequisite for shipping a functional feature, not optional cleanup.

2. **Parser Implementation (`internal/parser`)**:
   * Add scheme dispatch in `Parse(link string)`:
     * `case strings.HasPrefix(link, "hysteria2://"), strings.HasPrefix(link, "hy2://"):`
     * `case strings.HasPrefix(link, "tuic://"):`
   * Implement `parseHysteria2(link string) (option.Outbound, []string, error)`:
     * Support both `hysteria2://` and `hy2://` prefixes.
     * Normalize query string: replace HTML entity `&amp;` with `&` before query parameter parsing so keys like `sni` are extracted cleanly.
     * Default port 443; extract auth into `Password` (supports single password and `<user>:<password>`).
     * Normalize port ranges from hyphens (e.g. `20000-50000`) to colons (`20000:50000`) for `ServerPorts`.
     * Extract `obfs` and `obfs-password` into `Obfs`.
     * Extract `sni`, `insecure`, `pinSHA256`, `alpn` into `TLS`.
   * Implement `parseTUIC(link string) (option.Outbound, []string, error)`:
     * Strictly enforce TUIC v5: requires `uuid:password` userinfo where UUID is a valid RFC 4122 UUID. Single-token or invalid UUIDs must return error `"tuic: invalid userinfo: expected <uuid>:<password>"`.
     * Normalize query string: replace HTML entity `&amp;` with `&` before query parameter parsing.
     * Map query parameters to `CongestionControl`, `UDPRelayMode`, `ZeroRTTHandshake`, `Heartbeat`.
     * Map `sni`, `allow_insecure` / `insecure`, and `alpn` to embedded `opts.TLS` (`OutboundTLSOptionsContainer.TLS`), defaulting `TLS.ALPN` to `["h3"]`.

3. **Preserve Architectural Invariants (`AGENTS.md`)**:
   * No changes to `internal/store` or projection logic.
   * No changes to `internal/subserver` or `internal/publisher`.
   * Outbounds created by `internal/parser` must feed directly into `internal/tester` without changing probe mechanics.

4. **Verification & Acceptance Criteria**:
   * **Unit tests**: Full coverage for all fixtures (H1–H4, T1–T3) in `internal/parser/parser_test.go`.
   * **End-to-End Box Construction Test**: A parser-only unit test does NOT construct a sing-box `box.New(ctx, options)` and therefore will NOT catch a missing `with_quic` build tag. An integration/e2e test must instantiate a real Box with a parsed Hysteria2 and TUIC candidate (e.g., in `internal/tester` or a dedicated test) to verify that sing-box initializes the outbound without runtime errors.
   * **Build verification**: `make build` and `make test` pass cleanly with `-tags "with_utls with_quic"`.
   * **Race safety**: `go test -tags "with_utls with_quic" -race ./...` passes without regressions.
