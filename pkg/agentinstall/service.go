package agentinstall

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Runner func(name string, args ...string) error

type Manager struct {
	Platform string
	Paths    Paths
	Run      Runner
}

func NewManager(out, errOut io.Writer) (*Manager, error) {
	p, err := DefaultPaths()
	if err != nil {
		return nil, err
	}
	return &Manager{Platform: runtime.GOOS, Paths: p, Run: func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdout, cmd.Stderr = out, errOut
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", name, err)
		}
		return nil
	}}, nil
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func ServiceDefinition(platform string, p Paths) string {
	switch platform {
	case "linux":
		return fmt.Sprintf(`[Unit]
Description=Waker sleep agent
Wants=network-online.target tailscaled.service
After=network-online.target tailscaled.service
StartLimitIntervalSec=0

[Service]
Type=simple
User=root
ExecStart=%s run --config %s
Restart=on-failure
RestartSec=5
UMask=0077

[Install]
WantedBy=multi-user.target
`, p.Binary, p.Config)
	case "darwin":
		return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.waker.agent</string>
<key>ProgramArguments</key><array><string>%s</string><string>run</string><string>--config</string><string>%s</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>10</integer>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, xmlText(p.Binary), xmlText(p.Config), xmlText(p.Log), xmlText(p.Log))
	default:
		// The console binary is hosted by Task Scheduler, not registered with sc.exe.
		return fmt.Sprintf("$ErrorActionPreference = 'Continue'\nwhile ($true) {\n & %s run --config %s >> %s 2>&1\n Start-Sleep -Seconds 5\n}\n", PSQuote(p.Binary), PSQuote(p.Config), PSQuote(p.Log))
	}
}

func (m *Manager) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func (m *Manager) powershell(script string) error {
	return m.Run("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop'; "+script)
}

