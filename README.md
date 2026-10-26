# Waker ⚡

A terminal UI and CLI for waking, checking, connecting to, and sleeping computers.

```sh
waker                       # interactive UI
waker connect desktop       # wake if needed, wait for readiness, connect
waker sleep desktop --wait
```

## Install

Requires Go 1.27+ to build. SSH actions require the system `ssh` client.

```sh
make build                  # bin/waker and bin/waker-agent
make install                # installs waker to ~/.local/bin
# Or:
go install github.com/tomer/waker/cmd/waker@latest
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

**The agent runs on the computer you want to sleep, not on the wake relay.** It handles `POST /sleep` and `GET /health`; it cannot wake a sleeping machine. The relay requires no Waker installation.

Use the agent when SSH suspend is unavailable or inconvenient, especially on Windows. For ordinary Linux/macOS SSH sleep, no agent is needed. The service account must have permission to suspend the target.

```sh
go install github.com/tomer/waker/cmd/waker-agent@latest

# On the TARGET, set a strong token through your shell/service configuration.
# Bind explicitly to the target's Tailscale IP:
waker-agent --bind 100.101.102.103 --port 9876
```

Set `WAKER_AGENT_TOKEN` on the target before starting the agent. It refuses to start without a token. It defaults to `127.0.0.1`, so remote use requires `--bind`. Bind to the target's Tailscale address to limit access to the tailnet; `0.0.0.0` exposes every IPv4 interface. Start the agent after Tailscale has assigned its address, and configure it to restart on failure if installed as a service.

On the CLIENT, export the **same token**, then configure the target:

```yaml
on_sleep:
  type: agent
  port: 9876
  token_env: WAKER_AGENT_TOKEN
```

The TUI's Agent sleep option includes a token environment-variable field. Existing token settings survive host edits. Static `token:` is supported, but an environment variable avoids storing it in YAML. Never commit tokens.

`GET /health` is an unauthenticated liveness check. `POST /sleep` requires an exact Bearer token. An accepted sleep response means suspension was requested; use `waker sleep desktop --wait` to confirm the target becomes unreachable. Suspend failures are logged on the target. HTTP traffic runs inside the encrypted Tailscale connection when using its address.

**Upgrade note:** agents previously started without a token or without an explicit bind address need their startup configuration updated.

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
