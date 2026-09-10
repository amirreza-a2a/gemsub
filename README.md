# gemsub

[English](README.md) | [فارسی](README.fa.md) | [Русский](README.ru.md)

`gemsub` is an automated proxy and subscription validation daemon that ingests raw proxy share links from upstream sources, verifies network transport connectivity and target service accessibility through active probing, and serves reliable, classified configurations over a local HTTP subscription endpoint or Git repository. It is designed to detect regional restrictions, identify blocked or incompatible routes, filter unusable nodes specifically for services requiring verified target accessibility such as Google Gemini, and provide a general-purpose network health projection for standard proxy clients.

---

## Key Features

- **Two-Stage Active Probing:** Combines TCP/TLS/uTLS network health checks with real HTTP application-layer validation against target services (such as Google Gemini).
- **Dual Canonical Projections:** Derives both a strict Gemini-verified projection and a generic transport-passing projection from a single authoritative store.
- **Interactive Terminal UI:** Built with Bubble Tea and Lip Gloss; includes a real-time candidate table, credential masking (`[REDACTED]`), country flags, detail inspect modals, and a ring-buffer log viewer.
- **Headless Daemon Mode:** Lightweight background operation for servers, systemd services, and resource-constrained environments without initializing the TUI.
- **Local HTTP Subscription Server:** Serves auto-updating subscriptions to clients such as Throne, Sing-box, and Clash with on-demand protocol filtering (`?proto=vless`), format selection (`base64` or `raw`), and health endpoints.
- **Automated Git Publishing:** Deterministically exports, stages, and pushes passing configurations (`all.txt`, `vless.txt`, `vmess.txt`, `trojan.txt`, `meta.json`) to a remote Git repository.
- **Persistent State & History:** Retains bounded cycle history, consecutive inconclusive counters, and reliability scoring across restarts with atomic gzip-compressed snapshots.
- **Supported Protocols:** VLESS, VMess, Trojan, and Shadowsocks (`ss://`).

---

## Quick Start

Get started on Linux with a single command:

```bash
# 1. Install gemsub to ~/.local/bin
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash

# 2. Verify installation and version
gemsub --version

# 3. Create a minimal configuration file
cat <<'EOF' > config.json
{
  "sources": [
    "https://raw.githubusercontent.com/AvenCores/goida-vpn-configs/main/githubmirror/1.txt"
  ],
  "fetch_interval": "1h",
  "test": {
    "timeout": "10s",
    "gemini": {
      "block_phrases": [
        "isn't currently supported in your country",
        "not available in your country",
        "not available in your region"
      ]
    }
  },
  "serve": {
    "listen": "127.0.0.1:8765",
    "path": "/sub",
    "format": "base64"
  }
}
EOF

# 4. Start gemsub in interactive TUI mode
gemsub -config config.json
```

Once started, `gemsub` will execute an immediate probe cycle and begin serving verified subscriptions at `http://127.0.0.1:8765/sub`.

---

## Installation

### Linux

#### Automated Installer (Recommended)

The automated Linux installer detects host architecture, downloads the matching release archive, verifies SHA-256 checksum integrity against `checksums.txt`, and stages the binary atomically into `~/.local/bin` without requiring root permissions:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash
```

To install a specific version:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash -s -- -v v0.1.0
```

To install system-wide into `/usr/local/bin` (requires root privileges):

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash -s -- --system
```

#### Manual Archive Installation

1. Download the archive for your architecture from GitHub Releases:
   - `x86_64` (`amd64`): `gemsub_Linux_x86_64.tar.gz`
   - `arm64` (`aarch64`): `gemsub_Linux_arm64.tar.gz`
   - `armv7` (`armhf`): `gemsub_Linux_armv7.tar.gz`
2. Download `checksums.txt` and verify integrity:
   ```bash
   sha256sum --ignore-missing -c checksums.txt
   ```
3. Extract the binary and install it to your user path:
   ```bash
   tar -xzf gemsub_Linux_x86_64.tar.gz
   install -m 755 gemsub ~/.local/bin/gemsub
   ```

#### Linux Packages (.deb / .rpm)

Native system packages install the executable to the package-managed directory `/usr/bin/gemsub` and a configuration template to `/etc/gemsub/config.example.json`.

Debian / Ubuntu:
```bash
sudo dpkg -i gemsub_<version>_linux_<arch>.deb
```

RHEL / Fedora:
```bash
sudo rpm -i gemsub_<version>_linux_<arch>.rpm
```

#### Supported Linux Architectures
- `x86_64` / `amd64` (64-bit Intel/AMD)
- `arm64` / `aarch64` (64-bit ARM)
- `armv7` (32-bit ARMv7)

#### Upgrade Path
Re-running the automated installer script fetches and verifies the latest release, replacing the executable while leaving existing configuration and state files untouched.

---

### Android / Termux

> [!NOTE]
> This is a **Termux-compatible standalone artifact**, not yet an official package in the `termux-packages` upstream repository. Physical-device runtime validation is currently pending; verification has been conducted through hermetic builds and integration tests.

#### Supported Architecture
- `arm64` (`aarch64`) Android devices running Termux.

#### Automated Termux Installation
Execute the dedicated Termux installer script within your Termux shell:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install-termux.sh | bash
```

