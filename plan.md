# Wake-on-LAN Manager — Plan

## 1. Vision
TUI + CLI tool that wakes machines on demand and chains into what you actually want to do.

Hero flow:
```
connect desktop
→ wakes desktop (WOL magic packet) → waits for SSH/ping/TCP → ssh user@desktop
```

No manual `wake → ping -c100 → ssh` loop. Define hosts once, `waker` handles wake + wait + connect.

## 2. Goals / Non-Goals
Goals:
- Single binary, fast startup, works on macOS/Linux (+ Windows best-effort).
- TUI for browsing/waking hosts + CLI for scripting (`waker connect desktop`).
- Reliable wake → wait → connect + `sleep` pipeline with good feedback.
- Integrations via pluggable "connect actions": SSH, Parsec, mounts, game servers, custom commands, hooks.

Non-goals v1:
- No unknown-device subnet discovery scan / daemon / mobile app. Presence polling of *known* hosts is in scope (see §4.3).
- No remote WAN relay (VPN/Tailscale assumed for remote; direct subnet WOL only v1).
- No secrets vault; reuse existing ssh/config, OS keychain only if trivial.

## 3. User Stories
1. As user I `waker` → see host list with status (unknown/offline/waking/online), press `w` to wake, `c`/`enter` to connect.
2. As user I `waker connect desktop` → if offline: send WOL, show spinner + ping/SSH checks, auto-ssh when ready; if online: ssh immediately.
3. As user I define per-host post-wake actions: `ssh`, `parsec` (peer_id), `mount smb://nas/share`, `open steam://...`, `run: ./scripts/sync.sh`.
4. As gamer I `waker connect valheim` → wakes server, waits for TCP `2456`, then launches game client / shows `ready`.
5. As homelab user I edit `~/.config/waker/hosts.yaml` once and everything works.
6. As user I `waker sleep desktop` (or `z` in TUI) → machine suspends without extra prompts; presence flips to offline.

## 4. UX Design

### 4.1 TUI (default `waker`, no args)
Bubble Tea-style Elm layout:
```
┌ Waker ── q quit / ? help ─────────────┐
│ > ● desktop  192.168.1.10  online     │
│   ○ nas      192.168.1.20  offline [w]│
│   ◌ valheim  ...           waking… 12s│
├───────────────────────────────────────┤
│ detail: desktop • AA:BB:CC:.. • last  │
│ seen 2m ago • ssh tomer@desktop       │
│ log: WOL sent via 192.168.1.255:9 ✓   │
│      ping ok… ssh:22 open → connecting │
└───────────────────────────────────────┘
```
Screens: List / Detail / Add-Host form / Logs / Help / Settings.
Add/Edit form fields: `name, mac, ip, broadcast` + `connect type` selector: `ssh | parsec | mount | game | custom`, with conditional fields — `ssh: user/port/args`, `parsec: peer_id (+ optional client settings e.g. `client_vsync=1`)`, `mount: url`, `custom: run`. Parsec must be a first-class option, not hidden under custom.
Keys: `j/k` nav, `enter` wake, `shift+enter` wake+connect (wake→wait→action), `w` alias wake, `c` alias connect, `z` sleep, `p` ping, `e` edit, `a` add, `d` delete, `r` refresh, `o` sort online-first, `q` quit, `?` help, `/` filter. Note: `shift+enter` requires Kitty keyboard / modifyOtherKeys support — `c` is the fallback where the terminal swallows it.

### 4.2 CLI (scriptable, same core)
```
waker list [--json]
waker status desktop
waker wake desktop [--broadcast 192.168.1.255 --port 9] [--wait]
waker connect desktop [--timeout 120s] [--no-wake] [-- ssh -X ...]
waker sleep desktop [--method ssh|agent|custom] [--timeout 10s]
waker add --name desktop --mac AA:BB:CC:DD:EE:FF --ip 192.168.1.10
waker ping desktop
```
Exit codes: 0 connected/launched, 1 timeout/offline, 2 config error. `--json` for scripting.

### 4.3 Presence — showing what's already awake
Yes. No extra daemon needed; TUI + CLI poll *known* hosts from `hosts.yaml`.

How it works:
- Per-host `checks` (already in schema) double as presence probes: `ping` → ICMP echo, `tcp` → dial `ip:port` with 1-2s timeout, `arp` → OS ARP cache hit (`ip neigh` / `arp -a`).
- `presence/` poller runs on startup + every `poll_interval` (default 15s, configurable) + on demand (`r` refresh, `waker status <name>`, `waker list --watch`).
- Probes run concurrently (worker pool, ~10 parallel) so 20 hosts resolve in ~2-3s. Fast path: TCP dial first (no root needed); fall back to system `ping -c1 -W1` (avoids raw-socket privs on macOS/Linux).
- Status mapping: `online` (any check passes) → ● green; `offline` (all fail) → ○ dim; `waking` (WOL sent < timeout, still failing) → ◌ yellow spinner; `unknown` (never polled / DNS error) → ? grey.
- TUI shows: status dot + latency (`online 12ms`) + `last seen` timestamp from store. Header shows `3/5 online`. Sort online-first toggle (`o`).
- CLI: `waker list` prints status column, `waker list --json` emits `{name, status, latency_ms, last_seen}`, `waker status desktop --watch` live-updates.

