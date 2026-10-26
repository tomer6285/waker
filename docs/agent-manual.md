# Manual agent and service setup

**The agent runs on the computer you want to sleep, not on the wake relay.** It handles `POST /sleep` and `GET /health`; it cannot wake a sleeping machine. The relay requires no Waker installation.

Use the agent when SSH suspend is unavailable or inconvenient, especially on Windows. For ordinary Linux/macOS SSH sleep, no agent is needed. The service account must have permission to suspend the target.

```sh
go build -o bin/waker-agent ./cmd/waker-agent
# Then run bin/waker-agent on the target.

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

### Autostart on Linux (systemd)

Run these steps on the **target**, from the repository. Stop any manually running agent first. This system service runs as root so `systemctl suspend` does not require an interactive password. Replace `100.101.102.103` below with the target's address from `tailscale ip -4`.

```sh
go build -o bin/waker-agent ./cmd/waker-agent
sudo install -m 755 bin/waker-agent /usr/local/bin/waker-agent
sudo touch /etc/waker-agent.env
sudo chmod 600 /etc/waker-agent.env
sudoedit /etc/waker-agent.env
```

Add the same strong token configured on your client (replace the placeholder; do not commit this file):

```ini
WAKER_AGENT_TOKEN=your-shared-token
```

Create the service with `sudoedit /etc/systemd/system/waker-agent.service`:

```ini
[Unit]
Description=Waker sleep agent
Wants=network-online.target tailscaled.service
After=network-online.target tailscaled.service
StartLimitIntervalSec=0

[Service]
Type=simple
User=root
EnvironmentFile=/etc/waker-agent.env
ExecStart=/usr/local/bin/waker-agent --bind 100.101.102.103 --port 9876
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable it now and at boot, then inspect its status/logs:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now waker-agent
sudo systemctl status waker-agent
sudo journalctl -u waker-agent -n 50 --no-pager
```

The restart policy retries every five seconds if the Tailscale address is not ready. After changing the token or binary, run `sudo systemctl restart waker-agent`. To stop it and disable autostart, run `sudo systemctl disable --now waker-agent`. See the [systemd service documentation](https://www.freedesktop.org/software/systemd/man/257/systemd.service.html).

### Autostart on macOS (launchd, at login)

The macOS agent uses `osascript` and System Events, so run it as a **LaunchAgent in the logged-in user's session**. This starts at login, rather than before login at boot, and stops at logout. First verify sleep works when running the agent manually as that user, and approve any Automation permission prompts; permission failures appear in the agent log.

On the target, from the repository:

```sh
go build -o bin/waker-agent ./cmd/waker-agent
sudo mkdir -p /usr/local/bin
sudo install -m 755 bin/waker-agent /usr/local/bin/waker-agent
mkdir -p ~/.config/waker ~/Library/LaunchAgents ~/Library/Logs
touch ~/.config/waker/agent.env
chmod 600 ~/.config/waker/agent.env
vi ~/.config/waker/agent.env
```

Put the client's shared token in this file using shell assignment syntax (a random hexadecimal token avoids quoting issues):

```sh
WAKER_AGENT_TOKEN='your-shared-token'
```

Save this as `~/Library/LaunchAgents/com.waker.agent.plist`. Replace the example Tailscale IP and both `/Users/YOUR_USER` log paths with the target's actual values. The plist does not expand `~` in paths.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.waker.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>-c</string>
    <string>. "$HOME/.config/waker/agent.env"; export WAKER_AGENT_TOKEN; exec /usr/local/bin/waker-agent --bind 100.101.102.103 --port 9876</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>/Users/YOUR_USER/Library/Logs/waker-agent.log</string>
  <key>StandardErrorPath</key><string>/Users/YOUR_USER/Library/Logs/waker-agent.log</string>
</dict>
</plist>
```

Load it from a terminal in that user's desktop session. These commands use POSIX shell syntax; if your shell is fish, enter `sh` first:

