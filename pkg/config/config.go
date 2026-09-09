package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tomer/waker/pkg/wol"
	"gopkg.in/yaml.v3"
)

type CheckConfig struct {
	Type string `yaml:"type"` // ping, tcp, arp
	Port int    `yaml:"port,omitempty"`
}

type SSHConfig struct {
	User string   `yaml:"user,omitempty"`
	Port int      `yaml:"port,omitempty"`
	Args []string `yaml:"args,omitempty"`
}

type ConnectAction struct {
	Type     string `yaml:"type"`               // ssh, parsec, mount, game, custom
	PeerID   string `yaml:"peer_id,omitempty"`  // for parsec
	Settings string `yaml:"settings,omitempty"` // for parsec e.g. client_vsync=1
	Run      string `yaml:"run,omitempty"`      // for custom or mount URL
	URL      string `yaml:"url,omitempty"`      // for mount / game
}

type SleepAction struct {
	Type     string `yaml:"type"`               // ssh, agent, custom
	Port     int    `yaml:"port,omitempty"`     // for agent (default 9876)
	TokenEnv string `yaml:"token_env,omitempty"`// environment variable storing token
	Token    string `yaml:"token,omitempty"`    // static token fallback
	Run      string `yaml:"run,omitempty"`      // for custom sleep command
}

type HostConfig struct {
	Name       string            `yaml:"name"`
	MAC        string            `yaml:"mac"`
	IP         string            `yaml:"ip,omitempty"`
	Broadcast  string            `yaml:"broadcast,omitempty"`
	Port       int               `yaml:"port,omitempty"`
	Interface  string            `yaml:"interface,omitempty"`
	Relay      *wol.RelayConfig  `yaml:"relay,omitempty"`
	SSH        *SSHConfig        `yaml:"ssh,omitempty"`
	Checks     []CheckConfig     `yaml:"checks,omitempty"`
	OnConnect  *ConnectAction    `yaml:"on_connect,omitempty"`
	OnSleep    *SleepAction      `yaml:"on_sleep,omitempty"`
}

type DefaultsConfig struct {
	Broadcast     string            `yaml:"broadcast,omitempty"`
	WOLPort       int               `yaml:"wol_port,omitempty"`
	Timeout       time.Duration     `yaml:"timeout,omitempty"`
	PollInterval  time.Duration     `yaml:"poll_interval,omitempty"`
	ProbeTimeout  time.Duration     `yaml:"probe_timeout,omitempty"`
	Relay         *wol.RelayConfig  `yaml:"relay,omitempty"`
	SSHOptions    []string          `yaml:"ssh_options,omitempty"`
}

type Config struct {
	Version  int            `yaml:"version"`
	Defaults DefaultsConfig `yaml:"defaults"`
	Hosts    []HostConfig   `yaml:"hosts"`
}

// DefaultConfigDir returns ~/.config/waker on all platforms (macOS, Linux, etc.).
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "waker"), nil
}

// DefaultConfigPath returns the path to ~/.config/waker/hosts.yaml (or hosts.yml if present).
func DefaultConfigPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}

	// Check if hosts.yml exists first
	ymlPath := filepath.Join(dir, "hosts.yml")
	if _, err := os.Stat(ymlPath); err == nil {
		return ymlPath, nil
	}

	// Check if hosts.yaml exists
	yamlPath := filepath.Join(dir, "hosts.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath, nil
	}

	// Default to hosts.yaml
	return yamlPath, nil
}

// ResolveConfigPath returns explicit path if non-empty, or resolves default in ~/.config/waker.
func ResolveConfigPath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	return DefaultConfigPath()
}

