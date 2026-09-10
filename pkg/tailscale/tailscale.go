package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	ErrTailscaleNotFound = errors.New("tailscale binary not found")
)

// Status represents the current state of Tailscale.
type Status struct {
	Installed    bool
	BinaryPath   string
	BackendState string
	IsUp         bool
	IP           string
	Version      string
	Err          error
}

// Client defines the interface for interacting with Tailscale.
type Client interface {
	IsUp(ctx context.Context) (bool, error)
	Up(ctx context.Context) error
	Down(ctx context.Context) error
	Status(ctx context.Context) (*Status, error)
}

// CLIClient interacts with the official tailscale CLI binary.
type CLIClient struct {
	BinaryPath string
}

// NewCLIClient creates a new CLIClient with automatic binary detection.
func NewCLIClient() *CLIClient {
	return &CLIClient{}
}

// FindTailscaleBinary attempts to locate the tailscale CLI executable.
func FindTailscaleBinary() (string, error) {
	// 1. Check PATH
	if path, err := exec.LookPath("tailscale"); err == nil {
		return path, nil
	}

	// 2. Common platform paths
	candidates := []string{
		"/usr/local/bin/tailscale",
		"/opt/homebrew/bin/tailscale",
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		"/usr/bin/tailscale",
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

// Up runs `tailscale up`.
// Note: flags are deliberately omitted here. Passing any flag (like --timeout) causes Tailscale
// to fail with an error if non-default settings (such as exit nodes or route preferences)
// were previously configured on the device.
func (c *CLIClient) Up(ctx context.Context) error {
	bin, err := c.binary()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "up")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tailscale up failed (%s): %w", strings.TrimSpace(string(out)), err)
	}

	if isUp, _ := c.IsUp(ctx); isUp {
		return nil
	}

	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if isUp, _ := c.IsUp(ctx); isUp {
				return nil
			}
		}
	}
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
	// Only bring down if Waker started it on launch or during this session
	if m.startedByWaker {
		m.startedByWaker = false
		return m.client.Down(ctx)
	}
	return nil
}

// StartedByWaker returns true if Tailscale was started by this Waker instance.
func (m *Manager) StartedByWaker() bool {
	return m.startedByWaker
}

// WasUpOnLaunch returns whether Tailscale was active when Waker launched.
func (m *Manager) WasUpOnLaunch() bool {
	return m.wasUpOnLaunch
}

// SetStartedByWaker overrides the startedByWaker flag.
func (m *Manager) SetStartedByWaker(v bool) {
	m.startedByWaker = v
}

// Client returns the underlying Client.
func (m *Manager) Client() Client {
	return m.client
}

// Status returns the current status.
func (m *Manager) Status(ctx context.Context) (*Status, error) {
	return m.client.Status(ctx)
}

// Up manually starts Tailscale.
func (m *Manager) Up(ctx context.Context) error {
	return m.client.Up(ctx)
}

// Down manually stops Tailscale.
func (m *Manager) Down(ctx context.Context) error {
	return m.client.Down(ctx)
}
