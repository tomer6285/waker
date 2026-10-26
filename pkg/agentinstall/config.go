// Package agentinstall owns the on-target agent configuration and installation.
package agentinstall

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

type Config struct {
	Bind  string `json:"bind"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (c Config) Validate() error {
	ip := net.ParseIP(c.Bind)
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("bind must be a specific local IP address (prefer the target's Tailscale IP)")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if len(c.Token) < 32 || strings.ContainsAny(c.Token, "\r\n\x00") {
		return fmt.Errorf("setup token must contain at least 32 characters and no line breaks")
	}
	return nil
}

func DetectBind() (string, error) {
	cmd := exec.Command("tailscale", "ip", "-4")
	out, err := cmd.Output()
	if err != nil && runtime.GOOS == "darwin" {
		out, err = exec.Command("/Applications/Tailscale.app/Contents/MacOS/Tailscale", "ip", "-4").Output()
	}
	if err == nil {
		for _, line := range strings.Fields(string(out)) {
			if net.ParseIP(line) != nil {
				return line, nil
			}
		}
	}
	return "", fmt.Errorf("could not detect a Tailscale address; connect Tailscale or pass --bind with a specific local IP")
}

func ReadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("invalid agent configuration")
	}
	return c, c.Validate()
}

// WritePrivate replaces a file atomically and never follows an existing file symlink.
func WritePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".waker-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if runtime.GOOS == "windows" {
		current, err := user.Current()
		if err != nil {
			f.Close()
			return err
		}
		// chmod does not restrict Windows ACLs. Protect the empty file before secrets
		// are written, including pairing exports outside the installation directory.
		acl := exec.Command("icacls.exe", f.Name(), "/inheritance:r", "/grant:r", "*"+current.Uid+":F", "*S-1-5-18:F", "*S-1-5-32-544:F")
		if err := acl.Run(); err != nil {
			f.Close()
			return fmt.Errorf("protect credential file ACL: %w", err)
		}
	}

	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func WriteConfig(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivate(path, append(b, '\n'))
}

type Paths struct{ Dir, Binary, Config, Service, Log string }

func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return PathsFor(runtime.GOOS, home, os.Getenv("ProgramData")), nil
}
func PathsFor(platform, home, programData string) Paths {
	switch platform {
	case "linux":
		return Paths{"/etc/waker-agent", "/usr/local/bin/waker-agent", "/etc/waker-agent/config.json", "/etc/systemd/system/waker-agent.service", ""}
	case "windows":
		if programData == "" {
			programData = `C:\ProgramData`
		}
		dir := filepath.Join(programData, "Waker")
		return Paths{dir, filepath.Join(dir, "waker-agent.exe"), filepath.Join(dir, "config.json"), "Waker Agent", filepath.Join(dir, "agent.log")}
	default:
		dir := filepath.Join(home, ".local", "share", "waker-agent")
		return Paths{dir, filepath.Join(home, ".local", "bin", "waker-agent"), filepath.Join(dir, "config.json"), filepath.Join(home, "Library", "LaunchAgents", "com.waker.agent.plist"), filepath.Join(dir, "agent.log")}
	}
}

func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func PSQuote(s string) string    { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