The installer script:
- Verifies execution inside Termux userspace.
- Validates that the device architecture is 64-bit ARM (`aarch64`).
- Downloads the dedicated `gemsub_Termux_arm64.tar.gz` artifact.
- Validates the SHA-256 digest against `checksums.txt`.
- Installs the executable directly into `$PREFIX/bin/gemsub`.
- Does not require `root` or `sudo` access.

#### Termux Technical Details & Limitations
- **Binary Format:** Compiled as a pure Go Position-Independent Executable (PIE) ELF `DYN` binary targeting `/system/bin/linker64` and Android system DNS.
- **Clipboard Access:** Interactive TUI clipboard copy actions (`y`) require `termux-api` package utilities (`pkg install termux-api`) or running `gemsub` in headless mode (`-headless`).

---

## Usage

### Interactive TUI Mode

Run `gemsub` with a configuration file to launch the interactive terminal interface:

```bash
gemsub -config config.json
```

#### Keybindings & Controls

| Key | Context | Action |
| :--- | :--- | :--- |
| `Up` / `k` | Candidate List | Move selection up |
| `Down` / `j` | Candidate List | Move selection down |
| `PageUp` / `Ctrl+B` | Candidate List | Scroll up one page |
| `PageDown` / `Ctrl+F` | Candidate List | Scroll down one page |
| `Home` / `g` | Candidate List | Jump to top of list |
| `End` / `G` | Candidate List | Jump to bottom of list |
| `Enter` / `Space` | Candidate List | Toggle candidate inspection modal |
| `s` | Candidate List | Toggle filter between all candidates and servable-only |
| `y` | Candidate List | Copy active share link to system clipboard |
| `Tab` | Global | Switch focus between Candidate View and Log View |
| `l` (or `1`-`4`) | Log View | Cycle log level filter: `DEBUG` → `INFO` → `WARN` → `ERROR` |
| `q` / `Ctrl+C` | Global | Gracefully shut down daemon, persist state, and exit |

Minimum required terminal size is **80 columns × 24 rows**.

#### Credential Masking
For operational security, sensitive credentials (passwords, private UUIDs, secret query tokens) are automatically redacted in the TUI view (e.g. `vless://[REDACTED]@host:port`). When copying with `y`, the full unmasked share link is copied to the system clipboard.

---

### Headless Mode

For background services, Linux servers, systemd units, or unattended Termux environments:

```bash
gemsub -headless -config config.json
```

In headless mode:
- The TUI is not initialized.
- Structured log records are written directly to standard error (`stderr`).
- The process runs until terminated by `SIGINT` or `SIGTERM`, ensuring all background workers drain and state is saved before exit.

---

### Command-Line Flags

```text
Usage of gemsub:
  -config string
        path to config file (default "./config.json")
  -flag-mode string
        country flag presentation mode: auto, unicode, ascii
  -headless
        run without the TUI (daemon + sub server only)
  -limit int
        limit number of parsed candidates to probe per cycle (0 = unlimited)
  -publish
        enable git publishing after test cycles (overrides config)
  -v	print version information and exit (shorthand)
  -version
        print version information and exit
```

---

## Configuration

The configuration file is JSON-formatted. A template is provided in `config.example.json`. Values shown in the example are illustrative and may differ from the built-in defaults listed in the reference table below.