// Install preserves credentials supplied by the caller and replaces only Waker-owned files.
func (m *Manager) Install(source string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if m.Platform != "linux" && m.Platform != "darwin" && m.Platform != "windows" {
		return fmt.Errorf("unsupported OS %s", m.Platform)
	}
	if err := os.MkdirAll(m.Paths.Dir, 0700); err != nil {
		return err
	}
	if m.Platform == "windows" {
		// Restrict the directory before writing credentials or executable content.
		// Replace the DACL instead of merely removing inherited entries: an
		// existing explicit Users grant must not permit replacing SYSTEM code.
		acl := `$acl = [System.Security.AccessControl.DirectorySecurity]::new(); $acl.SetAccessRuleProtection($true, $false); foreach ($sid in @('S-1-5-18', 'S-1-5-32-544')) { $id = [System.Security.Principal.SecurityIdentifier]::new($sid); $rule = [System.Security.AccessControl.FileSystemAccessRule]::new($id, 'FullControl', 'ContainerInherit, ObjectInherit', 'None', 'Allow'); $acl.AddAccessRule($rule) }; Set-Acl -LiteralPath ` + PSQuote(m.Paths.Dir) + ` -AclObject $acl`
		if err := m.powershell(acl); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(m.Paths.Binary), 0755); err != nil {
		return err
	}
	// Read before stopping: a bad source must not disrupt an existing service.
	b, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	// Stop only an installed service. A failed stop must not be silently ignored.
	if _, err := os.Stat(m.Paths.Service); m.Platform != "windows" && err == nil {
		if err := m.Stop(); err != nil {
			return fmt.Errorf("stop existing installation: %w", err)
		}
	} else if m.Platform != "windows" && err != nil && !os.IsNotExist(err) {
		return err
	}
	if m.Platform == "windows" {
		if err := m.powershell("if (Get-ScheduledTask -TaskName 'Waker Agent' -ErrorAction SilentlyContinue) { Stop-ScheduledTask -TaskName 'Waker Agent' }"); err != nil {
			return err
		}
	}
	if err := WritePrivate(m.Paths.Binary, b); err != nil {
		return err
	}
	if err := os.Chmod(m.Paths.Binary, 0755); err != nil {
		return err
	}
	if err := WriteConfig(m.Paths.Config, cfg); err != nil {
		return err
	}
	if m.Platform == "windows" {
		scriptPath := filepath.Join(m.Paths.Dir, "start-agent.ps1")
		if err := WritePrivate(scriptPath, []byte(ServiceDefinition(m.Platform, m.Paths))); err != nil {
			return err
		}
		script := fmt.Sprintf(`$a = New-ScheduledTaskAction -Execute "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -Argument %s; $t = New-ScheduledTaskTrigger -AtStartup; $p = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest; $s = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable; Register-ScheduledTask -TaskName 'Waker Agent' -Action $a -Trigger $t -Principal $p -Settings $s -Force | Out-Null; Start-ScheduledTask -TaskName 'Waker Agent'`, PSQuote(`-NoProfile -ExecutionPolicy Bypass -File "`+scriptPath+`"`))
		return m.powershell(script)
	}
	if err := os.MkdirAll(filepath.Dir(m.Paths.Service), 0755); err != nil {
		return err
	}
	if err := WritePrivate(m.Paths.Service, []byte(ServiceDefinition(m.Platform, m.Paths))); err != nil {
		return err
	}
	if m.Platform == "linux" {
		if err := m.Run("systemctl", "daemon-reload"); err != nil {
			return err
		}
		return m.Run("systemctl", "enable", "--now", "waker-agent")
	}
	if err := m.Run("launchctl", "bootstrap", m.domain(), m.Paths.Service); err != nil {
		return fmt.Errorf("load LaunchAgent (a logged-in desktop session is required): %w", err)
	}
	return nil
}
func (m *Manager) Stop() error {
	switch m.Platform {
	case "linux":
		return m.Run("systemctl", "stop", "waker-agent")
	case "darwin":
		// bootout removes the registration; install bootstraps it again.
		return m.Run("/bin/sh", "-c", "if launchctl print "+ShellQuote(m.domain()+"/com.waker.agent")+" >/dev/null 2>&1; then launchctl bootout "+ShellQuote(m.domain())+" "+ShellQuote(m.Paths.Service)+"; fi")
	case "windows":
		return m.powershell("Stop-ScheduledTask -TaskName 'Waker Agent'")
	default:
		return fmt.Errorf("unsupported OS %s", m.Platform)
	}
}
func (m *Manager) Status() error {
	switch m.Platform {
	case "linux":
		return m.Run("systemctl", "status", "waker-agent", "--no-pager")
	case "darwin":
		return m.Run("launchctl", "print", m.domain()+"/com.waker.agent")
	case "windows":
		return m.powershell("Get-ScheduledTask -TaskName 'Waker Agent'; Get-ScheduledTaskInfo -TaskName 'Waker Agent'")
	default:
		return fmt.Errorf("unsupported OS %s", m.Platform)
	}
}
func (m *Manager) Logs() error {
	if m.Platform == "linux" {
		return m.Run("journalctl", "-u", "waker-agent", "-n", "50", "--no-pager")
	}
	b, err := os.ReadFile(m.Paths.Log)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) > 50 {
		lines = lines[len(lines)-50:]
	}
	fmt.Println(strings.Join(lines, "\n"))
	return nil
}
func (m *Manager) Uninstall() error {
	// Do not remove files unless disabling the running service succeeds.
	switch m.Platform {
	case "linux":
		if err := m.Run("systemctl", "disable", "--now", "waker-agent"); err != nil {
			return err
		}
	case "darwin":
		if err := m.Stop(); err != nil {
			return err
		}
	case "windows":
		if err := m.Stop(); err != nil {
			return err
		}
		if err := m.powershell("Unregister-ScheduledTask -TaskName 'Waker Agent' -Confirm:$false"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported OS %s", m.Platform)
	}
	if m.Platform != "windows" {
		if err := os.Remove(m.Paths.Service); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if m.Platform == "linux" {
		return m.Run("systemctl", "daemon-reload")
	}
	// Preserve credentials/binary/logs for repair or reinstallation. Running Windows
	// executables cannot always be removed; autostart is already disabled above.
	return nil
}
