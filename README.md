# gemsub

[English](README.md) | [فارسی](README.fa.md) | [Русский](README.ru.md)

[![CI](https://github.com/amirreza-a2a/gemsub/actions/workflows/ci.yml/badge.svg)](https://github.com/amirreza-a2a/gemsub/actions/workflows/ci.yml)

`gemsub` is a Go CLI/TUI daemon for testing proxy subscriptions with real target validation, Gemini compatibility checks, scheduling, persistence, and optional publishing.

It ingests raw proxy share links from upstream sources, verifies network transport connectivity and target service accessibility through active two-stage probing, and serves reliable, classified configurations over a local HTTP subscription endpoint or a remote Git repository.

> [!NOTE]
> **Release Status**: The repository is currently preparing for the upcoming **`v0.1.2`** release. The previous stable release is **`v0.1.1`**.

---

## Key Features

- **Two-Stage Active Probing:** Combines TCP/TLS/uTLS transport connectivity checks with real HTTP application-layer validation against target services (such as Google Gemini).
- **Dual Canonical Projections:** Derives both a strict Gemini-verified projection and a generic transport-passing projection from a single authoritative store.
- **Interactive Terminal UI:** Built with Bubble Tea and Lip Gloss; includes a real-time candidate table, credential masking (`[REDACTED]`), country flags (`auto`, `unicode`, `ascii`), candidate detail inspect modal, and one-key clipboard copying (`y`).
- **Interactive Configuration Center:** Built-in TUI manager (`c`) for live source management, runtime settings, scheduler control, and publishing diagnostics.
- **First-Run Onboarding Wizard:** Guided 5-step interactive setup that validates sources, sets up the local subserver, configures test parameters, and atomically saves configuration on fresh installations.
- **Headless Daemon Mode:** Lightweight background operation for Linux servers, systemd services, and resource-constrained environments without initializing the TUI.
- **Dynamic Configuration Propagation:** Thread-safe configuration service with atomic persistence and hot-reloading for runtime settings without restarting the daemon.
- **Local HTTP Subscription Server:** Serves auto-updating subscriptions to clients such as Throne, Sing-box, Clash, and v2rayN with dedicated routes (`/sub/generic`, `/sub/gemini`), protocol filtering (`?proto=vless`), format selection (`?format=raw` or `?format=base64`), and health/metrics endpoint (`/healthz`).
- **Automated Git Publishing:** Deterministically exports, stages, and pushes passing configurations (`generic/`, `gemini/`, `meta.json`) to a remote Git repository.
- **Persistent State & History:** Retains bounded circular cycle history, consecutive inconclusive tracking, and exponential reliability scoring across restarts with atomic gzip-compressed snapshots (`.json.gz`).
- **Supported Protocols:** VLESS, VMess, Trojan, and Shadowsocks (`ss://`).

---

## Architecture Overview

`gemsub` enforces strict architectural boundaries between ingestion, scheduling, probing, authoritative state management, presentation, and delivery:

```text
Upstream Sources (HTTP URLs / Local Files)
                 │
                 ▼
       Source Service & Normalizer
                 │
                 ▼
          Scheduler Loop ◄─── Control Service (Start / Pause / Resume / Run Now)
                 │
                 ▼
          Tester / Prober
         ┌────────────────────────────────────────┐
         │ Stage 1: Transport & Health (204 Check)│
         │ Stage 2: Target & Gemini (uTLS Probe)  │
         └────────────────────────────────────────┘
                 │
                 ▼
         Authoritative Store
         ┌────────────────────────────────────────┐
         │ - Bounded History & Reliability Score  │
         │ - Atomic Gzip Snapshots (.json.gz)     │
         │ - Dual Projections:                    │
         │   ├── NetworkPassing() → Generic       │
         │   └── Passing()        → Gemini        │
         └───────────────────┬────────────────────┘
                             │
            ┌────────────────┴────────────────┐
            ▼                                 ▼
     HTTP Subserver                     Git Publisher
     (/sub, /sub/generic,              (generic/, gemini/,
      /sub/gemini, /healthz)            meta.json)
```

The **Store** is the single authoritative source of candidate state, health, scoring, and projection membership. Presentation (TUI) and delivery (Subserver, Publisher) layers consume these canonical projections directly without duplicating classification or scoring logic.

---

## Quick Start

### 1. Interactive First Run (Onboarding Wizard)

When `gemsub` is launched interactively without an existing configuration file, it automatically starts the 5-step onboarding wizard:

```bash
gemsub
```

The wizard guides you through:
1. **Welcome & Architecture:** Overview of the active testing pipeline and projection model.
2. **Primary Subscription Source:** Ingest and validate your first upstream subscription URL.
3. **Local Subserver Setup:** Configure the listen address (default `127.0.0.1:8765`) and subscription path (default `/sub`).
4. **Testing & Gemini Defaults:** Configure probe concurrency, timeouts, and target URLs.
5. **Review & Save:** Review the generated configuration, atomically commit it to `config.json`, and immediately launch the testing runtime.

### 2. Headless First Run

In headless mode, an existing configuration file is required. If `config.json` is missing:

```bash
gemsub -headless
```

`gemsub` exits with code 1 and displays guidance:

```text
Configuration file not found: ./config.json

To configure gemsub:
  1. Run gemsub interactively without --headless to launch the onboarding wizard: gemsub
  2. Or create ./config.json manually by copying config.example.json:
     cp config.example.json ./config.json
```

### 3. Manual Configuration

You can also initialize configuration manually from the template:

```bash
cp config.example.json config.json
gemsub -config config.json
```

Or run directly as a background daemon:

```bash
gemsub -headless -config config.json
```

Once started, verified subscription feeds are immediately available:

```bash
# Fetch Gemini-passing proxies (Base64-encoded by default)
curl -s http://127.0.0.1:8765/sub

# Fetch generic network-passing proxies in plain text
curl -s "http://127.0.0.1:8765/sub/generic?format=raw"

# Query subserver status and metrics
curl -s http://127.0.0.1:8765/healthz
```

---

## Installation

### Linux

#### Automated Installer (Recommended)

The automated Linux installer detects host architecture, downloads the release archive, verifies SHA-256 digest integrity against `checksums.txt`, and stages the binary into `~/.local/bin`:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash
```

To install a specific release version:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash -s -- -v v0.1.0
```

To install system-wide into `/usr/local/bin` (requires `sudo`):

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash -s -- --system
```

#### Manual Archive Installation

1. Download the archive for your architecture from GitHub Releases:
   - `x86_64` (`amd64`): `gemsub_Linux_x86_64.tar.gz`
   - `arm64` (`aarch64`): `gemsub_Linux_arm64.tar.gz`
   - `armv7` (`armhf`): `gemsub_Linux_armv7.tar.gz`
2. Download `checksums.txt` and verify digest:
   ```bash
   sha256sum --ignore-missing -c checksums.txt
   ```
3. Extract and install:
   ```bash
   tar -xzf gemsub_Linux_x86_64.tar.gz
   install -m 755 gemsub ~/.local/bin/gemsub
   ```

#### Linux Packages (.deb / .rpm)

Debian / Ubuntu:
```bash
sudo dpkg -i gemsub_<version>_linux_<arch>.deb
```

RHEL / Fedora:
```bash
sudo rpm -i gemsub_<version>_linux_<arch>.rpm
```

---

### Android / Termux

> [!NOTE]
> This is a **Termux-compatible standalone artifact**, built for 64-bit ARM (`aarch64`) Android devices running Termux. Physical-device runtime validation is currently ongoing; verification has been conducted through hermetic builds and integration tests.

Run the Termux installer inside your Termux shell:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install-termux.sh | bash
```

The installer verifies the userspace environment, validates architecture, checks SHA-256 digests against `checksums.txt`, and installs the binary to `$PREFIX/bin/gemsub` without requiring root permissions.

---

### Windows

Official standalone zip archives are available for 64-bit Windows on both x86_64 and ARM64 architectures.

#### Manual Installation

1. Download the release archive for your architecture from GitHub Releases:
   - `x86_64` (64-bit Intel/AMD): `gemsub_Windows_x86_64.zip`
   - `arm64` (64-bit ARM): `gemsub_Windows_arm64.zip`
2. Download `checksums.txt` and verify the SHA-256 digest in PowerShell:
   ```powershell
   Get-FileHash .\gemsub_Windows_x86_64.zip -Algorithm SHA256
   ```
3. Extract the archive (via Windows Explorer or PowerShell):
   ```powershell
   Expand-Archive .\gemsub_Windows_x86_64.zip -DestinationPath .\gemsub
   cd .\gemsub
   ```
4. Run `gemsub.exe` in Windows Terminal or PowerShell:
   ```powershell
   # Interactive mode (launches onboarding wizard or TUI)
   .\gemsub.exe

   # Headless daemon mode with configuration file
   .\gemsub.exe -headless -config config.json
   ```

> [!NOTE]
> **Git Publishing Requirement:** If Git publishing is enabled (`publishing.enabled: true` or `-publish`), [Git for Windows](https://git-scm.com/download/win) must be installed and available in `%PATH%`.

---

### Building from Source

#### Prerequisites
- Go 1.25 or later
- Make
- Git

```bash
# Clone the repository
git clone https://github.com/amirreza-a2a/gemsub.git
cd gemsub

# Build executable with mandatory uTLS tags
make build
# or: go build -tags with_utls -ldflags "-s -w" -o gemsub ./cmd/gemsub

# Verify binary
./gemsub -version
```

---

## Usage & CLI Flags

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
  -v    print version information and exit (shorthand)
  -version
        print version information and exit
```

---

## Terminal User Interface (TUI)

The TUI provides real-time monitoring and interactive configuration management. Minimum recommended terminal size is **80 columns × 24 rows**.

### Candidate View (`ViewCandidates`)

Displays the table of evaluated candidates, latency, test outcome, protocol, and masked share link.

| Key | Context | Action |
| :--- | :--- | :--- |
| `Up` / `k` | Table | Move cursor up |
| `Down` / `j` | Table | Move cursor down |
| `PageUp` / `Ctrl+B` | Table | Page up |
| `PageDown` / `Ctrl+F` | Table | Page down |
| `Home` / `g` | Table | Jump to top |
| `End` / `G` | Table | Jump to bottom |
| `Enter` / `Space` | Table | Open candidate detail inspection modal |
| `s` | Table | Toggle filter between all candidates and servable-only |
| `y` | Table | Copy unredacted share link of selected candidate to clipboard |
| `c` | Global | Enter Config Center |
| `Tab` | Global | Switch focus between Candidate View and Log View |
| `q` / `Ctrl+C` | Global | Gracefully shut down daemon, save state, and exit |

*Credential Redaction:* Sensitive credentials (passwords, UUIDs) are automatically redacted in presentation (`[REDACTED]`). Copying with `y` copies the authentic, unredacted link to your clipboard.

### Log View (`ViewLogs`)

Displays real-time structured log records buffered in a circular memory handler.

| Key | Context | Action |
| :--- | :--- | :--- |
| `Up` / `k` | Log pane | Scroll up (disables auto-follow) |
| `Down` / `j` | Log pane | Scroll down |
| `PageUp` / `Ctrl+B` | Log pane | Page up |
| `PageDown` / `Ctrl+F` | Log pane | Page down |
| `Home` / `g` | Log pane | Jump to oldest log |
| `End` / `G` | Log pane | Jump to latest log (re-enables auto-follow) |
| `f` | Log pane | Toggle log follow mode |
| `l` (or `1`-`4`) | Log pane | Cycle log level: `DEBUG` (1) → `INFO` (2) → `WARN` (3) → `ERROR` (4) |
| `c` | Global | Enter Config Center |
| `Tab` | Global | Switch back to Candidate View |
| `q` / `Ctrl+C` | Global | Gracefully shut down daemon, save state, and exit |

### Config Center (`ViewConfig`)

Press `c` from Candidate or Log view to open the interactive configuration center. Press `Esc` to return.

- **Category Navigation:** `Tab` / `l` / `Right` (next category), `Shift+Tab` / `h` / `Left` (previous category).
- **Categories:**
  1. **General:** Subserver address, path, default format, country flag mode, state file path.
  2. **Sources:** Live subscription source manager:
     - `j` / `k` / `g` / `G`: Select source.
     - `Space` or `e`: Toggle source enabled / disabled.
     - `a`: Add new source URL and display name in modal dialog.
     - `Enter`: Edit selected source URL or name.
     - `d` or `x`: Delete selected source (prompts `y` to confirm).
  3. **Testing:** Concurrency, timeout, dial timeout, rate limit RPS, retries, retry backoff, max inconclusive cycles. Press `Enter` or `Space` or `e` to edit in modal.
  4. **Gemini:** Target check URL and block detection phrases list. Press `Enter` or `Space` or `e` to edit.
  5. **Scheduler:** Telemetry (state, next cycle estimate, completed cycles, last duration):
     - `p` / `P`: Pause or resume scheduler timer.
     - `r` / `R`: Trigger immediate test cycle ("Run Now").
     - `Enter` / `Space` / `e`: Edit cycle interval or candidate probe limit.
  6. **Publishing:** Git publication status, diagnostics, and options:
     - `e` / `E`: Toggle publishing enabled / disabled.
     - `p` / `P`: Publish subscriptions to remote Git repository now (prompts `y`/`n` confirmation).
     - `t` / `T`: Run publishing diagnostic test (verifies local repository, remote connectivity, and permissions).
     - `r` / `R` or `Enter`: Edit repository path, branch, or remote URL.

---

## Configuration

Configuration is stored in JSON format (default `./config.json`). A template is provided in `config.example.json`.

```json
{
  "sources": [
    {
      "url": "https://raw.githubusercontent.com/example/vpn/main/sub.txt",
      "name": "Primary Source",
      "enabled": true
    }
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
  "headless": false,
  "probe_limit": 0,
  "flag_mode": "auto"
}
```

### Configuration Reference

| Option | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `sources` | `[]SourceItem` / `[]string` | `[]` | Upstream subscription URLs or local file paths. Accepts structured objects (`id`, `url`, `name`, `enabled`) or legacy strings. |
| `fetch_interval` | `string` | `"3h"` | Duration interval between scheduled test cycles (minimum `"1m"`, e.g. `"3h"`, `"45m"`). |
| `test.health_url` | `string` | `"https://www.gstatic.com/generate_204"` | Endpoint used for Stage 1 transport connectivity validation. |
| `test.health_timeout` | `string` | `"4s"` | Timeout for Stage 1 transport health check. |
| `test.gemini.url` | `string` | `"https://gemini.google.com/"` | Endpoint for Stage 2 Gemini application verification. *(Legacy alias: `test.target_url`)* |
| `test.gemini.block_phrases` | `[]string` | *(built-in defaults)* | Substrings in response bodies indicating regional restriction or blocking. *(Legacy alias: `test.block_phrases`)* |
| `test.timeout` | `string` | `"10s"` | Total maximum probe duration per candidate. |
| `test.dial_timeout` | `string` | `"4s"` | TCP connection and TLS handshake timeout. |
| `test.concurrency` | `int` | `20` | Maximum number of concurrent candidate probes per cycle. |
| `test.rate_limit_rps` | `int` | `0` (unlimited) | Global requests-per-second limit during probing. |
| `test.max_retries` | `int` | `2` | Number of probe retries before classifying candidate as inconclusive (`0` = single attempt). |
| `test.retry_backoff` | `string` | `"1s"` | Wait time between probe retry attempts. |
| `test.max_inconclusive_cycles` | `int` | `2` | Consecutive inconclusive cycles tolerated before candidate eviction from servable set. |
| `serve.listen` | `string` | `"127.0.0.1:8765"` | Local network address and port for HTTP subserver. |
| `serve.path` | `string` | `"/sub"` | Base URL path for subscription delivery. |
| `serve.format` | `string` | `"base64"` | Default subscription feed encoding: `"base64"` or `"raw"`. |
| `publishing.enabled` | `bool` | `false` | Enable automated Git publication of passing configurations. |
| `publishing.repository` | `string` | `""` | Local filesystem path to the target Git repository (required if publishing enabled). |
| `publishing.branch` | `string` | `"main"` | Git branch for publication commits. |
| `publishing.remote_url` | `string` | `""` | Remote Git repository URL used for origin safety verification (required if publishing enabled). |
| `state_file` | `string` | `"./gemsub_state.json"` | Path for candidate state snapshot (automatically saved as compressed `.json.gz`). |
| `headless` | `bool` | `false` | Default execution mode (`true` = run without TUI). |
| `probe_limit` | `int` | `0` | Maximum candidates to test per cycle (`0` = test all candidates). |
| `flag_mode` | `string` | `"auto"` | Country flag presentation mode: `"auto"`, `"unicode"`, or `"ascii"`. |

### Reload Policy: Hot-Reload vs. Restart Required

`gemsub` differentiates between settings that take effect immediately at runtime and settings that require restarting the process:

| Policy | Configuration Fields | Runtime Behavior |
| :--- | :--- | :--- |
| **Hot-Reloadable** | `sources`<br>`fetch_interval`<br>`probe_limit`<br>`flag_mode`<br>`serve.format`<br>`test.health_url`<br>`test.health_timeout`<br>`test.gemini.url`<br>`test.gemini.block_phrases`<br>`test.timeout`<br>`test.dial_timeout`<br>`test.concurrency`<br>`test.rate_limit_rps`<br>`test.max_retries`<br>`test.retry_backoff`<br>`test.max_inconclusive_cycles`<br>`publishing.enabled`<br>`publishing.repository`<br>`publishing.branch`<br>`publishing.remote_url` | Applied immediately to running subsystems (Scheduler, Tester, Subserver, Publisher) upon save without restarting the process. |
| **Restart Required** | `serve.listen`<br>`serve.path`<br>`state_file`<br>`headless` | Saved to `config.json` immediately; flagged in the Config Center as pending restart because network sockets, snapshot paths, or process modes cannot be rebound safely while running. |

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
   - Every candidate retains a bounded circular history buffer (up to 5 recent test outcomes).
   - Transient failures (e.g. temporary packet loss or rate limits) are flagged as `inconclusive`. Candidates with prior passing records remain servable across up to `test.max_inconclusive_cycles`.
   - Continuous passes build higher reliability scores using exponential scoring decay (`decay_lambda`), determining sorting order in subscriptions and presentation.

---

## Gemini-Specific Validation

Generic proxy checkers rely solely on TCP pings or Google 204 connectivity. These checks are insufficient for Google Gemini because:

1. **Regional Restrictions:** Proxies hosted in unsupported territories (or whose IP geolocation is incorrectly categorized) may connect cleanly to Google's CDN but are served localized denial pages.
2. **Silent Rejections:** When blocked by region or policy, Gemini endpoints can return HTTP 200 or 403 pages containing specific rejection text:
   - *"isn't currently supported in your country"*
   - *"not available in your country"*
   - *"not available in your region"*
3. **TLS Fingerprint Rejection:** Google frontends may detect or throttle non-browser TLS handshakes.

`gemsub` addresses this by simulating standard browser TLS client hellos via uTLS, establishing an authentic HTTP session to `https://gemini.google.com/`, and evaluating the response payload against configured `block_phrases`. Candidates encountering regional blocks are classified as `region_blocked`.

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

## Delivery Layers

### HTTP Subserver

The subserver delivers live subscriptions to proxy clients:

- **Base Endpoint:** `http://127.0.0.1:8765/sub` (serves the Gemini projection by default).
- **Dedicated Generic Endpoint:** `http://127.0.0.1:8765/sub/generic` (serves the generic network-passing projection).
- **Dedicated Gemini Endpoint:** `http://127.0.0.1:8765/sub/gemini` (serves the Gemini-verified projection).
- **Health & Metrics Endpoint:** `http://127.0.0.1:8765/healthz` (returns JSON status, servable count, generic servable count, total stored, and last cycle time).

#### Query Parameters
- `projection=generic` or `projection=gemini`: Select projection on base endpoint `/sub`.
- `proto=vless`, `proto=vmess`, or `proto=trojan`: Filter feed by protocol (alias `protocol=`).
- `format=raw` or `format=base64`: Override feed encoding per-request.

#### Examples
```bash
# Gemini-passing subscription in Base64 (default):
curl -s "http://127.0.0.1:8765/sub"

# Generic network-healthy proxies in plain text:
curl -s "http://127.0.0.1:8765/sub/generic?format=raw"

# Gemini-verified VLESS proxies only in plain text:
curl -s "http://127.0.0.1:8765/sub/gemini?proto=vless&format=raw"

# Health status and telemetry:
curl -s "http://127.0.0.1:8765/healthz"
```

### Git Publisher

When `publishing.enabled` is `true`, `gemsub` commits and pushes updated configurations to the specified Git repository upon cycle completion:

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

- `generic/*.txt`: Proxies passing Stage 1 transport health checks.
- `gemini/*.txt`: Proxies passing both Stage 1 and Stage 2 Gemini checks.
- `meta.json`: Non-secret cycle metadata (timestamp, cycle number, candidate counts per projection).
- Publication operations are safe and atomic: changes are validated prior to Git staging, and clean working trees are enforced.

---

## State Persistence

- Persisted state is saved to `state_file` (default `./gemsub_state.json`) and automatically compressed with gzip as `./gemsub_state.json.gz`.
- Writes occur via a temporary file followed by an atomic rename, preventing file corruption across power loss or crashes.
- On startup, `gemsub` restores candidate records, reliability scores, and historical test samples before resuming scheduler probing. Restored passing candidates are served immediately on startup without waiting for a new cycle to finish.

---

## Development & Verification

### Build & Verification Commands

All development and CI operations require the `-tags with_utls` build tag to enable uTLS handshake emulation:

```bash
# Format check
gofmt -l .

# Whitespace check
git diff --check

# Static analysis
go vet -tags with_utls ./...

# Unit tests (serialized to ensure deterministic timing checks)
go test -tags with_utls -count=1 -p 1 ./...

# Race detector gate (on project packages)
go test -tags with_utls -race -count=1 ./cmd/gemsub ./internal/config ./internal/scheduler ./internal/publisher ./internal/source ./internal/store ./internal/subserver ./internal/tui

# Build executable
make build
# or: go build -tags with_utls ./cmd/gemsub
```

> [!NOTE]
> `internal/tester` is tested without `-race` due to an upstream sing-box interface monitor data race tracked as `TD-008`.

---

## Release Process

Maintainers follow a tag-driven release process automated through GitHub Actions and GoReleaser:

1. **Tagging:** A release is triggered by pushing a semantic version tag:
   ```bash
   git tag -a vX.Y.Z -m "Release vX.Y.Z"
   git push origin vX.Y.Z
   ```
2. **Automated Pipeline (`.github/workflows/release.yml`):**
   - The `validate` job runs `go vet` and serialized unit tests with least-privilege `contents: read` permissions.
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

## Platform Support & Known Limitations

| Platform | Architecture | Binary Format | Distribution Channels |
| :--- | :--- | :--- | :--- |
| **Linux** | `x86_64` (`amd64`) | Statically linked ELF64 | Installer script, `.tar.gz`, `.deb`, `.rpm` |
| **Linux** | `arm64` (`aarch64`) | Statically linked ELF64 | Installer script, `.tar.gz`, `.deb`, `.rpm` |
| **Linux** | `armv7` (`armhf`) | Statically linked ELF32 | Installer script, `.tar.gz`, `.deb`, `.rpm` |
| **Android / Termux** | `arm64` (`aarch64`) | Position-Independent ELF64 (`DYN`) | Termux installer script, `.tar.gz` |
| **Windows** | `x86_64` (`amd64`) | Standalone PE64 (`.exe`) | Standalone `.zip` |
| **Windows** | `arm64` | Standalone PE64 (`.exe`) | Standalone `.zip` |

### Known Limitations
- **Android / Termux Runtime Validation:** The Termux arm64 binary is validated via cross-compilation ELF inspection and hermetic tests. Physical device runtime testing is ongoing.
- **Desktop Platforms:** macOS desktop binaries are not currently generated or supported in the release matrix. Official Windows binaries are supported for x86_64 and arm64.
- **Upstream Sing-Box Race (`TD-008`):** An upstream data race exists in `sing-box`'s network interface monitor (`route/network.go`) under high concurrency with Go race detector enabled. This is isolated in CI and tracked in the repository technical debt register.

---

## License

This project is licensed under the MIT License.
