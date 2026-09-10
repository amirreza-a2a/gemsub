# gemsub

`gemsub` is a daemon that pulls VPN subscription share links from upstream sources, validates each link by testing connectivity and target accessibility through the proxy, and serves the servable candidates as a subscription for clients (e.g. Throne).

## Building

Build with uTLS/Reality support:

```bash
make build
```

Run tests:

```bash
make test
```

## Installation

### Linux

#### Recommended Installation (Automated)

Download and run the Linux installer, which automatically detects your architecture, downloads the release archive, verifies SHA-256 integrity, and installs `gemsub` into `~/.local/bin` without requiring root permissions:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash
```

Or install a specific version:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash -s -- -v v0.1.0
```

To install system-wide into `/usr/local/bin`:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install.sh | bash -s -- --system
```

#### Manual Installation

1. Download the archive for your architecture from [GitHub Releases](https://github.com/amirreza-a2a/gemsub/releases):
   - `x86_64` (amd64): `gemsub_Linux_x86_64.tar.gz`
   - `arm64` (aarch64): `gemsub_Linux_arm64.tar.gz`
   - `armv7` (armhf): `gemsub_Linux_armv7.tar.gz`
2. Download `checksums.txt` and verify integrity:
   ```bash
   sha256sum --ignore-missing -c checksums.txt
   ```
3. Extract the executable and move it to your preferred bin directory:
   ```bash
   tar -xzf gemsub_Linux_x86_64.tar.gz
   install -m 755 gemsub ~/.local/bin/gemsub
   ```

#### Linux Packages (.deb / .rpm)

Native system packages install the executable to the conventional package-managed path `/usr/bin/gemsub` and configuration template to `/etc/gemsub/config.example.json`.

Debian / Ubuntu:
```bash
sudo dpkg -i gemsub_<version>_linux_<arch>.deb
```

RHEL / Fedora:
```bash
sudo rpm -i gemsub_<version>_linux_<arch>.rpm
```

#### Available Architectures
- `x86_64` / `amd64` (64-bit Intel/AMD)
- `arm64` / `aarch64` (64-bit ARM)
- `armv7` (32-bit ARMv7)

#### Upgrade Path
Run the installer script again to fetch and verify the latest release. Existing configuration and state files are preserved.

---

### Android / Termux

> [!NOTE]
> This is a **Termux-compatible standalone artifact**, not yet an official package in the `termux-packages` upstream repository. Official Termux package repository integration is scheduled for a future milestone.

#### Supported Architecture
- `arm64` (`aarch64`) Android devices running Termux.

#### Installation Method

Run the dedicated Termux installation script from within your Termux environment:

```bash
curl -sSfL https://raw.githubusercontent.com/amirreza-a2a/gemsub/main/scripts/install-termux.sh | bash
```

The script:
- Verifies execution inside Termux userspace
- Detects the 64-bit ARM architecture
- Downloads the dedicated `gemsub_Termux_arm64.tar.gz` artifact
- Verifies SHA-256 checksum against `checksums.txt`
- Installs the binary directly to `$PREFIX/bin/gemsub`
- Does not require `root` or `sudo` permissions

#### Important Limitations
- Only 64-bit ARM (`aarch64`) Termux environments are currently supported.
- Statically linked pure Go Position-Independent Executable (PIE) targeting `/system/bin/linker64` and Android system DNS.
- Clipboard copy actions in TUI require standard Termux clipboard utilities or headless daemon operation (`gemsub -headless`).

---

## Release Pipeline & Maintainer Process

### Maintainer Tagging

Releases are strictly tag-driven. Maintainers trigger a release by pushing a semantic version tag:

```bash
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```

### GitHub Actions Automation

Pushing a `v*` tag triggers `.github/workflows/release.yml`, which:
1. Runs pre-release verification: `go test -tags with_utls -count=1 ./...` and `go vet -tags with_utls ./...`.
2. Invokes GoReleaser to cross-compile Linux (`amd64`, `arm64`, `armv7`) and Termux (`arm64`) binaries.
3. Generates `.tar.gz` archives, `.deb` packages, and `.rpm` packages.
4. Generates SHA-256 `checksums.txt` covering all published assets.
5. Injects linker version metadata:
   ```text
   gemsub vX.Y.Z
   commit: <sha>
   built: <timestamp>
   ```
6. Publishes assets to GitHub Releases.

### Local Snapshot Verification

Maintainers can verify the release pipeline locally without creating Git tags or publishing:

```bash
# Validate GoReleaser configuration syntax
make release-check

# Build snapshot binaries, packages, and checksums locally into dist/
make release-snapshot
```

Inspect generated binaries:
```bash
./dist/gemsub-linux_linux_amd64_v1/gemsub --version
```


## Git Publishing Layer

`gemsub` includes an optional local Git publishing layer that can automatically publish verified servable configurations to a dedicated Git repository after each successfully completed test cycle.

### What Publishing Does

After a probe cycle completes successfully, the publisher:
1. Reads the current canonically servable candidates from the store (including active passes and valid last-known-good candidates).
2. Generates deterministic subscription files:
   - `all.txt`: All servable links (sorted, deduplicated).
   - `vless.txt`: Servable `vless://` links.
   - `vmess.txt`: Servable `vmess://` links.
   - `trojan.txt`: Servable `trojan://` links.
   - `meta.json`: Non-secret cycle metadata (timestamp, cycle number, link counts).
3. Stages only the generated files in the target Git repository.
4. If changes are detected, commits them and pushes to the configured remote branch (`main`).
5. If no changes occurred between cycles, publishing is a safe no-op.

### Configuration

Publishing is disabled by default. You can enable it in `config.json`:

```json
{
  "publishing": {
    "enabled": true,
    "repository": "~/gemsub-subscriptions",
    "branch": "main",
    "remote_url": "git@github.com:your-user/your-subscriptions.git"
  }
}
```

Or enable it via CLI flag (which overrides config):

```bash
./gemsub -headless -config config.json -publish
```

### Git Authentication & Security

- Git operations occur locally using the system's Git toolchain.
- GitHub authentication (e.g. SSH keys) is expected to be configured on the host machine via standard SSH (`~/.ssh/id_*` / ssh-agent) or Git credentials.
- **Do NOT store private keys, tokens, or GitHub credentials** in `config.json`, `.env`, or anywhere in the repository.
- The publisher validates the remote origin URL before staging or pushing to prevent accidental publication to an unintended remote.
