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