```sh
plutil -lint ~/Library/LaunchAgents/com.waker.agent.plist
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.waker.agent.plist
launchctl print "gui/$(id -u)/com.waker.agent"
tail -n 50 ~/Library/Logs/waker-agent.log
```

`KeepAlive` retries after an exit, including when the Tailscale IP is not yet available. After changing the token or binary, run `launchctl kickstart -k "gui/$(id -u)/com.waker.agent"`. To unload it, run `launchctl bootout "gui/$(id -u)" ~/Library/LaunchAgents/com.waker.agent.plist`; remove the plist to disable future login starts. See Apple's [launchd guide](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html).

### Autostart on Windows (Task Scheduler, at boot)

Use Task Scheduler: `waker-agent.exe` is a console application and cannot be registered directly with `sc.exe` as a Windows service. Run the following in **PowerShell as Administrator on the target**, from the repository. Stop any manually running agent first.

```powershell
go build -o bin\waker-agent.exe ./cmd/waker-agent
New-Item -ItemType Directory -Force 'C:\ProgramData\Waker'
icacls.exe 'C:\ProgramData\Waker' /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F'
Copy-Item .\bin\waker-agent.exe 'C:\ProgramData\Waker\waker-agent.exe'
New-Item -ItemType File -Path 'C:\ProgramData\Waker\agent-token.txt'
notepad.exe 'C:\ProgramData\Waker\agent-token.txt'
```

The directory is restricted to SYSTEM and Administrators. Put **only the shared token** in `agent-token.txt`, save it, and close Notepad. The scheduled task reads this protected file; it does not inherit the token from your interactive shell.

Save the following as `C:\ProgramData\Waker\start-agent.ps1`, replacing the example IP with the target's address from `tailscale ip -4`:

```powershell
$env:WAKER_AGENT_TOKEN = (Get-Content -Raw 'C:\ProgramData\Waker\agent-token.txt').Trim()
while ($true) {
    & 'C:\ProgramData\Waker\waker-agent.exe' --bind 100.101.102.103 --port 9876 >> 'C:\ProgramData\Waker\agent.log' 2>&1
    Start-Sleep -Seconds 5
}
```

Register it to run as SYSTEM at startup, even before anyone logs in:

```powershell
$action = New-ScheduledTaskAction -Execute "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -Argument '-NoProfile -ExecutionPolicy Bypass -File "C:\ProgramData\Waker\start-agent.ps1"'
$trigger = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
Register-ScheduledTask -TaskName 'Waker Agent' -Action $action -Trigger $trigger -Principal $principal -Settings $settings
Start-ScheduledTask -TaskName 'Waker Agent'
Get-ScheduledTask -TaskName 'Waker Agent'
Get-Content 'C:\ProgramData\Waker\agent.log' -Tail 50
```

The wrapper retries every five seconds while the Tailscale address is unavailable or if the agent exits. The task has no execution time limit. After changing the token or binary, stop and start the task; stop it before replacing the executable:

```powershell
Stop-ScheduledTask -TaskName 'Waker Agent'
Start-ScheduledTask -TaskName 'Waker Agent'
```

To disable autostart, run `Disable-ScheduledTask -TaskName 'Waker Agent'` and `Stop-ScheduledTask -TaskName 'Waker Agent'`. If Windows Firewall blocks access, allow inbound TCP port 9876 only on the target's Tailscale address and from the intended tailnet clients. See Microsoft's [task principal](https://learn.microsoft.com/en-us/powershell/module/scheduledtasks/new-scheduledtaskprincipal) and [task settings](https://learn.microsoft.com/en-us/powershell/module/scheduledtasks/new-scheduledtasksettingsset) documentation.

For every platform, keep the client's sleep method set to `agent`, port `9876`, with the same token. Verify reachability with `curl http://TARGET_TAILSCALE_IP:9876/health` (or `Invoke-RestMethod http://TARGET_TAILSCALE_IP:9876/health` in PowerShell). Health checks do not require the token and do not suspend the target. Then use `waker sleep HOST --wait` when you are ready to test suspension; authorization or OS sleep failures are logged on the target.