```json
{
  "sources": [
    "https://raw.githubusercontent.com/AvenCores/goida-vpn-configs/main/githubmirror/1.txt"
  ],
  "fetch_interval": "3h",
  "test": {
    "health_url": "https://www.gstatic.com/generate_204",
    "health_timeout": "4s",
    "gemini": {
      "url": "https://gemini.google.com/",
      "block_phrases": [
        "isn't currently supported in your country",
        "not available in your country",
        "not available in your region"
      ]
    },
    "timeout": "10s",
    "dial_timeout": "4s",
    "concurrency": 40,
    "rate_limit_rps": 20,
    "max_retries": 2,
    "retry_backoff": "1s",
    "max_inconclusive_cycles": 2
  },
  "serve": {
    "listen": "127.0.0.1:8765",
    "path": "/sub",
    "format": "base64"
  },
  "publishing": {
    "enabled": false,
    "repository": "~/gemsub-subscriptions",
    "branch": "main",
    "remote_url": "git@github.com:your-user/your-subscriptions.git"
  },
  "state_file": "./gemsub_state.json",
  "headless": false
}
```

### Configuration Reference

| Option | Type | Required | Default | Description |
| :--- | :--- | :---: | :--- | :--- |
| `sources` | `[]string` | **Yes** | — | Upstream subscription URLs or local file paths containing share links |
| `fetch_interval` | `string` | **Yes** | — | Cycle interval duration (minimum `"1m"`, e.g. `"3h"`, `"45m"`) |
| `test.health_url` | `string` | No | `"https://www.gstatic.com/generate_204"` | Lightweight endpoint for network transport validation |
| `test.health_timeout` | `string` | No | `"4s"` (or `dial_timeout`) | Timeout for network transport connectivity check |
| `test.gemini.url` | `string` | No | `"https://gemini.google.com/"` | Target URL for Gemini application validation |
| `test.gemini.block_phrases` | `[]string` | **Yes** | — | String phrases indicating region/country blocks in response bodies |
| `test.timeout` | `string` | **Yes** | — | Maximum duration allocated for a complete candidate test |
| `test.dial_timeout` | `string` | No | `"4s"` | TCP connection and TLS handshake timeout |
| `test.concurrency` | `int` | No | `20` | Maximum number of concurrent candidate probes per cycle |
| `test.rate_limit_rps` | `int` | No | `0` (unlimited) | Global requests-per-second rate limit during probing |
| `test.max_retries` | `int` | No | `2` | Number of probe retries before marking an inconclusive candidate |
| `test.retry_backoff` | `string` | No | `"1s"` | Wait duration between probe retry attempts |
| `test.max_inconclusive_cycles` | `int` | No | `2` | Number of consecutive inconclusive cycles allowed before eviction |
| `serve.listen` | `string` | No | `"127.0.0.1:8765"` | Local address and port for the HTTP subscription server |
| `serve.path` | `string` | No | `"/sub"` | URL path for subscription delivery |
| `serve.format` | `string` | No | `"base64"` | Subscription output format: `"base64"` or `"raw"` |
| `publishing.enabled` | `bool` | No | `false` | Enable automated Git publication of passing configurations |
| `publishing.repository` | `string` | Conditional | — | Local filesystem path to Git repository (required if publishing enabled) |
| `publishing.branch` | `string` | No | `"main"` | Git target branch for publication commits |
| `publishing.remote_url` | `string` | Conditional | — | Remote Git repository URL used to verify origin (required if publishing enabled) |
| `state_file` | `string` | No | `"./gemsub_state.json"` | Filesystem path for persistent candidate state snapshot |
| `headless` | `bool` | No | `false` | Enable headless daemon execution by default |

---

## Candidate Testing & Validation Model

`gemsub` maintains an active, multi-stage probing pipeline to ensure only stable and functional proxies are published:

```text
Raw Sources → Link Ingestion → Normalization & Deduplication
                                         ↓
                     Stage 1: Transport Health Check
                       (TCP Connect, TLS / uTLS Handshake)
                                         ↓
                     Stage 2: Application Target Check
                       (HTTP Request to Target Endpoint)
                                         ↓
                     Content Inspection & Gate Evaluation
                                         ↓
                     Authoritative Store & State Update
```

1. **Ingestion & Normalization:** Ingests raw configurations, parses protocol parameters, and normalizes candidate representations to prevent duplicate tests.
2. **Transport Probing:** Evaluates the proxy's transport layer using `sing-box` with realistic uTLS client fingerprints. If TCP connection, TLS handshake, or health endpoint queries fail, the candidate is classified as a transport failure (`conn_refused`, `timeout`, `tls_error`, `reality_error`).
3. **Application Validation:** Proxies with sound transport connectivity proceed to application-layer validation against the target endpoint.
4. **Reliability Scoring & Bounded History:**
   - Every candidate retains a bounded circular history buffer of recent test outcomes.
   - Transient failures (e.g. temporary packet loss or rate limits) are flagged as `inconclusive`. Candidates with prior passing records remain servable across up to `test.max_inconclusive_cycles`.
   - Continuous passes build higher reliability scores, determining sorting order in subscriptions and presentation.

