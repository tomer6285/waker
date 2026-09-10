package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var (
	ErrTailscaleNotFound = errors.New("tailscale binary not found")
)

// Status represents the current operational status of Tailscale on this host.
type Status struct {
	Installed    bool   `json:"installed"`
	BinaryPath   string `json:"binary_path,omitempty"`
	BackendState string `json:"backend_state,omitempty"`
	IsUp         bool   `json:"is_up"`
	IP           string `json:"ip,omitempty"`
	Version      string `json:"version,omitempty"`
	Err          error  `json:"-"`
}

// Client defines the interface for interacting with Tailscale.
type Client interface {
	IsUp(ctx context.Context) (bool, error)
	Up(ctx context.Context) error
	Down(ctx context.Context) error
	Status(ctx context.Context) (*Status, error)
}

// CLIClient interacts with Tailscale via the local CLI binary.
type CLIClient struct {
	BinaryPath string
}

// NewCLIClient creates a new CLI-based Tailscale client.
func NewCLIClient() *CLIClient {
	return &CLIClient{}
}

// FindTailscaleBinary locates the tailscale binary on the machine.
func FindTailscaleBinary() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	candidates := []string{
		"/usr/local/bin/tailscale",
		"/opt/homebrew/bin/tailscale",
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		"/usr/bin/tailscale",
		"/bin/tailscale",
	}
	for _, cand := range candidates {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand, nil
		}
	}
	return "", ErrTailscaleNotFound
}

func (c *CLIClient) binary() (string, error) {
	if c.BinaryPath != "" {
		return c.BinaryPath, nil
	}
	return FindTailscaleBinary()
}

// Status returns the current status by querying `tailscale status --json`.
func (c *CLIClient) Status(ctx context.Context) (*Status, error) {
	bin, err := c.binary()
	if err != nil {
		return &Status{
			Installed: false,
			Err:       err,
		}, nil
	}

	cmd := exec.CommandContext(ctx, bin, "status", "--json")
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return &Status{
			Installed:    true,
			BinaryPath:   bin,
			BackendState: "Stopped",
			IsUp:         false,
			Err:          err,
		}, nil
	}

	var raw struct {
		Version      string   `json:"Version"`
		BackendState string   `json:"BackendState"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	}
	if jsonErr := json.Unmarshal(out, &raw); jsonErr != nil {
		outStr := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(outStr), "stopped") {
			return &Status{
				Installed:    true,
				BinaryPath:   bin,
				BackendState: "Stopped",
				IsUp:         false,
			}, nil
		}
		return &Status{
			Installed:  true,
			BinaryPath: bin,
			IsUp:       false,
			Err:        jsonErr,
		}, nil
	}

	ip := ""
	if len(raw.TailscaleIPs) > 0 {
		ip = raw.TailscaleIPs[0]
	}

	isUp := strings.EqualFold(raw.BackendState, "Running")
	return &Status{
		Installed:    true,
		BinaryPath:   bin,
		BackendState: raw.BackendState,
		IsUp:         isUp,
		IP:           ip,
		Version:      raw.Version,
	}, nil
}

// IsUp checks whether Tailscale backend state is currently Running.
func (c *CLIClient) IsUp(ctx context.Context) (bool, error) {
	st, err := c.Status(ctx)
	if err != nil {
		return false, err
	}
	return st.IsUp, nil
}

// Up runs `tailscale up --timeout=15s`.
func (c *CLIClient) Up(ctx context.Context) error {
	bin, err := c.binary()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "up", "--timeout=15s")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tailscale up failed (%s): %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Down runs `tailscale down`.
func (c *CLIClient) Down(ctx context.Context) error {
	bin, err := c.binary()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "down")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tailscale down failed (%s): %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Manager orchestrates Tailscale lifecycle (launch and quit actions) based on configuration.
type Manager struct {
	client         Client
	startedByWaker bool
	wasUpOnLaunch  bool
	initialChecked bool
}

// NewManager creates a Manager with the default CLIClient.
func NewManager() *Manager {
	return NewManagerWithClient(NewCLIClient())
}

// NewManagerWithClient creates a Manager with a custom Client (e.g. for testing).
func NewManagerWithClient(client Client) *Manager {
	return &Manager{
		client: client,
	}
}

// OnLaunch checks Tailscale status when waker launches.
// If autoManage is enabled:
// - If Tailscale is not up, it runs `tailscale up` and remembers that waker started it.
// - If Tailscale is already up, it leaves it alone and does not mark it as started by waker.
func (m *Manager) OnLaunch(ctx context.Context, autoManage bool) (bool, error) {
	if !autoManage {
		return false, nil
	}
	m.initialChecked = true
	isUp, err := m.client.IsUp(ctx)
	if err != nil {
		isUp = false
	}
	m.wasUpOnLaunch = isUp
	if !isUp {
		if err := m.client.Up(ctx); err != nil {
			return false, err
		}
		m.startedByWaker = true
		return true, nil
	}
	// Was already up on launch
	m.startedByWaker = false
	return false, nil
}

// OnQuit executes when waker quits.
// If Tailscale was started by waker during this session, it brings Tailscale down.
// If Tailscale was already up prior to launch, it leaves it running.
func (m *Manager) OnQuit(ctx context.Context, autoManage bool) error {
	if !autoManage {
		return nil
	}
	if m.startedByWaker {
		if err := m.client.Down(ctx); err != nil {
			return err
		}
		m.startedByWaker = false
	}
	return nil
}

// StartedByWaker reports whether Tailscale was started by waker in this session.
func (m *Manager) StartedByWaker() bool {
	return m.startedByWaker
}

// SetStartedByWaker overrides or updates the started-by-waker flag.
func (m *Manager) SetStartedByWaker(v bool) {
	m.startedByWaker = v
}

// WasUpOnLaunch reports whether Tailscale was already up when waker launched.
func (m *Manager) WasUpOnLaunch() bool {
	return m.wasUpOnLaunch
}

// InitialChecked reports whether OnLaunch has run.
func (m *Manager) InitialChecked() bool {
	return m.initialChecked
}

// Client returns the underlying Client.
func (m *Manager) Client() Client {
	return m.client
}

// Status queries the current status through the Client.
func (m *Manager) Status(ctx context.Context) (*Status, error) {
	return m.client.Status(ctx)
}

// Up manually brings Tailscale up through the Client.
func (m *Manager) Up(ctx context.Context) error {
	return m.client.Up(ctx)
}

// Down manually brings Tailscale down through the Client.
func (m *Manager) Down(ctx context.Context) error {
	return m.client.Down(ctx)
}
