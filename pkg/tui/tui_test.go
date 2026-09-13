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

	// Navigate down with 'j' to setting 1: Sort Online Hosts First
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	if m.settingsFocus != 1 {
		t.Errorf("expected settingsFocus 1, got %d", m.settingsFocus)
	}

	// Toggle Sort Online with Space
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(Model)
	if !m.sortOnline {
		t.Errorf("expected sortOnline to be true after toggle")
	}

	// Navigate down with 'j' to focus 2: Manual Tailscale Button
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

	// Press direct hotkey 't' to connect
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected cmd for tailscaleUpCmd via 't' key")
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

func TestFormNicknameAndHostname(t *testing.T) {
	m, _, cfgPath := setupTestModel(t, false)

	// Press 'a' to open Add Host form
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(Model)
	if m.mode != ModeForm {
		t.Fatalf("expected ModeForm after pressing 'a', got %v", m.mode)
	}

	// Check form render output contains "Nickname *"
	view := m.View()
	if !strings.Contains(view, "Nickname *") {
		t.Errorf("expected form view to contain 'Nickname *', got: %s", view)
	}
	if !strings.Contains(view, "IP / Hostname") {
		t.Errorf("expected form view to contain 'IP / Hostname', got: %s", view)
	}

	// Attempt submitting with empty name -> verify error "Nickname is required"
	m.formFocus = 9 // Save button
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.mode != ModeForm {
		t.Fatalf("expected to remain in ModeForm on validation error")
	}
	if m.formErrorMsg != "Nickname is required" {
		t.Errorf("expected 'Nickname is required', got %q", m.formErrorMsg)
	}

	// Set valid nickname, MAC, and hostname in IP/Hostname field
	m.formInputs[0].SetValue("my-pc")
	m.formInputs[1].SetValue("11:22:33:44:55:66")
	m.formInputs[2].SetValue("my-pc.tailnet.ts.net")

	// Submit form
	m.formFocus = 9 // Save button
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.mode != ModeList {
		t.Fatalf("expected ModeList after successful save, got %v (error: %s)", m.mode, m.formErrorMsg)
	}

	// Verify host was saved with hostname in config
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	savedHost, err := reloaded.FindHost("my-pc")
	if err != nil {
		t.Fatalf("FindHost my-pc failed: %v", err)
	}
	if savedHost.IP != "my-pc.tailnet.ts.net" {
		t.Errorf("expected saved hostname 'my-pc.tailnet.ts.net', got %s", savedHost.IP)
	}
}

func TestFormHAndLKeysInput(t *testing.T) {
	m, _, _ := setupTestModel(t, false)

	// Press 'a' to open Add Host form
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(Model)
	if m.mode != ModeForm {
		t.Fatalf("expected ModeForm, got %v", m.mode)
	}

	// m.formFocus is 0 (Nickname)
	// Type "hello" into Nickname
	for _, r := range "hello" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	if got := m.formInputs[0].Value(); got != "hello" {
		t.Errorf("expected Nickname field to be 'hello', got %q", got)
	}

	// Move focus to IP / Hostname (focus 2)
	m.formFocus = 2
	m.applyFormFocus()

	// Type "host.local" into Hostname
	for _, r := range "host.local" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	if got := m.formInputs[2].Value(); got != "host.local" {
		t.Errorf("expected Hostname field to be 'host.local', got %q", got)
	}

	// Test cursor navigation (left/right) in input field
	// Current cursor in Hostname is at position 10 (end)
	// Press KeyLeft twice
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	if pos := m.formInputs[2].Position(); pos != 8 {
		t.Errorf("expected cursor position 8 after 2 KeyLeft, got %d", pos)
	}

	// Test selector navigation with h/l when not on text input
	// Focus 4: Connect Type
	m.formFocus = 4
	m.applyFormFocus()
	m.formConnTypeIdx = 0
	// Press 'l' -> should increment formConnTypeIdx
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = updated.(Model)
	if m.formConnTypeIdx != 1 {
		t.Errorf("expected formConnTypeIdx == 1 after 'l', got %d", m.formConnTypeIdx)
	}
	// Press 'h' -> should decrement formConnTypeIdx
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = updated.(Model)
	if m.formConnTypeIdx != 0 {
		t.Errorf("expected formConnTypeIdx == 0 after 'h', got %d", m.formConnTypeIdx)
	}

	// Test buttons navigation with h/l
	m.formFocus = 9 // Save button
	m.applyFormFocus()
	// Press 'l' -> should move to Cancel button (10)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = updated.(Model)
	if m.formFocus != 10 {
		t.Errorf("expected formFocus == 10 after 'l' on Save button, got %d", m.formFocus)
	}
	// Press 'h' -> should move back to Save button (9)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = updated.(Model)
	if m.formFocus != 9 {
		t.Errorf("expected formFocus == 9 after 'h' on Cancel button, got %d", m.formFocus)
	}
}
