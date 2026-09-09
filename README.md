# Waker ⚡

> Wake-on-LAN manager with a TUI + CLI. Wake machines on demand and seamlessly chain into what you actually want to do — SSH, Parsec, mounts, game servers, or custom scripts.

[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)]()
[![License](https://img.shields.io/badge/license-MIT-green)]()

No more manual `wake → ping -c100 → ssh` loop. Define your hosts once, `waker` handles **wake + wait + connect + sleep**.

```sh
waker connect desktop
# → sends WOL magic packet → waits for SSH/ping/TCP → ssh user@desktop
```

---

## ✨ Features

- 🖥️ **Interactive TUI** (Bubble Tea) — browse hosts, live presence, wake/connect/sleep, add/edit/delete, logs, filter
- ⌨️ **Scriptable CLI** — `list`, `status`, `wake`, `connect`, `sleep`, `ping`, `add` with `--json` and `--watch` flags
- 📡 **Wake-on-LAN** — magic packets via broadcast, per-host interface, or SSH relay for remote networks
- 👀 **Presence polling** — ping + TCP (+ ARP) checks, latency, last-seen, online-first sorting
- 🔌 **Pluggable connect actions** — `ssh`, `parsec`, `mount` (smb://), `game` (steam://), `custom` scripts
- 😴 **Remote sleep** — via SSH (zero-install), `waker-agent` (HTTP + token), or custom webhooks
- 📄 **Simple YAML config** — `~/.config/waker/hosts.yaml`, auto-created on first run
- ➕ **MAC auto-detection** — `waker add` can resolve MAC from IP via ARP when `--mac` is omitted

---

## 📦 Installation

### Prerequisites

- Go 1.27+
- `ssh` client (for SSH connect/sleep and relay support)
- `parsecd` (only if you use Parsec actions — must already be logged in)
- WOL-capable wired NIC with Wake-on-LAN enabled in BIOS/UEFI (magic packets don't work over Wi-Fi on most hardware)

### Option 1: `go install` (recommended)

```sh
go install github.com/tomer/waker/cmd/waker@latest
go install github.com/tomer/waker/cmd/waker-agent@latest  # optional, for keyless remote sleep
```

### Option 2: Build from source

```sh
git clone https://github.com/tomer/waker.git
cd waker
make build        # produces bin/waker and bin/waker-agent
make install      # copies to ~/.local/bin (ensure it's on your PATH)
```

### Option 3: Manual `go build`

```sh
go build -o bin/waker ./cmd/waker
go build -o bin/waker-agent ./cmd/waker-agent  # optional
```

> See [`hosts.sample.yaml`](./hosts.sample.yaml) for a ready-to-copy example config.

---

## 🚀 Quickstart

1. **Launch the TUI** (creates `~/.config/waker/hosts.yaml` on first run):

   ```sh
   waker
   waker --config ./hosts.sample.yaml   # try with the sample config
   ```

2. **Add a host** (TUI: press `a`, or via CLI):

   ```sh
   waker add --name desktop --mac AA:BB:CC:DD:EE:FF --ip 192.168.1.10 \
     --connect ssh --user tomer

   # MAC auto-detected via ARP when omitted (host must be online):
   waker add --name nas --ip 192.168.1.20 --connect ssh --user tomer
   ```

3. **Wake → wait → connect**:

   ```sh
   waker wake desktop --wait
   waker connect desktop
   waker connect desktop --timeout 60s -- -X   # extra args passed to ssh
   ```

4. **Put it back to sleep**:

   ```sh
   waker sleep desktop
   ```

---

## 🖥️ TUI

Run with no arguments to start the interactive terminal UI:

```sh
waker
waker --config hosts.yaml
```

| Key | Action |
|---|---|
| `j` / `↓`, `k` / `↑` | Navigate hosts |
| `enter` | Wake host (or connect immediately if already online) |
| `c` / `shift+enter` | Connect pipeline: wake → wait until reachable → launch action |
| `w` | Send Wake-on-LAN magic packet only |
| `z` | Suspend / sleep machine (with confirmation) |
| `p` | Manual ping & TCP health-check probe |
| `a` / `e` / `d` | Add / edit / delete host |
| `o` | Toggle sort online-first |
| `r` | Refresh presence status immediately |
| `/` | Filter hosts by name or IP |
| `l` | Activity logs screen |
| `?` | Help screen |
| `q` | Quit |

> `shift+enter` needs Kitty keyboard / `modifyOtherKeys` support — `c` is the fallback where the terminal swallows it.

---

## ⌨️ CLI Reference

```sh
# List hosts and presence status
waker list
waker list --json
waker list --watch

# Single-host status & probe details
waker status desktop
waker status desktop --json
waker status desktop --watch

# Probe checks directly (exit 0 = online, 1 = offline, 2 = config error)
waker ping desktop

# Wake-on-LAN
waker wake desktop
waker wake desktop --wait
waker wake desktop --broadcast 192.168.1.255 --port 9
waker wake desktop --relay home-pi              # wake via SSH relay (remote nets)

# Wake, wait, and connect
waker connect desktop
waker connect desktop --timeout 60s
waker connect desktop --no-wake                 # fail if offline instead of waking
waker connect desktop -- -X                     # extra flags forwarded to ssh

# Suspend / sleep
waker sleep desktop
waker sleep desktop --method ssh|agent|custom
waker sleep desktop --confirm --timeout 10s

# Add or update a host
waker add --name desktop --mac AA:BB:CC:DD:EE:FF --ip 192.168.1.10 --connect ssh --user tomer
waker add --name gaming-pc --mac DE:AD:BE:EF:00:01 --ip 192.168.1.50 --connect parsec --peer-id demo123

# Use a custom config file / JSON output globally
waker --config ./hosts.yaml list
waker --json list
```

---

## ⚙️ Configuration

Default path: `~/.config/waker/hosts.yaml` (override with `--config`). Auto-created on first run with `0600` permissions.

Minimal example — full annotated example in [`hosts.sample.yaml`](./hosts.sample.yaml):

```yaml
version: 1

defaults:
  broadcast: 192.168.1.255
  wol_port: 9
  timeout: 120s
  poll_interval: 15s
  probe_timeout: 2s
  # relay: { host: home-pi, user: pi }  # SSH relay for remote WOL

hosts:
  - name: desktop
    mac: AA:BB:CC:DD:EE:FF
    ip: 192.168.1.10
    ssh: { user: tomer, port: 22 }
    checks: [{ type: ping }, { type: tcp, port: 22 }]
    on_connect: { type: ssh }
    on_sleep: { type: ssh }

  - name: gaming-pc
    mac: DE:AD:BE:EF:00:01
    ip: 192.168.1.50
    checks: [{ type: ping }, { type: tcp, port: 21725 }]
    on_connect: { type: parsec, peer_id: "demo-peer-id" }
    on_sleep: { type: agent, port: 9876, token_env: WAKER_AGENT_TOKEN }
```

MAC formats are flexible (`AA:BB:..`, `AA-BB-..`, `AABB..`, lowercase all normalize).

### 🔌 Connect actions (`on_connect`)

| Type | What it does |
|---|---|
| `ssh` | Execs system `ssh` — respects `~/.ssh/config`, agents, `ProxyJump`, `ControlMaster` |
| `parsec` | Launches `parsecd peer_id=ID[:settings]` (macOS / Linux / Windows paths resolved automatically) |
| `mount` | Opens `smb://…` URLs via OS handler (`open` / `xdg-open` / `explorer`) |
| `game` | Opens client URLs like `steam://connect/…` via OS handler |
| `custom` | Runs arbitrary `run:` script with `WAKER_HOST_NAME`, `WAKER_HOST_IP`, `WAKER_HOST_MAC` env vars |

### 😴 Sleep methods (`on_sleep`)

| Method | How it works |
|---|---|
| `ssh` (default) | Zero-install suspend over existing SSH keys (`systemctl suspend` / `osascript` sleep / `rundll32 …SetSuspendState`) |
| `agent` | `POST /sleep` to `waker-agent` on the target (Bearer token auth, default port `9876`) |
| `custom` | Any command — e.g. Home Assistant webhook or MQTT publish |

Flow: require `online` → confirm (TUI always, CLI only with `--confirm`) → run method → poll until `offline` (10s default).

### 👀 Presence (`checks`)

Each host's `checks` double as presence probes: `ping` (system ping, no root needed), `tcp` (dial `ip:port`), `arp` (OS ARP cache). Statuses: `online` ● / `offline` ○ / `waking` ◌ / `unknown` ?. Configure with `poll_interval` and `probe_timeout` under `defaults`.

> Sleep and power-off are indistinguishable on the wire — both show as `offline (last seen …)`.

---

## 🛰️ `waker-agent` (optional)

Tiny HTTP server for keyless sleep on targets where you don't want SSH keys:

```sh
# On the target machine (listens on :9876 by default):
WAKER_AGENT_TOKEN=super-secret waker-agent
# or: waker-agent --port 9876 --token super-secret

# In hosts.yaml:
on_sleep: { type: agent, port: 9876, token_env: WAKER_AGENT_TOKEN }
```

Binds LAN only, Bearer-token auth, token passed via `token_env` (never logged).

---

## 📋 Requirements & Notes

- **Same subnet for WOL** — magic packets are broadcast-based. For remote networks, use Tailscale/subnet-router or the `relay:` SSH option (sends the packet from an always-on home device).
- **Blocked ICMP?** — give the host an explicit TCP check (e.g. `checks: [{type: tcp, port: 22}]`).
- **Parsec** — client must already be logged in; get the peer ID from Parsec → Computers → right-click → Copy Peer ID.
- **SSH sleep/connect** — one-time `ssh-copy-id user@host` removes per-use auth prompts.

---

## 🛠️ Development

```sh
make build    # build both binaries into bin/
make test     # go test -v ./...
make run      # build + launch TUI
make clean    # remove bin/
```

Project layout:

```
cmd/waker/         CLI + TUI entrypoint (cobra)
cmd/waker-agent/   optional remote-sleep HTTP server
pkg/config/        YAML load/validate/save
pkg/wol/           magic packet construction, broadcast, SSH relay
pkg/health/        ping / TCP / ARP checkers
pkg/presence/      polling, status merging, last-seen
pkg/actions/       ssh / parsec / mount / game / custom
pkg/sleeper/       ssh / agent / custom sleep dispatcher
pkg/store/         wake history, last-seen cache
pkg/tui/           Bubble Tea models (list, detail, add/edit, logs, help)
hosts.sample.yaml  example config
```

---

## 🤝 Contributing

Issues and PRs welcome. Run `make test` before submitting; keep the TUI fast (no blocking I/O in the render path) and prefer `exec`ing system tools over reimplementing them.

---

## 📄 License

MIT — see [LICENSE](./LICENSE) (add one if missing).