// Load loads the configuration from a given file path. If path is empty, DefaultConfigPath is used.
func Load(path string) (*Config, error) {
	var err error
	path, err = ResolveConfigPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file at %s: %w", path, err)
	}

	cfg := &Config{
		Defaults: DefaultsConfig{
			Broadcast:    "255.255.255.255",
			WOLPort:      9,
			Timeout:      120 * time.Second,
			PollInterval: 15 * time.Second,
			ProbeTimeout: 2 * time.Second,
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse yaml config: %w", err)
	}

	// Apply defaults and validate
	if cfg.Defaults.Broadcast == "" {
		cfg.Defaults.Broadcast = "255.255.255.255"
	}
	if cfg.Defaults.WOLPort == 0 {
		cfg.Defaults.WOLPort = 9
	}
	if cfg.Defaults.Timeout == 0 {
		cfg.Defaults.Timeout = 120 * time.Second
	}
	if cfg.Defaults.PollInterval == 0 {
		cfg.Defaults.PollInterval = 15 * time.Second
	}
	if cfg.Defaults.ProbeTimeout == 0 {
		cfg.Defaults.ProbeTimeout = 2 * time.Second
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate validates hosts and configuration semantics.
func (c *Config) Validate() error {
	names := make(map[string]bool)
	for i := range c.Hosts {
		h := &c.Hosts[i]
		if strings.TrimSpace(h.Name) == "" {
			return fmt.Errorf("host #%d is missing a name", i+1)
		}
		nameLower := strings.ToLower(h.Name)
		if names[nameLower] {
			return fmt.Errorf("duplicate host name %q", h.Name)
		}
		names[nameLower] = true

		if h.MAC == "" {
			return fmt.Errorf("host %q is missing MAC address", h.Name)
		}
		hw, err := wol.NormalizeMAC(h.MAC)
		if err != nil {
			return fmt.Errorf("host %q invalid MAC address: %w", h.Name, err)
		}
		h.MAC = hw.String()

		if h.IP != "" {
			if parsedIP := net.ParseIP(h.IP); parsedIP == nil {
				return fmt.Errorf("host %q has invalid IP address: %s", h.Name, h.IP)
			}
		}

		if h.Broadcast == "" {
			h.Broadcast = c.Defaults.Broadcast
		}
		if h.Port == 0 {
			h.Port = c.Defaults.WOLPort
		}

		// Check defaults for on_connect if empty
		if h.OnConnect == nil {
			if h.SSH != nil {
				h.OnConnect = &ConnectAction{Type: "ssh"}
			}
		}

		// Check checks defaults: if checks is empty and IP is present, default to ping + (tcp 22 if ssh defined)
		if len(h.Checks) == 0 && h.IP != "" {
			h.Checks = append(h.Checks, CheckConfig{Type: "ping"})
			if h.SSH != nil {
				port := 22
				if h.SSH.Port != 0 {
					port = h.SSH.Port
				}
				h.Checks = append(h.Checks, CheckConfig{Type: "tcp", Port: port})
			}
		}

		// Validate connect action
		if h.OnConnect != nil {
			switch h.OnConnect.Type {
			case "ssh":
				// ok
			case "parsec":
				if strings.TrimSpace(h.OnConnect.PeerID) == "" {
					return fmt.Errorf("host %q: parsec connect requires peer_id", h.Name)
				}
			case "mount":
				if h.OnConnect.URL == "" && h.OnConnect.Run == "" {
					return fmt.Errorf("host %q: mount connect requires url or run", h.Name)
				}
			case "game":
				if h.OnConnect.URL == "" && h.OnConnect.Run == "" {
					return fmt.Errorf("host %q: game connect requires url or run", h.Name)
				}
			case "custom":
				if strings.TrimSpace(h.OnConnect.Run) == "" {
					return fmt.Errorf("host %q: custom connect requires run command", h.Name)
				}
			case "":
				// empty is fine
			default:
				return fmt.Errorf("host %q: unknown connect type %q", h.Name, h.OnConnect.Type)
			}
		}

		// Validate sleep action
		if h.OnSleep != nil {
			switch h.OnSleep.Type {
			case "ssh", "":
				// ok
			case "agent":
				if h.OnSleep.Port == 0 {
					h.OnSleep.Port = 9876
				}
			case "custom":
				if strings.TrimSpace(h.OnSleep.Run) == "" {
					return fmt.Errorf("host %q: custom sleep requires run command", h.Name)
				}
			default:
				return fmt.Errorf("host %q: unknown sleep type %q", h.Name, h.OnSleep.Type)
			}
		}
	}
	return nil
}

// FindHost looks up a host by name (case-insensitive).
func (c *Config) FindHost(name string) (*HostConfig, error) {
	for i := range c.Hosts {
		if strings.EqualFold(c.Hosts[i].Name, name) {
			return &c.Hosts[i], nil
		}
	}
	return nil, fmt.Errorf("host %q not found in config", name)
}

// Save writes the configuration back to disk.
func (c *Config) Save(path string) error {
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return err
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// 0600 file permissions for security
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", path, err)
	}
	return nil
}

// UpsertHost adds or updates a host in configuration.
func (c *Config) UpsertHost(host HostConfig) error {
	if err := host.Validate(); err != nil {
		return err
	}
	for i, h := range c.Hosts {
		if strings.EqualFold(h.Name, host.Name) {
			c.Hosts[i] = host
			return nil
		}
	}
	c.Hosts = append(c.Hosts, host)
	return nil
}

// DeleteHost removes a host from configuration.
func (c *Config) DeleteHost(name string) bool {
	for i, h := range c.Hosts {
		if strings.EqualFold(h.Name, name) {
			c.Hosts = append(c.Hosts[:i], c.Hosts[i+1:]...)
			return true
		}
	}
	return false
}

// Validate single HostConfig
func (h *HostConfig) Validate() error {
	if strings.TrimSpace(h.Name) == "" {
		return errors.New("host name cannot be empty")
	}
	if h.MAC == "" {
		return errors.New("MAC address is required")
	}
	hw, err := wol.NormalizeMAC(h.MAC)
	if err != nil {
		return fmt.Errorf("invalid MAC address %q: %w", h.MAC, err)
	}
	h.MAC = hw.String()

	if h.IP != "" {
		if parsedIP := net.ParseIP(h.IP); parsedIP == nil {
			return fmt.Errorf("invalid IP address: %s", h.IP)
		}
	}
	return nil
}