---

## Gemini-Specific Validation

Generic proxy checkers rely solely on TCP pings or Google 204 connectivity. These checks are insufficient for Google Gemini because:

1. **Regional Restrictions:** Proxies hosted in unsupported territories (or whose IP geolocation is incorrectly categorized) may connect cleanly to Google's CDN but can be served localized denial pages.
2. **Silent Rejections:** When blocked by region or policy, Gemini endpoints in some environments can return HTTP 200 or 403 pages containing specific rejection text:
   - *"isn't currently supported in your country"*
   - *"not available in your country"*
   - *"not available in your region"*
3. **TLS Fingerprint Rejection:** In some environments, Google frontends may detect or throttle non-browser TLS handshakes.

`gemsub` addresses this by simulating standard browser TLS client hellos via uTLS, establishing an authentic HTTP session to `https://gemini.google.com/`, and evaluating the response payload against configured `block_phrases`. Candidates encountering regional blocks are isolated with error category `region_blocked`.

---

## Generic vs Gemini Projections

The authoritative `Store` maintains two distinct, decoupled projections:

```text
                    ┌─────────────────────────┐
                    │       Store State       │
                    └────────────┬────────────┘
                                 │
                 ┌───────────────┴───────────────┐
                 ▼                               ▼
     Store.NetworkPassing()               Store.Passing()
   [Generic Network Projection]       [Gemini-Verified Projection]
                 │                               │
                 ▼                               ▼
       /sub/generic or                  /sub/gemini or
       /sub?projection=generic          /sub?projection=gemini
```

- **Generic Projection (`Store.NetworkPassing()`):** Contains all candidates that passed transport health probing (`https://www.gstatic.com/generate_204`), regardless of whether Gemini blocks them. Recommended for general web browsing, messaging, and non-Gemini workloads.
- **Gemini Projection (`Store.Passing()`):** Contains only candidates that passed both transport probing and Gemini application verification. Recommended for AI workflows and region-sensitive access.

---

## Publishing & Subserver Behavior

### Local HTTP Subserver

The subserver serves live subscription feeds directly to clients:

- **Base Endpoint:** `http://127.0.0.1:8765/sub` (serves the Gemini projection by default).
- **Dedicated Generic Endpoint:** `http://127.0.0.1:8765/sub/generic`
- **Dedicated Gemini Endpoint:** `http://127.0.0.1:8765/sub/gemini`
- **Health & Metrics Endpoint:** `http://127.0.0.1:8765/healthz`

#### Query Parameters

- `projection=generic` or `projection=gemini`: Select projection on `/sub`.
- `proto=vless`, `proto=vmess`, or `proto=trojan`: Filter feed by protocol (alias `protocol=`).
- `format=raw` or `format=base64`: Override feed encoding per-request.

Examples:
```bash
# Fetch raw generic subscription
curl -s "http://127.0.0.1:8765/sub/generic?format=raw"

# Fetch Gemini-passing VLESS proxies encoded in Base64
curl -s "http://127.0.0.1:8765/sub/gemini?proto=vless&format=base64"

# Check subserver status and metrics
curl -s "http://127.0.0.1:8765/healthz"
```

### Git Publishing Layer

When `publishing.enabled` is `true`, `gemsub` commits and pushes updated configurations to a Git repository at the conclusion of each test cycle:

- `all.txt`: All servable links (deduplicated and sorted by reliability).
- `vless.txt`: Servable `vless://` links.
- `vmess.txt`: Servable `vmess://` links.
- `trojan.txt`: Servable `trojan://` links.
- `meta.json`: Non-secret cycle metadata (timestamp, cycle number, candidate counts).

Publishing operations are atomic and safe: if no candidate status changes occurred during the cycle, Git publication is a no-op.

---

## Persistence & State Management

`gemsub` maintains persistent state across restarts using an atomic snapshot mechanism:

- State is saved to `state_file` (default `./gemsub_state.json`) and automatically compressed as `./gemsub_state.json.gz`.
- Writes occur via a temporary file followed by an atomic rename, preventing corruption during system crashes.
- On startup, `gemsub` restores persisted candidate state before normal probing resumes, enabling the HTTP subserver to serve valid restored configurations without waiting for a full new probe cycle to complete.

