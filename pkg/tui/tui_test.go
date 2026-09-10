package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tomer/waker/pkg/config"
	"github.com/tomer/waker/pkg/presence"
	"github.com/tomer/waker/pkg/store"
	"github.com/tomer/waker/pkg/tailscale"
)

type mockTailscaleClient struct {
	isUp      bool
	upCalls   int
	downCalls int
}

func (m *mockTailscaleClient) IsUp(ctx context.Context) (bool, error) {
	return m.isUp, nil
}

func (m *mockTailscaleClient) Up(ctx context.Context) error {
	m.upCalls++
	m.isUp = true
	return nil
}

func (m *mockTailscaleClient) Down(ctx context.Context) error {
	m.downCalls++
	m.isUp = false
	return nil
}

func (m *mockTailscaleClient) Status(ctx context.Context) (*tailscale.Status, error) {
	state := "Stopped"
	ip := ""
	if m.isUp {
		state = "Running"
		ip = "100.64.1.2"
	}
	return &tailscale.Status{
		Installed:    true,
		BinaryPath:   "/usr/local/bin/tailscale",
		BackendState: state,
		IsUp:         m.isUp,
		IP:           ip,
		Version:      "1.98.0",
	}, nil
}

func setupTestModel(t *testing.T, autoTS bool) (Model, *mockTailscaleClient, string) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "hosts.yaml")

	cfg := &config.Config{
		Version: 1,
		Settings: config.SettingsConfig{
			AutoTailscale: autoTS,
		},
		Defaults: config.DefaultsConfig{
			Timeout:      30 * time.Second,
			PollInterval: 15 * time.Second,
			ProbeTimeout: 2 * time.Second,
		},
		Hosts: []config.HostConfig{
			{
				Name: "test-host",
				MAC:  "AA:BB:CC:DD:EE:FF",
				IP:   "192.168.1.10",
			},
		},
	}
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	st, _ := store.LoadStore(filepath.Join(tmpDir, "state.json"))
	poller := presence.NewPoller(cfg, st)
	mockTS := &mockTailscaleClient{isUp: false}
	tsMgr := tailscale.NewManagerWithClient(mockTS)

	model := NewModelWithTailscale(cfg, st, poller, cfgPath, tsMgr)
	return model, mockTS, cfgPath
}

func TestSettingsPageNavigationAndToggle(t *testing.T) {
	m, _, cfgPath := setupTestModel(t, false)

	if m.mode != ModeList {
		t.Fatalf("expected initial mode ModeList, got %v", m.mode)
	}

	// Press 's' to enter settings
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if m.mode != ModeSettings {
		t.Fatalf("expected ModeSettings after pressing 's', got %v", m.mode)
	}
	if cmd == nil {
		t.Fatalf("expected cmd to fetch tailscale status on entering settings")
	}

	// Process status msg
	stMsg := cmd()
	updated, _ = m.Update(stMsg)
	m = updated.(Model)

	// Verify settings view renders
	viewOut := m.View()
	if !strings.Contains(viewOut, "SETTINGS") {
		t.Errorf("expected view to contain 'SETTINGS'")
	}
	if !strings.Contains(viewOut, "Auto-manage Tailscale") {
		t.Errorf("expected view to contain 'Auto-manage Tailscale'")
	}
	if !strings.Contains(viewOut, "Disabled") {
		t.Errorf("expected toggle to show Disabled initially")
	}

	// Press Space to toggle auto-manage to Enabled
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(Model)
	if !m.cfg.Settings.AutoTailscale {
		t.Errorf("expected AutoTailscale to be true after toggle")
	}

	// Verify view now shows Enabled
	viewOut = m.View()
	if !strings.Contains(viewOut, "Enabled") {
		t.Errorf("expected view to show Enabled after toggle")
	}

	// Verify persistence in config file
	reloadedCfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if !reloadedCfg.Settings.AutoTailscale {
		t.Errorf("expected AutoTailscale persisted as true")
	}

	// Navigate down with 'j' to action button (focus 1)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	if m.settingsFocus != 1 {
		t.Errorf("expected settingsFocus 1, got %d", m.settingsFocus)
	}

	// Navigate down with 'j' to close button (focus 2)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	if m.settingsFocus != 2 {
		t.Errorf("expected settingsFocus 2, got %d", m.settingsFocus)
	}

	// Press 's' to return to ModeList
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if m.mode != ModeList {
		t.Fatalf("expected ModeList after pressing 's' in settings, got %v", m.mode)
	}

	// Press 's' again to go to settings, then 'esc' to return to list
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if m.mode != ModeSettings {
		t.Fatalf("expected ModeSettings, got %v", m.mode)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.mode != ModeList {
		t.Fatalf("expected ModeList after esc, got %v", m.mode)
	}
}

func TestSettingsManualConnectDisconnect(t *testing.T) {
	m, mockTS, _ := setupTestModel(t, true)

	// Enter settings
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	stMsg := cmd()
	updated, _ = m.Update(stMsg)
	m = updated.(Model)

	// Focus 1: manual connect button
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.settingsFocus != 1 {
		t.Fatalf("expected settingsFocus 1, got %d", m.settingsFocus)
	}

	// Press Enter to connect
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected cmd for tailscaleUpCmd")
	}
	resMsg := cmd()
	if mockTS.upCalls != 1 {
		t.Errorf("expected 1 upCall, got %d", mockTS.upCalls)
	}

	// Dispatch action completion
	updated, batchCmd := m.Update(resMsg)
	m = updated.(Model)
	if !strings.Contains(m.settingsMsg, "connected") {
		t.Errorf("expected success msg, got: %s", m.settingsMsg)
	}
	if batchCmd == nil {
		t.Fatalf("expected batchCmd after action completed")
	}
}

func TestHelpViewIncludesSettings(t *testing.T) {
	m, _, _ := setupTestModel(t, false)

	// Press '?'
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(Model)
	if m.mode != ModeHelp {
		t.Fatalf("expected ModeHelp, got %v", m.mode)
	}

	view := m.View()
	if !strings.Contains(view, "s              Open settings page") {
		t.Errorf("expected help view to contain settings keybinding description")
	}
}