Config addition:
```yaml
defaults:
  poll_interval: 15s
  probe_timeout: 2s
```

Limits (document in help): can't distinguish S3-sleep vs powered-off (both silent) — show `offline (last seen 2h ago)`; hosts blocking ICMP need explicit `checks: [{type: tcp, port: 22}]`; devices on another subnet/VPN only checkable via TCP, not ARP.

### 4.4 Sleep — suspend devices without per-use auth
Strategy: SSH by default (zero install), opt-in `waker-agent` for keyless users. Same `waker sleep X` / `z` key tries agent → falls back to SSH.

- Default `ssh` method: reuse existing `ssh:` config + keys/agent + `ControlMaster`. No prompt after one-time `ssh-copy-id`. Per-OS command:
  - Linux: `systemctl suspend`
  - macOS: `osascript -e 'tell application "System Events" to sleep'`
  - Windows (via OpenSSH server): `rundll32 powrprof.dll,SetSuspendState 0,1,0` (fallback `shutdown /h`)
- Opt-in `agent` method: tiny `waker-agent` HTTP server on target (`:9876` default), `POST /sleep` with `Authorization: Bearer <token>`. Single binary, autostart via systemd / LaunchAgent / Scheduled Task. No SSH keys needed; token stored once in `hosts.yaml` (`0600`).
- `custom` method escape hatch: `on_sleep: { type: custom, run: "..." }` (e.g. Home Assistant webhook, MQTT, `net rpc`).
- Flow: `sleep` → require `online` (else no-op "already offline") → confirm in TUI (`z` → `y/n`, skipped in CLI unless `--confirm`) → run method → poll presence until `offline` or 10s timeout → toast/log `desktop → offline`.
- Add/Edit form: `sleep type` selector `ssh (default) | agent | custom` + conditional `agent: port/token`, same first-class treatment as Parsec.

## 5. Tech Choice
Recommend: **Go + Bubble Tea + Lip Gloss + Cobra/Viper**.
Why: single static binary, excellent WOL/UDP stdlib, SSH exec via `exec ssh` (reuse user config/keys/agent, no reimpl), mature TUI ecosystem.
Alternative: Rust + ratatui — better if you want memory safety showcase, but slower to build v1.
Persistence: YAML config + SQLite/bbolt only for history/cache (last-seen, latency). Start YAML-only.

## 6. Architecture
```
cmd/waker (cobra) ─┬─ tui/ (bubbletea models: list, detail, add, logs)
                   └─ core/ config | wol | health | actions | ssh | sleeper
                   └─ agent/ (optional `waker-agent` HTTP server: POST /sleep, token auth)
config/: load ~/.config/waker/hosts.yaml, validate MAC/IP, watch reload
wol/: build magic packet (FF*6 + MAC*16), send UDP to broadcast:port, support directed + subnet-directed
health/: parallel checkers: ICMP ping, TCP dial (22,445,游戏 port), SSH TCP banner; backoff poll until timeout
presence/: reuses health checkers on ticker (`poll_interval`), merges with ARP cache + store `last_seen`, emits status events to TUI/CLI
actions/: ssh (exec system ssh), parsec (`parsecd peer_id=ID[:settings]` — macOS `/Applications/Parsec.app/Contents/MacOS/parsecd`, Linux `/usr/bin/parsecd`, Windows `"C:\Program Files\Parsec\parsecd.exe"`), mount (open/mount cmd), custom (shell), game (open URL); stream output to TUI log pane
sleeper/: `sleep` dispatcher (agent → ssh → custom), per-OS ssh sleep commands, presence-wait until offline
store/: last-seen, wake history
```

