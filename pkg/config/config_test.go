package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigLoadAndValidate(t *testing.T) {
	yamlContent := `
version: 1
defaults:
  broadcast: 192.168.1.255
  wol_port: 9
  timeout: 60s
hosts:
  - name: desktop
    mac: "AA:BB:CC:DD:EE:FF"
    ip: 192.168.1.10
    ssh:
      user: tomer
      port: 22
    on_connect:
      type: ssh
    on_sleep:
      type: ssh
  - name: gaming-pc
    mac: "de-ad-be-ef-00-01"
    ip: 192.168.1.50
    on_connect:
      type: parsec
      peer_id: "test1234"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hosts.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(cfg.Hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(cfg.Hosts))
	}

	if cfg.Defaults.Timeout != 60*time.Second {
		t.Errorf("expected timeout 60s, got %v", cfg.Defaults.Timeout)
	}

	desktop, err := cfg.FindHost("desktop")
	if err != nil {
		t.Fatalf("FindHost desktop failed: %v", err)
	}
	if desktop.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected normalized mac aa:bb:cc:dd:ee:ff, got %s", desktop.MAC)
	}
	if desktop.Broadcast != "192.168.1.255" {
		t.Errorf("expected default broadcast 192.168.1.255, got %s", desktop.Broadcast)
	}

	gaming, err := cfg.FindHost("GAMING-PC")
	if err != nil {
		t.Fatalf("FindHost case-insensitive failed: %v", err)
	}
	if gaming.OnConnect.PeerID != "test1234" {
		t.Errorf("expected peer_id test1234, got %s", gaming.OnConnect.PeerID)
	}

	// Test host with hostname instead of IP literal
	hostnameYaml := `
hosts:
  - name: ts-host
    mac: "11:22:33:44:55:66"
    ip: "my-desktop.tailnet.ts.net"
`
	configPath2 := filepath.Join(tmpDir, "hosts_hostname.yaml")
	_ = os.WriteFile(configPath2, []byte(hostnameYaml), 0600)
	cfgHostname, err := Load(configPath2)
	if err != nil {
		t.Fatalf("Load with hostname failed: %v", err)
	}
	if cfgHostname.Hosts[0].IP != "my-desktop.tailnet.ts.net" {
		t.Errorf("expected hostname preserved, got %s", cfgHostname.Hosts[0].IP)
	}
}

func TestConfigInvalidMAC(t *testing.T) {
	yamlContent := `
hosts:
  - name: bad
    mac: "invalid-mac"
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hosts.yaml")
	_ = os.WriteFile(configPath, []byte(yamlContent), 0600)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for invalid MAC, got nil")
	}
}

func TestConfigDirAndYmlFallback(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir, err := DefaultConfigDir()
	if err != nil {
		t.Fatalf("DefaultConfigDir failed: %v", err)
	}
	expectedDir := filepath.Join(tmpHome, ".config", "waker")
	if dir != expectedDir {
		t.Errorf("expected %s, got %s", expectedDir, dir)
	}

	// Default when none exists is hosts.yaml
	defaultPath, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath failed: %v", err)
	}
	expectedYaml := filepath.Join(expectedDir, "hosts.yaml")
	if defaultPath != expectedYaml {
		t.Errorf("expected %s, got %s", expectedYaml, defaultPath)
	}

	// If hosts.yml exists, it should be preferred/detected
	if err := os.MkdirAll(expectedDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	ymlPath := filepath.Join(expectedDir, "hosts.yml")
	if err := os.WriteFile(ymlPath, []byte("hosts:\n  - name: yml-host\n    mac: aa:bb:cc:dd:ee:ff\n"), 0600); err != nil {
		t.Fatalf("write yml failed: %v", err)
	}

	detectedPath, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath failed: %v", err)
	}
	if detectedPath != ymlPath {
		t.Errorf("expected %s, got %s", ymlPath, detectedPath)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with empty path failed: %v", err)
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].Name != "yml-host" {
		t.Errorf("expected yml-host loaded from hosts.yml, got %+v", cfg.Hosts)
	}
}

func TestConfigSettingsAutoTailscale(t *testing.T) {
	// Test default when no settings block exists in YAML
	defaultYaml := `
version: 1
hosts:
  - name: default-host
    mac: "11:22:33:44:55:66"
`
	tmpDir := t.TempDir()
	defaultCfgPath := filepath.Join(tmpDir, "default_hosts.yaml")
	if err := os.WriteFile(defaultCfgPath, []byte(defaultYaml), 0600); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	defCfg, err := Load(defaultCfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if defCfg.Settings.AutoTailscale {
		t.Errorf("expected AutoTailscale to default to false on first launch / unconfigured, got true")
	}

	// Test when explicitly set to true
	yamlContent := `
version: 1
settings:
  auto_tailscale: true
hosts:
  - name: my-host
    mac: "11:22:33:44:55:66"
`
	configPath := filepath.Join(tmpDir, "hosts.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !cfg.Settings.AutoTailscale {
		t.Errorf("expected AutoTailscale to be true")
	}

	// Test saving false and reloading
	cfg.Settings.AutoTailscale = false
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	reloaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if reloaded.Settings.AutoTailscale {
		t.Errorf("expected AutoTailscale to be false after save/reload")
	}
}

func TestHostConfigValidate(t *testing.T) {
	// Valid host with IP
	h1 := HostConfig{
		Name: "desktop",
		MAC:  "AA:BB:CC:DD:EE:FF",
		IP:   "192.168.1.50",
	}
	if err := h1.Validate(); err != nil {
		t.Fatalf("expected valid host with IP to pass validation, got: %v", err)
	}

	// Valid host with hostname
	h2 := HostConfig{
		Name: "media-server",
		MAC:  "AA:BB:CC:DD:EE:FF",
		IP:   "media-server.tailnet.ts.net",
	}
	if err := h2.Validate(); err != nil {
		t.Fatalf("expected valid host with hostname to pass validation, got: %v", err)
	}

	// Valid host with local hostname
	h3 := HostConfig{
		Name: "living-room-pc",
		MAC:  "AA:BB:CC:DD:EE:FF",
		IP:   "desktop.local",
	}
	if err := h3.Validate(); err != nil {
		t.Fatalf("expected valid host with .local hostname to pass validation, got: %v", err)
	}

	// Missing name / nickname
	hBadName := HostConfig{
		Name: "",
		MAC:  "AA:BB:CC:DD:EE:FF",
	}
	if err := hBadName.Validate(); err == nil {
		t.Fatal("expected error for empty nickname, got nil")
	}

	// Invalid hostname with spaces or special chars
	hBadIP := HostConfig{
		Name: "bad-host",
		MAC:  "AA:BB:CC:DD:EE:FF",
		IP:   "invalid host name",
	}
	if err := hBadIP.Validate(); err == nil {
		t.Fatal("expected error for invalid IP/hostname with spaces, got nil")
	}
}