---

## Development

The project is implemented in Go and follows strict architectural boundaries:

```text
gemsub/
├── cmd/gemsub/               # Application entrypoint and lifecycle coordination
├── internal/
│   ├── config/              # JSON configuration parsing and validation
│   ├── source/              # Subscription fetching from remote URLs and local files
│   ├── parser/              # Proxy URI parsing (VLESS, VMess, Trojan, Shadowsocks)
│   ├── tester/              # Probe runners (transport health & Gemini validation)
│   ├── store/               # Authoritative candidate store, scoring, and projections
│   ├── subserver/           # HTTP subscription delivery server
│   ├── publisher/           # Git repository publishing integration
│   ├── tui/                 # Bubble Tea terminal user interface
│   ├── logging/             # Ring-buffer and structured logging infrastructure
│   ├── clipboard/           # Safe platform clipboard integration
│   └── version/             # Linker-injected version metadata
└── scripts/                 # Automated installers and hermetic integration tests
```

---

## Building from Source

### Prerequisites
- Go 1.25 or later
- Make
- Git

### Build Commands

```bash
# Clone the repository
git clone https://github.com/amirreza-a2a/gemsub.git
cd gemsub

# Compile binary with uTLS/Reality build tag
make build

# Run repository test suite
make test
```

The `-tags with_utls` tag is mandatory to enable uTLS handshake emulation in `sing-box`.

---

## Release Process

Maintainers follow a tag-driven release process automated through GitHub Actions and GoReleaser:

1. **Tagging:** A release is triggered by pushing a semantic version tag:
   ```bash
   git tag -a v0.1.0 -m "Release v0.1.0"
   git push origin v0.1.0
   ```
2. **Automated Pipeline (`.github/workflows/release.yml`):**
   - The `validate` job runs `go vet` and `go test` with least-privilege `contents: read` permissions.
   - The `release` job invokes GoReleaser v2 (`v2.18.1`) with `contents: write` permissions.
   - GoReleaser cross-compiles binaries, generates archives, builds `.deb` and `.rpm` packages, computes SHA-256 digests in `checksums.txt`, injects version metadata, and publishes assets to GitHub Releases.
3. **Local Release Verification:**
   Maintainers can verify release generation locally without creating Git tags:
   ```bash
   # Validate GoReleaser configuration syntax
   make release-check

   # Generate snapshot binaries and packages in ./dist
   make release-snapshot
   ```

---

## Checksums & Verification

All release assets are accompanied by an authoritative `checksums.txt` file containing SHA-256 digests.

### Automated Verifier Safety
Both `scripts/install.sh` and `scripts/install-termux.sh` enforce strict integrity checks:
- Exact filename matching (prevents substring confusion).
- Single-entry cardinality enforcement (rejects duplicate or missing entries).
- Strict 64-hex SHA-256 format verification.
- Pre-extraction fail-closed validation (archives are deleted if checksums do not match).
- Full support for binary-mode indicator formatting (`*<filename>`).

### Manual Verification
```bash
sha256sum --ignore-missing -c checksums.txt
```

---

## Platform Support

| Platform | Architecture | Binary Format | Distribution Channels |
| :--- | :--- | :--- | :--- |
| **Linux** | `x86_64` (`amd64`) | Statically linked ELF64 | Installer script, `.tar.gz`, `.deb`, `.rpm` |
| **Linux** | `arm64` (`aarch64`) | Statically linked ELF64 | Installer script, `.tar.gz`, `.deb`, `.rpm` |
| **Linux** | `armv7` (`armhf`) | Statically linked ELF32 | Installer script, `.tar.gz`, `.deb`, `.rpm` |
| **Android / Termux** | `arm64` (`aarch64`) | Position-Independent ELF64 (`DYN`) | Termux installer script, `.tar.gz` |

---

## Known Limitations

- **Termux Runtime Validation:** The Termux arm64 binary is validated via cross-compilation ELF inspection and mock installation tests. Physical runtime validation on Android devices is ongoing.
- **Termux Architectures:** 32-bit ARM, x86, and x86_64 Android devices are not supported.
- **Desktop Platforms:** Windows and macOS binaries are not currently generated or supported in the release matrix.
- **Upstream Dependency Notice:** A known data race exists in the upstream `sing-box` network interface monitor (`route/network.go`) under high concurrency with Go race detector active (`-race`). This is isolated in testing and tracked as technical debt (`TD-008`).

---

## License

This project is licensed under the MIT License.