Config schema (`hosts.yaml`):
```yaml
version: 1
defaults:
  broadcast: 192.168.1.255
  wol_port: 9
  timeout: 120s
  ssh_options: ["-o", "ConnectTimeout=5"]
hosts:
  - name: desktop
    mac: AA:BB:CC:DD:EE:FF
    ip: 192.168.1.10
    broadcast: 192.168.1.255
    ssh: { user: tomer, port: 22, args: ["-X"] }
    checks: [{ type: ping }, { type: tcp, port: 22 }]
    on_connect: { type: ssh }
    on_sleep: { type: ssh }
    # or: on_sleep: { type: agent, port: 9876, token_env: WAKER_AGENT_TOKEN }
    # or: on_sleep: { type: custom, run: "curl -X POST http://nas:8123/api/..." }
  - name: nas
    mac: "11:22:33:44:55:66"
    ip: 192.168.1.20
    checks: [{ type: tcp, port: 445 }]
    on_connect: { type: custom, run: "open smb://192.168.1.20/share" }
  - name: valheim
    mac: ...
    checks: [{ type: tcp, port: 2456 }]
    on_connect: { type: custom, run: "open steam://connect/192.168.1.30:2456" }
  - name: gaming-pc # Windows host via Parsec
    mac: DE:AD:BE:EF:00:01
    ip: 192.168.1.50
    checks: [{ type: ping }, { type: tcp, port: 21725 }]
    on_connect: { type: parsec, peer_id: "exampleid", settings: "client_vsync=1" }
```

Wake→Wait→Connect state machine: `Offline → WolSent → Waking (poll checks, spinner, log) → Online → Action (exec, replace process) → Done/Timeout`.

## 7. Milestones
**M0 Scaffold (0.5d):** `go mod`, cobra skeleton, `list/wake/connect --help`, sample hosts.yaml, README.
**M1 Core headless (1-2d):** config load/validate, WOL send, `wake/wait` with ping+TCP, `presence` poller + `list/status` with status/latency/last-seen, timeouts, `--json`.
**M2 TUI v1 (2-3d):** list+detail+logs, keybindings above, live presence refresh (ticker + `r`/`o`), wake/connect/sleep (`enter`/`shift+enter`/`z`) from TUI.
**M3 Actions (1-2d):** ssh/parsec/mount/custom/game actions, per-host `on_connect`, pre/post hooks. Parsec: resolve binary per-OS, exec `parsecd peer_id=ID[:settings]`, require prior Parsec login, surface peer-ID hint. Sleep: `sleeper/` with ssh default + `waker-agent` (HTTP token) + custom; `waker sleep`, confirm prompt, wait-until-offline.
**M4 Polish (1d):** add/edit form with `connect type` selector incl. parsec (peer_id field + validation), fuzzy filter, last-seen cache, error toasts, `?` help, man page.
**M5 Release:** GoReleaser, brew tap, CI (lint/test), integration test with fake UDP listener + TCP stub.

## 8. Edge Cases & Decisions
- Already online (presence says `online`) → skip WOL, connect immediately.
- Sleep vs off indistinguishable — both `offline`; rely on `last seen` + still offer `wake`.
- ICMP blocked → require TCP check per host; `unknown` only when host unresolvable/misconfigured, never as steady state.
- Multi-NIC / VLAN: allow per-host `interface` + `broadcast`; `waker wake --interface eth0`.
- MAC formats: accept `AA-BB-..`, `AABB..`, lowercase; normalize.
- WOL needs wired NIC + BIOS enabled; surface hint on timeout: "no response in 120s — check BIOS/allow WOL, same subnet?".
- SSH: always `exec` system ssh (respects `~/.ssh/config`, agent, ProxyJump, `ControlPersist`); never embed keys. One-time `ssh-copy-id` removes per-use auth for both `connect` and `sleep`.
- Sleep-agent: token via `token_env` (never plaintext in logs); bind localhost + LAN only, no WAN exposure v1; `waker-agent` install folded into `waker add --install-agent user@host` (scp + enable service) post-v1.
- Parsec: client must already be logged in (peer_id from Computers tab → right-click → Copy Peer ID); validate `peer_id` non-empty in add/edit form; on timeout show "host up but Parsec not reachable — check Hosting enabled, logged-in session, firewall/UPnP". Waits on ping/TCP before launching `parsecd`, same state machine as SSH.
- Remote wake over internet: document Tailscale/subnet-router or router ARP relay; no port-forward magic in v1.
- Security: config `0600` if it ever holds secrets; log redaction; nopriv ports only except raw ping fallback to TCP.

## 9. Testing
- Unit: magic packet bytes, config validation, state machine timeouts (fake clock).
- Integration: dummy UDP server asserts packet; dummy TCP server simulates SSH port coming up after 3s → `connect` must wait then exec `echo`.
- Manual: real desktop + NAS + game server; measure wake-to-ssh p50/p95.
- Sleep: mock ssh executor asserts per-OS command; fake agent HTTP server asserts Bearer token → `sleep` must wait until presence `offline`.

## 10. Open Questions
1. Go vs Rust — ok with Go/Bubble Tea?
2. Should `connect` replace process (`syscall.Exec`) or spawn child to keep TUI logs?
3. v1 needs auto-discovery (arp-scan) or manual YAML is enough?
