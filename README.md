# Waker ⚡

A terminal UI and CLI for waking, checking, connecting to, and sleeping computers.

```sh
waker                       # interactive UI
waker connect desktop       # wake if needed, wait for readiness, connect
waker sleep desktop --wait
```

## Install

Download a standalone `waker_OS_ARCH` binary from [Releases](https://github.com/tomer6285/waker/releases), make it executable on Linux/macOS, and put it on your `PATH`. Release builds include agent binaries too. Prebuilt downloads require a published version tag; for an unreleased checkout, build locally below. SSH actions require the system `ssh` client.

To build from source, use Go 1.27+:

```sh
make build                  # bin/waker and bin/waker-agent
make install                # installs both binaries to ~/.local/bin
```

Configuration lives at `~/.config/waker/hosts.yaml` (an existing `hosts.yml` is also supported). Override it with `--config PATH`. First launch creates an empty configuration. See [hosts.sample.yaml](hosts.sample.yaml).

## Remote wake over Tailscale

Use an **always-on SSH relay on the target's home LAN**. The relay sends the wake packet locally; after waking, the target is accessed directly through Tailscale.

1. Laptop → relay's Tailscale address → SSH → local Wake-on-LAN packet.
2. Target wakes and reconnects to Tailscale.
3. Laptop → target's Tailscale address → SSH, sleep agent, or other connect action.

The sleeping target's Tailscale address is unreachable until it wakes. A Tailscale subnet router alone does not forward WOL broadcasts. This setup needs no subnet routes, public port forwarding, or Waker daemon on the relay.

### Relay requirements

- A Pi, NAS, Linux server, or similar device that stays awake.
- Tailscale running in the same tailnet as your client.
- An SSH server with key authentication.
- Python 3 and a POSIX shell.
- Access to the target's physical LAN/broadcast domain. Ethernet is recommended.

On your client, first connect interactively to establish SSH host trust and verify the key and Python installation:

```sh
ssh pi@home-pi 'python3 --version'
ssh -o BatchMode=yes pi@home-pi 'python3 --version'
```

Here `home-pi` is the relay's MagicDNS name, Tailscale IP, or an SSH config alias. Waker honors `~/.ssh/config` and the SSH agent. Resolve any login, host-key, or Tailscale access-policy errors before using Waker.

### Target requirements

- Wake-on-LAN enabled in BIOS/UEFI and the OS/NIC settings.
- A supported sleep/power state and a powered NIC; wired Ethernet is usually required.
- Tailscale running automatically after boot/resume.
- SSH enabled for SSH connect/sleep, or the optional sleep agent described below.

Use the target's **physical Ethernet MAC**, not its Tailscale interface address. MAC auto-detection only works on the local LAN while the target is awake; enter the MAC explicitly for remote setup.

### Configuration

Replace the example MAC, names, user, and broadcast with your own values:

```yaml
version: 1
settings:
  auto_tailscale: true
defaults:
  broadcast: 192.168.1.255   # broadcast address of the HOME LAN
  wol_port: 9
  timeout: 120s
  poll_interval: 15s
  probe_timeout: 2s
  relay:
    host: home-pi            # always-on relay's Tailscale name/IP or SSH alias
    user: pi
    port: 22
hosts:
  - name: desktop
    mac: AA:BB:CC:DD:EE:FF   # target's physical Ethernet MAC
    ip: desktop             # target's Tailscale MagicDNS name or 100.x address
    subnet_check: false
    ssh: { user: tomer, port: 22 }
    checks: [{ type: tcp, port: 22 }]
    on_connect: { type: ssh }
    on_sleep: { type: ssh }
```

Allow SSH from the client to the relay and target in your tailnet policy and host firewalls. If using the sleep agent, allow its TCP port on the target too.

A host-level `relay:` overrides `defaults.relay`. A configured relay is used both at home and away; omit relay settings for local-only broadcast. In the TUI, add/edit a host to configure its relay host, user, and port. Leaving relay host blank inherits the default.

Advanced relay settings can be supplied in YAML:

```yaml
relay:
  host: home-pi
  user: pi
  args: ["-i", "/absolute/path/to/relay_key"]
  source_ip: 192.168.1.2     # optional relay LAN address for a multi-NIC relay
```

`interface:` selects a **client-side** interface for local WOL and cannot be combined with a relay. Use `relay.source_ip` instead. The broadcast address always belongs to the target LAN, not the Tailscale network.

`auto_tailscale: true` shows a connection screen while checking and starting the client's Tailscale connection, before host checks and actions become available. Failed connections show the error with retry and quit options. Network CLI commands also report when they are waiting for Tailscale. **Waker leaves Tailscale connected on exit** so SSH, Parsec, mounts, and games can continue working. Complete Tailscale installation/login beforehand; this setting does not enroll the relay or target. You can manually disconnect from the TUI settings or Tailscale itself.

### Verify the setup

First confirm WOL works locally. Then, with the client on another network and the relay still on at home:

```sh
waker status desktop
waker wake desktop --wait
waker connect desktop
waker sleep desktop --wait
```

Successful UDP delivery only means the relay sent a packet. `--wait` verifies target readiness. SSH connect waits for the configured SSH TCP port; ping alone is insufficient after resume.

If wake fails, check the relay's SSH/Python error. If it sends successfully but times out, check the target MAC, home broadcast address, VLAN isolation, NIC/BIOS WOL settings, and Tailscale startup on the target. If the target is already awake, `connect` skips the relay. Explicit `wake` always sends a packet.

## Optional waker-agent: sleep on the target

**Install the agent on the computer you want to sleep, not the wake relay.** It cannot wake a sleeping machine. Linux/macOS hosts with working SSH suspend can keep using `on_sleep: { type: ssh }` without an agent.

Agent setup handles credentials, autostart, and connection checks. You do not need Go on the target, token environment variables, or handwritten service files. Tailscale should already be installed and connected. Setup detects its IPv4 address and binds only to that address; `--bind IP` selects another specific local address. Wildcard binds are rejected by setup.

### Option A: set up the target from your laptop

Add the target to Waker first, including its physical Ethernet MAC and SSH user. Establish SSH host trust and make sure key authentication works:

```sh
waker add desktop --mac AA:BB:CC:DD:EE:FF --ip desktop --ssh-user tomer
ssh tomer@desktop
# Exit the SSH session, then:
waker setup desktop
```

Waker detects the target's OS and architecture, downloads a checksum-verified release binary, transfers it and a generated credential over SSH, and enables autostart. It verifies authenticated access from the client before saving the sleep method. It never sends a sleep request during setup.

- **Linux:** requires systemd. Installation runs as root; SSH opens a terminal so sudo can ask for the target user's password.
- **macOS:** installs a LaunchAgent as the SSH user, who must have an active desktop login. It runs at login and stops at logout. Do not install it with sudo.
- **Windows:** use local setup below; SSH installation currently requires a POSIX target.

SSH setup uses the host's SSH user, port, extra arguments, SSH config, and agent. It requires existing key authentication (`BatchMode=yes`) and honors normal host-key verification. The target must be awake and reachable. Its configured `ip` must also reach the agent after installation. The wake relay is not involved.

Repeat `waker setup desktop` to repair or upgrade an installation. The client reuses its saved credential. SSH setup from a new client provisions a new credential on the target; to keep an existing credential when adding another client, use a pairing export instead. Useful overrides:

```sh
waker setup desktop --release v1.0.0       # select a published version
waker setup desktop --bind 100.101.102.103 --port 9876
waker setup desktop --agent-binary ./waker-agent_linux_arm64
```

`--agent-binary` skips the download and must point to a binary built for the **target**, not the laptop. Downloads require a published release with the binary and `checksums.txt`; see Development for building before the first release.

### Option B: install directly on the target, then pair

Download the matching `waker-agent_OS_ARCH` binary from [Releases](https://github.com/tomer6285/waker/releases): `linux`, `darwin` (macOS), or `windows`; `amd64` or `arm64`. Windows binaries end in `.exe`. There is no Go installation or build step.

On Linux/macOS, make the downloaded file executable and run it, substituting its actual filename:

```sh
chmod +x ./waker-agent_linux_amd64
./waker-agent_linux_amd64 setup
```

Linux automatically requests sudo. On macOS, run setup from the logged-in user's Terminal **without sudo**. On Windows, run the downloaded binary in **PowerShell as Administrator**:

```powershell
.\waker-agent_windows_amd64.exe setup
```

Setup installs a managed copy, generates a strong credential, enables autostart, and verifies the local endpoint. It writes `waker-agent-pair.json` in your current directory. Use `--pair-file PATH` to choose another location, or `--no-pair` when repairing without exporting credentials.

Securely copy that pairing file to your laptop, then pair an existing Waker host:

```sh
waker setup desktop --pair-file ./waker-agent-pair.json
```

The client's existing host address is used for verification. The imported port and credential are saved automatically. **The pairing file contains a reusable credential**: transfer it over SSH or another trusted encrypted channel, and delete the transferred copies after pairing. It is not a one-time code. Pair other clients by securely transferring a pairing export to each one.

### Manage the installation

Use the installed binary's full path, or add its directory to `PATH`:

- Linux: `/usr/local/bin/waker-agent`
- macOS: `~/.local/bin/waker-agent`
- Windows: `C:\ProgramData\Waker\waker-agent.exe` (or your `ProgramData` directory)

For example, on Linux:

```sh
/usr/local/bin/waker-agent status
/usr/local/bin/waker-agent logs
/usr/local/bin/waker-agent update
/usr/local/bin/waker-agent uninstall
```

`update` downloads a checksum-verified release, preserves the configuration, restarts the service, and verifies authentication. `--release v1.0.0` selects a version. On Windows, `update` starts a helper that waits for the current command to exit before replacing the executable; inspect `%ProgramData%\Waker\update.log` for its result. You can also download a new binary outside the installation directory and run its `setup` command. Setup preserves the existing credential when no provisioning file is supplied.

`uninstall` stops the agent and removes autostart. It retains its binary, configuration, and logs so setup can restore the installation. On Linux, commands that need privileges automatically request sudo. On Windows, use an elevated PowerShell session for management.

### Permissions, credentials, and troubleshooting

Linux uses a root systemd service so suspend does not need an interactive password. macOS uses the logged-in user's LaunchAgent and may need an Automation permission approval on the first sleep request. Windows uses a SYSTEM startup task and a retry wrapper, so it works before login. Service restart/retry policies handle Tailscale not being ready at startup. The detected bind address is saved; rerun setup with `--bind IP` if the target's address changes.

Agent configuration is stored at `/etc/waker-agent/config.json`, `~/.local/share/waker-agent/config.json`, or `%ProgramData%\Waker\config.json`. Files containing credentials are owner-only on Unix and have restricted ACLs on Windows. Client setup stores its credential in a separate file in `credentials/` beside `hosts.yaml`; YAML contains only an absolute `on_sleep.token_file` reference. Keep those credential files with that client. Existing `token_env` and static `token` configurations remain supported.

If connection verification fails, run the target's `logs` command and check the bind address, Tailscale access policy, and firewall. Allow the configured agent TCP port (default 9876) from intended clients only. The Windows installer does not change firewall rules automatically. A failed remote setup leaves its credential on the client for retry; the previous host sleep configuration is saved only after successful verification.

`GET /health` is an unauthenticated liveness check. `GET /verify` checks Bearer authentication without suspending. `POST /sleep` requires that credential and requests suspension; an accepted response does not prove the OS suspended. Verify actual sleep when ready:

```sh
waker sleep desktop --wait
```

Agent HTTP traffic is encrypted when carried over Tailscale. For advanced manual operation, legacy service migration, and the underlying platform service files, see [Manual agent setup](docs/agent-manual.md). Stop any older manually installed service before using the new installer; it does not migrate legacy service paths or tokens automatically.

## CLI

```sh
waker list [--json] [--watch]
waker status desktop [--json] [--watch]
waker ping desktop [--json]

waker wake desktop [--wait]
waker wake desktop --relay home-pi --broadcast 192.168.1.255 --port 9
waker connect desktop --timeout 120s
waker connect desktop --no-wake       # fail if offline
waker connect desktop --no-wait       # if waking, send packet and exit
waker connect desktop --dry-run       # show action/settings without waking/connecting
waker connect desktop -- -X           # forward options to SSH

waker sleep desktop [--wait]
waker setup desktop                     # install agent over SSH
waker setup desktop --pair-file pair.json # pair a locally installed agent
waker add desktop --mac AA:BB:CC:DD:EE:FF --ip desktop \
  --ssh-user tomer --relay home-pi --relay-user pi --broadcast 192.168.1.255
waker --config ./hosts.yaml list
```

`--relay` on wake/connect overrides the configured relay; use an SSH alias for its user/key/port. `add` also supports `--relay-port` and `--ssh-port`. Configure connect/sleep types and advanced settings through YAML or the TUI.

## TUI

Run `waker`. The host list opens immediately with saved status while fresh checks run; the header identifies this initial refresh. Use arrows or `j/k` to select a host; `enter` wakes an offline host or connects to an online one, `c` wakes and connects, `w` sends WOL, `z` sleeps, `p` probes, and `r` refreshes. Use `a/e/d` to add/edit/delete, `/` to filter, `l` for logs, `s` for settings, `?` for help, and `q` to quit.

## Actions and presence

Connect actions: `ssh`, `parsec` (`peer_id`, optional `settings`), `mount`/`game` (`url`), and `custom` (`run`). Parsec requires its client to be installed and logged in. Mount/game URLs must themselves use an address reachable remotely; Waker does not rewrite them.

Sleep actions: `ssh` (default), `agent`, and `custom` (`run`). SSH sleep runs a POSIX detection script explicitly through `/bin/sh` on Linux/macOS, including when the remote login shell is fish. Windows' default OpenSSH shell should use the agent or a custom sleep command. SSH authentication and suspend-command failures are reported. If suspension drops SSH, Waker requires a remote command acknowledgement and an offline probe before treating the disconnect as success. An ambiguous disconnect is reported as an error; check the target status.

Checks: `ping`, `tcp` (`port`), `arp`. Any successful presence check marks a host online. Use TCP for remote hosts; ARP is local-only and cached entries can be stale. Sleep and power-off cannot be distinguished by these probes.

`subnet_check` defaults to **false**: OS routing and actual probes determine reachability, including Tailscale and routed networks. Explicit `true` enables the old local-interface guard and can reject valid VPN routes; remove it or set false for remote hosts.

## Development

```sh
make test
make build
go vet ./...
```

The automated relay tests execute the transmitted Python through an SSH test double and verify the actual 102-byte UDP packet. Real hardware WOL and off-site Tailscale connectivity require the setup check above.

### Agent releases

Pushing a version tag such as `v1.0.0` triggers `.github/workflows/release.yml`. It tests the project, cross-builds both binaries for Linux/macOS/Windows on amd64/arm64, and publishes the standalone binaries and `checksums.txt` to GitHub Releases. No release is published by running setup. Downloads use this repository's release assets and check their SHA-256 hashes.

Before a release is published, build a target binary locally and pass it to SSH setup:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/waker-agent_linux_arm64 ./cmd/waker-agent
bin/waker setup desktop --agent-binary bin/waker-agent_linux_arm64
```
