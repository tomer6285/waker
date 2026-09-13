package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tomer/waker/pkg/actions"
	"github.com/tomer/waker/pkg/config"
	"github.com/tomer/waker/pkg/presence"
	"github.com/tomer/waker/pkg/sleeper"
	"github.com/tomer/waker/pkg/store"
	"github.com/tomer/waker/pkg/tailscale"
	"github.com/tomer/waker/pkg/wol"
)

type ViewMode int

const (
	ModeList ViewMode = iota
	ModeDetail
	ModeForm
	ModeHelp
	ModeLogs
	ModeSettings
)

type StatusUpdateMsg map[string]*presence.HostStatusInfo
type LogMsg string
type PeriodicTickMsg time.Time
type WakeCompletedMsg struct {
	HostName string
	Err      error
}
type SleepCompletedMsg struct {
	HostName string
	Err      error
}
type TailscaleStatusMsg *tailscale.Status
type TailscaleActionCompletedMsg struct {
	Action string
	Err    error
}

var (
	// Styling
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E5E9F0")).
			Background(lipgloss.Color("#2E3440")).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7B88A1")).
			MarginBottom(1)

	selectedItemStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#88C0D0"))

	dotOnline      = lipgloss.NewStyle().Foreground(lipgloss.Color("#A3BE8C")).Render("●")
	dotOffline     = lipgloss.NewStyle().Foreground(lipgloss.Color("#4C566A")).Render("○")
	dotUnreachable = lipgloss.NewStyle().Foreground(lipgloss.Color("#D08770")).Render("⊗")
	dotWaking      = lipgloss.NewStyle().Foreground(lipgloss.Color("#EBCB8B")).Render("◌")
	dotUnknown     = lipgloss.NewStyle().Foreground(lipgloss.Color("#616E88")).Render("?")

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#3B4252")).
			Padding(0, 1)

	// Form Styling
	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#ECEFF4")).
			Background(lipgloss.Color("#434C5E")).
			Padding(0, 1)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7B88A1")).
				Background(lipgloss.Color("#2E3440")).
				Padding(0, 1)

	formLabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D8DEE9")).
			Width(22)

	formFocusLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#88C0D0")).
				Width(22)

	hintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#616E88"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#BF616A")).
			Bold(true)

	btnStyle = lipgloss.NewStyle().
			Padding(0, 2)

	btnActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#ECEFF4")).
			Background(lipgloss.Color("#434C5E")).
			Padding(0, 2)
)

type Model struct {
	cfg        *config.Config
	store      *store.Store
	poller     *presence.Poller
	sleeper    *sleeper.Sleeper
	tsMgr      *tailscale.Manager
	tsStatus   *tailscale.Status
	configPath string

	mode        ViewMode
	cursor      int
	filterText  string
	filtering   bool
	filterInput textinput.Model
	sortOnline  bool

	spinner spinner.Model
	logs    []string
	status  string

	// Settings focus: 0: Auto-manage Tailscale, 1: Sort Online First, 2: Manual Tailscale Button
	settingsFocus int
	settingsMsg   string

	// Form editing
	formInputs      []textinput.Model
	formFocus       int
	isEditingHost   bool
	editingIndex    int
	formConnTypeIdx int // 0: SSH, 1: Parsec, 2: Mount, 3: Game, 4: Custom
	formSleepIdx    int // 0: SSH, 1: Agent, 2: Custom
	formErrorMsg    string

	// Confirmation for sleep
	confirmSleepHost string

	width  int
	height int
}

func NewModel(cfg *config.Config, st *store.Store, poller *presence.Poller, configPath string) Model {
	return NewModelWithTailscale(cfg, st, poller, configPath, tailscale.NewManager())
}

func NewModelWithTailscale(cfg *config.Config, st *store.Store, poller *presence.Poller, configPath string, tsMgr *tailscale.Manager) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0AF68"))

	fi := textinput.New()
	fi.Placeholder = "type to filter..."
	fi.Prompt = "/ "

	if tsMgr == nil {
		tsMgr = tailscale.NewManager()
	}

	m := Model{
		cfg:         cfg,
		store:       st,
		poller:      poller,
		sleeper:     sleeper.NewSleeper(),
		tsMgr:       tsMgr,
		configPath:  configPath,
		mode:        ModeList,
		spinner:     s,
		filterInput: fi,
		logs:        make([]string, 0),
		width:       80,
		height:      24,
	}

	m.initFormInputs(nil)
	return m
}

func (m *Model) initFormInputs(host *config.HostConfig) {
	m.formErrorMsg = ""
	m.formFocus = 0

	m.formInputs = make([]textinput.Model, 7)

	placeholders := []string{
		"desktop",
		"auto-detect or AA:BB:CC:DD:EE:FF",
		"192.168.1.50 or hostname",
		"255.255.255.255",
		"",
		"",
		"",
	}

	for i := range m.formInputs {
		ti := textinput.New()
		ti.Placeholder = placeholders[i]
		m.formInputs[i] = ti
	}
	m.formInputs[0].Focus()

	m.formConnTypeIdx = 0 // default SSH
	m.formSleepIdx = 0    // default SSH

	if host != nil {
		m.formInputs[0].SetValue(host.Name)
		m.formInputs[1].SetValue(host.MAC)
		m.formInputs[2].SetValue(host.IP)
		m.formInputs[3].SetValue(host.Broadcast)

		if host.OnConnect != nil {
			switch strings.ToLower(host.OnConnect.Type) {
			case "parsec":
				m.formConnTypeIdx = 1
				m.formInputs[4].SetValue(host.OnConnect.PeerID)
				m.formInputs[5].SetValue(host.OnConnect.Settings)
			case "mount":
				m.formConnTypeIdx = 2
				url := host.OnConnect.URL
				if url == "" {
					url = host.OnConnect.Run
				}
				m.formInputs[4].SetValue(url)
			case "game":
				m.formConnTypeIdx = 3
				url := host.OnConnect.URL
				if url == "" {
					url = host.OnConnect.Run
				}
				m.formInputs[4].SetValue(url)
			case "custom":
				m.formConnTypeIdx = 4
				m.formInputs[4].SetValue(host.OnConnect.Run)
			default: // ssh
				m.formConnTypeIdx = 0
				if host.SSH != nil {
					m.formInputs[4].SetValue(host.SSH.User)
					if host.SSH.Port != 0 {
						m.formInputs[5].SetValue(fmt.Sprintf("%d", host.SSH.Port))
					}
				}
			}
		} else if host.SSH != nil {
			m.formConnTypeIdx = 0
			m.formInputs[4].SetValue(host.SSH.User)
			if host.SSH.Port != 0 {
				m.formInputs[5].SetValue(fmt.Sprintf("%d", host.SSH.Port))
			}
		}

		if host.OnSleep != nil {
			switch strings.ToLower(host.OnSleep.Type) {
			case "agent":
				m.formSleepIdx = 1
				port := host.OnSleep.Port
				if port == 0 {
					port = 9876
				}
				m.formInputs[6].SetValue(fmt.Sprintf("%d", port))
			case "custom":
				m.formSleepIdx = 2
				m.formInputs[6].SetValue(host.OnSleep.Run)
			default: // ssh
				m.formSleepIdx = 0
			}
		} else {
			m.formSleepIdx = 0
		}
	}

	m.updateDynamicPlaceholders()
}

func (m *Model) updateDynamicPlaceholders() {
	switch m.formConnTypeIdx {
	case 0: // SSH
		m.formInputs[4].Placeholder = "tomer (or empty)"
		m.formInputs[5].Placeholder = "22"
	case 1: // Parsec
		m.formInputs[4].Placeholder = "1a2b3c4d..."
		m.formInputs[5].Placeholder = "client_vsync=1"
	case 2: // Mount
		m.formInputs[4].Placeholder = "smb://nas/share"
		m.formInputs[5].Placeholder = ""
	case 3: // Game
		m.formInputs[4].Placeholder = "steam://connect/192.168.1.30:2456"
		m.formInputs[5].Placeholder = ""
	case 4: // Custom
		m.formInputs[4].Placeholder = "./scripts/sync.sh"
		m.formInputs[5].Placeholder = ""
	}

	switch m.formSleepIdx {
	case 0: // SSH
		m.formInputs[6].Placeholder = "automatic"
	case 1: // Agent
		m.formInputs[6].Placeholder = "9876"
	case 2: // Custom
		m.formInputs[6].Placeholder = "curl -X POST ..."
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.pollTickCmd(),
		m.checkTailscaleStatusCmd(),
	)
}

func (m Model) pollTickCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return PeriodicTickMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case PeriodicTickMsg:
		return m, tea.Batch(
			func() tea.Msg {
				return StatusUpdateMsg(m.poller.PollOnce(context.Background()))
			},
			m.pollTickCmd(),
		)

	case StatusUpdateMsg:
		return m, nil

	case LogMsg:
		m.addLog(string(msg))
		return m, nil

	case WakeCompletedMsg:
		if msg.Err != nil {
			m.addLog(fmt.Sprintf("Wake error for %s: %v", msg.HostName, msg.Err))
		} else {
			m.addLog(fmt.Sprintf("WOL packet successfully sent to %s ✓", msg.HostName))
		}
		return m, nil

	case SleepCompletedMsg:
		if msg.Err != nil {
			m.addLog(fmt.Sprintf("Sleep error for %s: %v", msg.HostName, msg.Err))
		} else {
			m.addLog(fmt.Sprintf("Host %s suspend signal sent ✓", msg.HostName))
		}
		return m, nil

	case TailscaleStatusMsg:
		m.tsStatus = (*tailscale.Status)(msg)
		return m, nil

	case TailscaleActionCompletedMsg:
		if msg.Err != nil {
			m.settingsMsg = fmt.Sprintf("Error: %v", msg.Err)
			m.addLog(fmt.Sprintf("Tailscale %s error: %v", msg.Action, msg.Err))
		} else {
			if msg.Action == "up" {
				m.settingsMsg = "Tailscale connected successfully ✓"
				m.addLog("Tailscale connected ✓")
			} else {
				m.settingsMsg = "Tailscale disconnected successfully ✓"
				m.addLog("Tailscale disconnected ✓")
			}
		}
		return m, tea.Batch(
			m.checkTailscaleStatusCmd(),
			func() tea.Msg {
				return StatusUpdateMsg(m.poller.PollOnce(context.Background()))
			},
		)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		// Handle Sleep confirmation modal
		if m.confirmSleepHost != "" {
			switch strings.ToLower(msg.String()) {
			case "y", "enter":
				hName := m.confirmSleepHost
				m.confirmSleepHost = ""
				return m, m.sleepHostCmd(hName)
			case "n", "esc", "q":
				m.confirmSleepHost = ""
				m.addLog("Sleep canceled.")
				return m, nil
			default:
				return m, nil
			}
		}

		// Handle Settings input
		if m.mode == ModeSettings {
			return m.handleSettingsKeys(msg)
		}

		// Handle Form input
		if m.mode == ModeForm {
			return m.handleFormKeys(msg)
		}

		// Handle Filter input
		if m.filtering {
			switch msg.String() {
			case "esc", "enter":
				m.filtering = false
				m.filterInput.Blur()
				return m, nil
			default:
				var cmd tea.Cmd
				m.filterInput, cmd = m.filterInput.Update(msg)
				m.filterText = m.filterInput.Value()
				return m, cmd
			}
		}

		// Global navigation keys
		switch msg.String() {
		case "ctrl+c", "q":
			if m.mode != ModeList {
				m.mode = ModeList
				return m, nil
			}
			return m, tea.Quit

		case "?":
			if m.mode == ModeHelp {
				m.mode = ModeList
			} else {
				m.mode = ModeHelp
			}
			return m, nil

		case "s":
			m.mode = ModeSettings
			m.settingsFocus = 0
			m.settingsMsg = ""
			return m, m.checkTailscaleStatusCmd()

		case "l":
			if m.mode == ModeLogs {
				m.mode = ModeList
			} else {
				m.mode = ModeLogs
			}
			return m, nil

		case "/":
			m.filtering = true
			m.filterInput.Focus()
			return m, nil

		case "o":
			m.sortOnline = !m.sortOnline
			return m, nil

		case "r":
			m.addLog("Refreshing presence status...")
			return m, func() tea.Msg {
				return StatusUpdateMsg(m.poller.PollOnce(context.Background()))
			}

		case "j", "down":
			hosts := m.filteredHosts()
			if m.cursor < len(hosts)-1 {
				m.cursor++
			}
			return m, nil

		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case "w": // Wake
			host := m.selectedHost()
			if host != nil {
				return m, m.wakeHostCmd(host)
			}

		case "enter": // Wake or Connect if already online
			host := m.selectedHost()
			if host != nil {
				stat := m.poller.Get(host.Name)
				if stat != nil && stat.Status == presence.StatusOnline {
					return m, m.connectHostCmd(host)
				}
				return m, m.wakeHostCmd(host)
			}

		case "c": // Connect
			host := m.selectedHost()
			if host != nil {
				return m, m.wakeAndConnectHostCmd(host)
			}

		case "z": // Sleep
			host := m.selectedHost()
			if host != nil {
				m.confirmSleepHost = host.Name
				return m, nil
			}

		case "p": // Manual ping
			host := m.selectedHost()
			if host != nil {
				m.addLog(fmt.Sprintf("Probing %s (%s)...", host.Name, host.IP))
				return m, func() tea.Msg {
					res := m.poller.ProbeOne(context.Background(), host)
					return LogMsg(fmt.Sprintf("Probe %s: %s (latency %v)", res.Name, res.Status, res.Latency))
				}
			}

		case "a": // Add host
			m.isEditingHost = false
			m.initFormInputs(nil)
			m.mode = ModeForm
			return m, nil

		case "e": // Edit host
			host := m.selectedHost()
			if host != nil {
				m.isEditingHost = true
				m.editingIndex = m.cursor
				m.initFormInputs(host)
				m.mode = ModeForm
			}
			return m, nil

		case "d": // Delete host
			host := m.selectedHost()
			if host != nil {
				m.cfg.DeleteHost(host.Name)
				_ = m.cfg.Save(m.configPath)
				m.addLog(fmt.Sprintf("Deleted host %s", host.Name))
				if m.cursor >= len(m.cfg.Hosts) && m.cursor > 0 {
					m.cursor--
				}
			}
			return m, nil
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleSettingsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	totalFocusable := 3

	switch msg.String() {
	case "s", "esc":
		m.mode = ModeList
		m.settingsMsg = ""
		return *m, nil

	case "tab", "down", "j":
		m.settingsFocus = (m.settingsFocus + 1) % totalFocusable
		return *m, nil

	case "shift+tab", "up", "k":
		m.settingsFocus = (m.settingsFocus - 1 + totalFocusable) % totalFocusable
		return *m, nil

	case "t":
		// Quick direct hotkey to toggle Tailscale
		if m.tsStatus != nil && m.tsStatus.IsUp {
			m.settingsMsg = "Disconnecting Tailscale..."
			return *m, m.tailscaleDownCmd()
		}
		m.settingsMsg = "Connecting Tailscale..."
		return *m, m.tailscaleUpCmd()

	case " ":
		switch m.settingsFocus {
		case 0:
			return m.toggleAutoTailscale()
		case 1:
			m.sortOnline = !m.sortOnline
			if m.sortOnline {
				m.settingsMsg = "Sort online-first ENABLED"
			} else {
				m.settingsMsg = "Sort online-first DISABLED"
			}
			return *m, nil
		case 2:
			if m.tsStatus != nil && m.tsStatus.IsUp {
				m.settingsMsg = "Disconnecting Tailscale..."
				return *m, m.tailscaleDownCmd()
			}
			m.settingsMsg = "Connecting Tailscale..."
			return *m, m.tailscaleUpCmd()
		}

	case "enter":
		switch m.settingsFocus {
		case 0:
			return m.toggleAutoTailscale()
		case 1:
			m.sortOnline = !m.sortOnline
			if m.sortOnline {
				m.settingsMsg = "Sort online-first ENABLED"
			} else {
				m.settingsMsg = "Sort online-first DISABLED"
			}
			return *m, nil
		case 2:
			// Toggle Tailscale Up/Down manually
			if m.tsStatus != nil && m.tsStatus.IsUp {
				m.settingsMsg = "Disconnecting Tailscale..."
				return *m, m.tailscaleDownCmd()
			}
			m.settingsMsg = "Connecting Tailscale..."
			return *m, m.tailscaleUpCmd()
		}

	case "ctrl+c", "q":
		return *m, tea.Quit
	}
	return *m, nil
}

func (m *Model) toggleAutoTailscale() (tea.Model, tea.Cmd) {
	m.cfg.Settings.AutoTailscale = !m.cfg.Settings.AutoTailscale
	err := m.cfg.Save(m.configPath)
	var cmd tea.Cmd
	if err != nil {
		m.settingsMsg = fmt.Sprintf("Save failed: %v", err)
	} else {
		if m.cfg.Settings.AutoTailscale {
			m.settingsMsg = "Tailscale auto-management ENABLED (saved to hosts.yaml)"
			m.addLog("Settings: Tailscale auto-management enabled")
			if m.tsStatus == nil || !m.tsStatus.IsUp {
				m.settingsMsg = "Tailscale auto-management ENABLED — connecting..."
				cmd = m.tailscaleUpCmd()
			}
		} else {
			m.settingsMsg = "Tailscale auto-management DISABLED (saved to hosts.yaml)"
			m.addLog("Settings: Tailscale auto-management disabled")
		}
	}
	return *m, cmd
}

func (m *Model) checkTailscaleStatusCmd() tea.Cmd {
	return func() tea.Msg {
		if m.tsMgr == nil {
			return TailscaleStatusMsg(nil)
		}
		st, _ := m.tsMgr.Status(context.Background())
		return TailscaleStatusMsg(st)
	}
}

func (m *Model) tailscaleUpCmd() tea.Cmd {
	return func() tea.Msg {
		if m.tsMgr == nil {
			return TailscaleActionCompletedMsg{Action: "up", Err: errors.New("tailscale manager not available")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := m.tsMgr.Up(ctx)
		if err == nil {
			m.tsMgr.SetStartedByWaker(true)
		}
		return TailscaleActionCompletedMsg{Action: "up", Err: err}
	}
}

func (m *Model) tailscaleDownCmd() tea.Cmd {
	return func() tea.Msg {
		if m.tsMgr == nil {
			return TailscaleActionCompletedMsg{Action: "down", Err: errors.New("tailscale manager not available")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := m.tsMgr.Down(ctx)
		if err == nil {
			m.tsMgr.SetStartedByWaker(false)
		}
		return TailscaleActionCompletedMsg{Action: "down", Err: err}
	}
}

func (m *Model) handleFormKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	connTypes := []string{"SSH", "Parsec", "Mount (SMB/NFS)", "Game (Steam)", "Custom"}
	sleepTypes := []string{"SSH (Standard)", "Agent (HTTP)", "Custom"}

	totalFocusable := 11

	switch msg.String() {
	case "esc":
		m.mode = ModeList
		m.formErrorMsg = ""
		return *m, nil

	case "tab", "down":
		m.formErrorMsg = ""
		m.formFocus = (m.formFocus + 1) % totalFocusable
		m.applyFormFocus()
		return *m, nil

	case "shift+tab", "up":
		m.formErrorMsg = ""
		m.formFocus = (m.formFocus - 1 + totalFocusable) % totalFocusable
		m.applyFormFocus()
		return *m, nil

	case "enter":
		if m.formFocus == 10 {
			m.mode = ModeList
			m.formErrorMsg = ""
			return *m, nil
		}

		if m.formFocus == 9 {
			return m.submitForm()
		}

		m.formFocus = (m.formFocus + 1) % totalFocusable
		m.applyFormFocus()
		return *m, nil
	}

	// For selector and button controls (non-input fields), support left/right and h/l navigation
	if m.currentInputIndex() == -1 {
		switch msg.String() {
		case "left", "h":
			if m.formFocus == 4 { // Connect Type
				if m.formConnTypeIdx > 0 {
					m.formConnTypeIdx--
					m.updateDynamicPlaceholders()
				}
				return *m, nil
			}
			if m.formFocus == 7 { // Sleep Type
				if m.formSleepIdx > 0 {
					m.formSleepIdx--
					m.updateDynamicPlaceholders()
				}
				return *m, nil
			}
			if m.formFocus == 10 { // Cancel button -> Save button
				m.formFocus = 9
				return *m, nil
			}

		case "right", "l":
			if m.formFocus == 4 { // Connect Type
				if m.formConnTypeIdx < len(connTypes)-1 {
					m.formConnTypeIdx++
					m.updateDynamicPlaceholders()
				}
				return *m, nil
			}
			if m.formFocus == 7 { // Sleep Type
				if m.formSleepIdx < len(sleepTypes)-1 {
					m.formSleepIdx++
					m.updateDynamicPlaceholders()
				}
				return *m, nil
			}
			if m.formFocus == 9 { // Save button -> Cancel button
				m.formFocus = 10
				return *m, nil
			}
		}
		return *m, nil
	}

	// For text inputs, delegate all remaining keys (including h, l, and arrow keys) to the focused input
	inputIdx := m.currentInputIndex()
	if inputIdx >= 0 && inputIdx < len(m.formInputs) {
		var cmd tea.Cmd
		m.formInputs[inputIdx], cmd = m.formInputs[inputIdx].Update(msg)
		return *m, cmd
	}

	return *m, nil
}
func (m *Model) currentInputIndex() int {
	switch m.formFocus {
	case 0:
		return 0 // Name
	case 1:
		return 1 // MAC
	case 2:
		return 2 // IP
	case 3:
		return 3 // Broadcast
	case 5:
		return 4 // Connect Primary
	case 6:
		return 5 // Connect Secondary
	case 8:
		return 6 // Sleep Target
	default:
		return -1
	}
}

func (m *Model) applyFormFocus() {
	activeIdx := m.currentInputIndex()
	for i := range m.formInputs {
		if i == activeIdx {
			m.formInputs[i].Focus()
		} else {
			m.formInputs[i].Blur()
		}
	}
}

func (m *Model) submitForm() (tea.Model, tea.Cmd) {
	h, err := m.parseFormHost()
	if err != nil {
		m.formErrorMsg = err.Error()
		return *m, nil
	}

	if err := h.Validate(); err != nil {
		m.formErrorMsg = fmt.Sprintf("Validation: %v", err)
		return *m, nil
	}

	if err := m.cfg.UpsertHost(h); err != nil {
		m.formErrorMsg = fmt.Sprintf("Config error: %v", err)
		return *m, nil
	}

	if err := m.cfg.Save(m.configPath); err != nil {
		m.formErrorMsg = fmt.Sprintf("Save failed: %v", err)
		return *m, nil
	}

	m.mode = ModeList
	m.formErrorMsg = ""
	m.addLog(fmt.Sprintf("Saved host '%s' ✓", h.Name))
	return *m, nil
}

func (m *Model) parseFormHost() (config.HostConfig, error) {
	name := strings.TrimSpace(m.formInputs[0].Value())
	mac := strings.TrimSpace(m.formInputs[1].Value())
	ip := strings.TrimSpace(m.formInputs[2].Value())
	bcast := strings.TrimSpace(m.formInputs[3].Value())
	connPrimary := strings.TrimSpace(m.formInputs[4].Value())
	connSecondary := strings.TrimSpace(m.formInputs[5].Value())
	sleepTarget := strings.TrimSpace(m.formInputs[6].Value())

	if name == "" {
		return config.HostConfig{}, fmt.Errorf("Nickname is required")
	}
	if mac == "" {
		if ip == "" {
			return config.HostConfig{}, fmt.Errorf("MAC Address is required (or provide an IP to auto-detect)")
		}
		detectedMAC, err := wol.ResolveMACFromIP(ip)
		if err != nil {
			return config.HostConfig{}, fmt.Errorf("could not auto-detect MAC from %s: %w", ip, err)
		}
		mac = detectedMAC
		m.formInputs[1].SetValue(detectedMAC)
	}

	h := config.HostConfig{
		Name:      name,
		MAC:       mac,
		IP:        ip,
		Broadcast: bcast,
	}

	connTypes := []string{"ssh", "parsec", "mount", "game", "custom"}
	connType := connTypes[m.formConnTypeIdx]
	h.OnConnect = &config.ConnectAction{Type: connType}

	switch connType {
	case "ssh":
		port := 22
		if connSecondary != "" {
			fmt.Sscanf(connSecondary, "%d", &port)
		}
		h.SSH = &config.SSHConfig{
			User: connPrimary,
			Port: port,
		}
	case "parsec":
		if connPrimary == "" {
			return config.HostConfig{}, fmt.Errorf("Parsec requires a Peer ID")
		}
		h.OnConnect.PeerID = connPrimary
		h.OnConnect.Settings = connSecondary
	case "mount", "game":
		if connPrimary == "" {
			return config.HostConfig{}, fmt.Errorf("%s requires a URL", strings.ToUpper(connType))
		}
		h.OnConnect.URL = connPrimary
	case "custom":
		if connPrimary == "" {
			return config.HostConfig{}, fmt.Errorf("Custom connect requires a command to run")
		}
		h.OnConnect.Run = connPrimary
	}

	sleepTypes := []string{"ssh", "agent", "custom"}
	sleepType := sleepTypes[m.formSleepIdx]
	h.OnSleep = &config.SleepAction{Type: sleepType}

	switch sleepType {
	case "ssh":
		// Standard SSH suspend
	case "agent":
		port := 9876
		if sleepTarget != "" {
			fmt.Sscanf(sleepTarget, "%d", &port)
		}
		h.OnSleep.Port = port
	case "custom":
		if sleepTarget == "" {
			return config.HostConfig{}, fmt.Errorf("Custom sleep requires a command to run")
		}
		h.OnSleep.Run = sleepTarget
	}

	return h, nil
}

func (m *Model) wakeHostCmd(host *config.HostConfig) tea.Cmd {
	return func() tea.Msg {
		m.poller.MarkWaking(host.Name)
		opts := wol.Options{
			BroadcastIP: host.Broadcast,
			Port:        host.Port,
			IfaceName:   host.Interface,
		}
		relay := host.Relay
		if relay == nil {
			relay = m.cfg.Defaults.Relay
		}
		err := wol.SendWithRelay(context.Background(), relay, host.MAC, opts)
		return WakeCompletedMsg{HostName: host.Name, Err: err}
	}
}

func (m *Model) sleepHostCmd(hostName string) tea.Cmd {
	return func() tea.Msg {
		thost, err := m.cfg.FindHost(hostName)
		if err != nil {
			return SleepCompletedMsg{HostName: hostName, Err: err}
		}
		err = m.sleeper.Sleep(context.Background(), thost)
		return SleepCompletedMsg{HostName: hostName, Err: err}
	}
}

func (m *Model) connectHostCmd(host *config.HostConfig) tea.Cmd {
	return func() tea.Msg {
		r := actions.NewRunner(nil, nil, nil)
		err := r.Connect(context.Background(), host, nil, false)
		if err != nil {
			return LogMsg(fmt.Sprintf("Connect failed for %s: %v", host.Name, err))
		}
		return LogMsg(fmt.Sprintf("Launched connect action for %s ✓", host.Name))
	}
}

func (m *Model) wakeAndConnectHostCmd(host *config.HostConfig) tea.Cmd {
	return func() tea.Msg {
		m.poller.MarkWaking(host.Name)
		relay := host.Relay
		if relay == nil {
			relay = m.cfg.Defaults.Relay
		}
		_ = wol.SendWithRelay(context.Background(), relay, host.MAC, wol.Options{
			BroadcastIP: host.Broadcast,
			Port:        host.Port,
			IfaceName:   host.Interface,
		})

		ctx, cancel := context.WithTimeout(context.Background(), m.cfg.Defaults.Timeout)
		defer cancel()

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return LogMsg(fmt.Sprintf("Timed out waiting for %s to wake", host.Name))
			case <-ticker.C:
				res := m.poller.ProbeOne(ctx, host)
				if res.Status == presence.StatusOnline {
					r := actions.NewRunner(nil, nil, nil)
					err := r.Connect(context.Background(), host, nil, false)
					if err != nil {
						return LogMsg(fmt.Sprintf("Connected error for %s: %v", host.Name, err))
					}
					return LogMsg(fmt.Sprintf("Host %s woke and connected successfully! ✓", host.Name))
				}
			}
		}
	}
}

func (m *Model) addLog(entry string) {
	timestamp := time.Now().Format("15:04:05")
	m.logs = append(m.logs, fmt.Sprintf("[%s] %s", timestamp, entry))
	if len(m.logs) > 50 {
		m.logs = m.logs[len(m.logs)-50:]
	}
}

// AddLog appends a message to the activity log.
func (m *Model) AddLog(entry string) {
	m.addLog(entry)
}

func (m *Model) filteredHosts() []config.HostConfig {
	var list []config.HostConfig
	query := strings.ToLower(strings.TrimSpace(m.filterText))

	for _, h := range m.cfg.Hosts {
		if query != "" && !strings.Contains(strings.ToLower(h.Name), query) && !strings.Contains(h.IP, query) {
			continue
		}
		list = append(list, h)
	}

	if m.sortOnline {
		var online, others []config.HostConfig
		for _, h := range list {
			st := m.poller.Get(h.Name)
			if st != nil && st.Status == presence.StatusOnline {
				online = append(online, h)
			} else {
				others = append(others, h)
			}
		}
		return append(online, others...)
	}

	return list
}

func (m *Model) selectedHost() *config.HostConfig {
	hosts := m.filteredHosts()
	if len(hosts) == 0 || m.cursor < 0 || m.cursor >= len(hosts) {
		return nil
	}
	return &hosts[m.cursor]
}

func (m Model) View() string {
	if m.width == 0 {
		m.width = 80
	}

	var sb strings.Builder

	// Title Bar
	title := titleStyle.Render("WAKER")
	var headerRight string
	onlineCount := 0
	for _, h := range m.cfg.Hosts {
		st := m.poller.Get(h.Name)
		if st != nil && st.Status == presence.StatusOnline {
			onlineCount++
		}
	}
	headerRight = fmt.Sprintf("[%d/%d online] [s settings | ? help | q quit]", onlineCount, len(m.cfg.Hosts))
	sb.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, title, " ", headerStyle.Render(headerRight)))
	sb.WriteString("\n\n")

	if m.confirmSleepHost != "" {
		modal := panelStyle.Width(m.width - 4).Render(fmt.Sprintf(
			"Suspend host '%s'?\n\n[y] Yes / Suspend    [n/esc] Cancel",
			m.confirmSleepHost,
		))
		sb.WriteString(modal)
		return sb.String()
	}

	switch m.mode {
	case ModeHelp:
		return m.viewHelp()
	case ModeLogs:
		return m.viewLogs()
	case ModeForm:
		return m.viewForm()
	case ModeSettings:
		return m.viewSettings()
	default:
		return m.viewListAndDetail()
	}
}

func (m Model) viewListAndDetail() string {
	var sb strings.Builder
	hosts := m.filteredHosts()

	// Filter bar if active or non-empty
	if m.filtering || m.filterText != "" {
		sb.WriteString(m.filterInput.View() + "\n")
	}

	// Host list table
	listLines := make([]string, 0)
	if len(hosts) == 0 {
		listLines = append(listLines, "  (No hosts found. Press 'a' to add a host)")
	} else {
		for i, h := range hosts {
			isSelected := (i == m.cursor)
			cursorStr := "  "
			if isSelected {
				cursorStr = "> "
			}

			st := m.poller.Get(h.Name)
			dot := dotUnknown
			statusStr := "unknown"
			latencyStr := ""

			if st != nil {
				switch st.Status {
				case presence.StatusOnline:
					dot = dotOnline
					statusStr = "online"
					if st.Latency > 0 {
						latencyStr = fmt.Sprintf("%dms", st.Latency.Milliseconds())
					}
				case presence.StatusOffline:
					dot = dotOffline
					statusStr = "offline"
				case presence.StatusUnreachable:
					dot = dotUnreachable
					statusStr = "unreachable"
				case presence.StatusWaking:
					dot = m.spinner.View()
					statusStr = "waking…"
				}
			}

			line := fmt.Sprintf("%s%s %-16s %-16s %-12s %s",
				cursorStr, dot, h.Name, h.IP, statusStr, latencyStr)

			if isSelected {
				listLines = append(listLines, selectedItemStyle.Render(line))
			} else {
				listLines = append(listLines, line)
			}
		}
	}

	listPanel := panelStyle.Width(m.width - 4).Render(strings.Join(listLines, "\n"))
	sb.WriteString(listPanel)
	sb.WriteString("\n")

	// Detail & Logs Pane
	host := m.selectedHost()
	var detailLines []string
	if host != nil {
		st := m.poller.Get(host.Name)
		connInfo := "ssh"
		if host.OnConnect != nil {
			connInfo = host.OnConnect.Type
			if host.OnConnect.PeerID != "" {
				connInfo += fmt.Sprintf(" (peer: %s)", host.OnConnect.PeerID)
			}
		}

		lastSeenStr := "never"
		if st != nil && !st.LastSeen.IsZero() {
			lastSeenStr = time.Since(st.LastSeen).Truncate(time.Second).String() + " ago"
		}

		detailLines = append(detailLines, fmt.Sprintf("Detail: %s • MAC: %s • Connect: %s • Last Seen: %s",
			host.Name, host.MAC, connInfo, lastSeenStr))
	} else {
		detailLines = append(detailLines, "No host selected")
	}

	// Last 3 log lines
	if len(m.logs) > 0 {
		start := len(m.logs) - 3
		if start < 0 {
			start = 0
		}
		detailLines = append(detailLines, "--- Recent Activity ---")
		for _, l := range m.logs[start:] {
			detailLines = append(detailLines, l)
		}
	}

	detailPanel := panelStyle.Width(m.width - 4).Render(strings.Join(detailLines, "\n"))
	sb.WriteString(detailPanel)
	sb.WriteString("\n")

	// Footer hotkeys
	sb.WriteString(headerStyle.Render("[Enter] Wake/Connect  [c] Connect  [w] Wake  [z] Sleep  [s] Settings  [a] Add  [e] Edit  [d] Delete  [r] Refresh"))

	return sb.String()
}

func renderToggleSwitch(enabled bool, isFocused bool) string {
	onStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#2E3440")).
		Background(lipgloss.Color("#A3BE8C")).
		Padding(0, 1)

	offStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#ECEFF4")).
		Background(lipgloss.Color("#4C566A")).
		Padding(0, 1)

	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#616E88")).
		Background(lipgloss.Color("#2E3440")).
		Padding(0, 1)

	if enabled {
		return onStyle.Render("● ON (Enabled)") + " " + dimStyle.Render("OFF")
	}
	return dimStyle.Render("ON") + " " + offStyle.Render("○ OFF (Disabled)")
}

func (m Model) viewSettings() string {
	var sb strings.Builder
	innerWidth := m.width - 8
	if innerWidth < 64 {
		innerWidth = 64
	}

	// 1. Header Banner
	headerTitle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#88C0D0")).
		Render("SETTINGS & PREFERENCES")

	sb.WriteString(headerTitle + "\n")
	sb.WriteString(hintStyle.Render("Press [Space] or [Enter] to toggle setting  •  [s] or [Esc] to return to host list") + "\n\n")

	// Notification message if an action occurred
	if m.settingsMsg != "" {
		msgStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#A3BE8C")).
			Background(lipgloss.Color("#2E3440")).
			Padding(0, 1)
		if strings.Contains(strings.ToLower(m.settingsMsg), "error") || strings.Contains(strings.ToLower(m.settingsMsg), "failed") {
			msgStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#BF616A")).
				Background(lipgloss.Color("#2E3440")).
				Padding(0, 1)
		}
		sb.WriteString(msgStyle.Render(m.settingsMsg) + "\n\n")
	}

	// 2. SYSTEM PREFERENCES SECTION
	sectionTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#81A1C1")).Render("AUTOMATION & DISPLAY")
	sb.WriteString(sectionTitle + "\n\n")

	// Row 0: Auto-manage Tailscale
	cur0 := "  "
	name0Style := lipgloss.NewStyle().Foreground(lipgloss.Color("#ECEFF4"))
	if m.settingsFocus == 0 {
		cur0 = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#88C0D0")).Render("▶ ")
		name0Style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#88C0D0"))
	}
	sw0 := renderToggleSwitch(m.cfg.Settings.AutoTailscale, m.settingsFocus == 0)
	sb.WriteString(fmt.Sprintf("%s%-32s %s\n", cur0, name0Style.Render("Auto-manage Tailscale"), sw0))
	sb.WriteString(fmt.Sprintf("    %s\n\n", hintStyle.Render("Start Tailscale before scanning on launch; shut it down on quit (if started by Waker)")))

	// Row 1: Sort Online Hosts First
	cur1 := "  "
	name1Style := lipgloss.NewStyle().Foreground(lipgloss.Color("#ECEFF4"))
	if m.settingsFocus == 1 {
		cur1 = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#88C0D0")).Render("▶ ")
		name1Style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#88C0D0"))
	}
	sw1 := renderToggleSwitch(m.sortOnline, m.settingsFocus == 1)
	sb.WriteString(fmt.Sprintf("%s%-32s %s\n", cur1, name1Style.Render("Sort Online Hosts First"), sw1))
	sb.WriteString(fmt.Sprintf("    %s\n\n", hintStyle.Render("Pin online and reachable machines to the top of the main host list")))

	// 3. TAILSCALE LIVE STATUS & ACTION CARD
	statusTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#81A1C1")).Render("TAILSCALE NETWORK STATUS")
	sb.WriteString(statusTitle + "\n\n")

	tsStatusLine := "Checking..."
	tsIPLine := "-"
	if m.tsStatus != nil {
		if !m.tsStatus.Installed {
			tsStatusLine = lipgloss.NewStyle().Foreground(lipgloss.Color("#BF616A")).Render("Tailscale CLI not found in PATH")
		} else if m.tsStatus.IsUp {
			ipStr := ""
			if m.tsStatus.IP != "" {
				ipStr = fmt.Sprintf(" (%s)", m.tsStatus.IP)
				tsIPLine = m.tsStatus.IP
			}
			tsStatusLine = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#A3BE8C")).Render("● Connected" + ipStr)
		} else {
			state := m.tsStatus.BackendState
			if state == "" {
				state = "Stopped / Offline"
			}
			tsStatusLine = lipgloss.NewStyle().Foreground(lipgloss.Color("#EBCB8B")).Render("○ " + state)
		}
	}

	var statusCard strings.Builder
	lblStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7B88A1"))
	statusCard.WriteString(fmt.Sprintf("%-16s %s\n", lblStyle.Render("Status:"), tsStatusLine))
	statusCard.WriteString(fmt.Sprintf("%-16s %s\n\n", lblStyle.Render("Tailscale IP:"), tsIPLine))

	// Row 2: Manual Connect/Disconnect button
	actionBtnText := "[ Connect Tailscale Now ]"
	if m.tsStatus != nil && m.tsStatus.IsUp {
		actionBtnText = "[ Disconnect Tailscale Now ]"
	}
	btnRendered := btnStyle.Background(lipgloss.Color("#2E3440")).Foreground(lipgloss.Color("#D8DEE9")).Render(actionBtnText)
	cur2 := "  "
	if m.settingsFocus == 2 {
		cur2 = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#88C0D0")).Render("▶ ")
		btnRendered = btnActiveStyle.Render(actionBtnText)
	}
	statusCard.WriteString(fmt.Sprintf("%s%s  %s", cur2, btnRendered, hintStyle.Render("(Press Enter, or press [t] anytime)")))

	cardBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#3B4252")).
		Padding(0, 1).
		Width(innerWidth)
	sb.WriteString(cardBox.Render(statusCard.String()) + "\n\n")

	// 4. Footer Hotkey Legend
	sb.WriteString(headerStyle.Render("[Space / Enter] Toggle Setting  •  [↑/↓/j/k] Navigate  •  [t] Quick Tailscale  •  [s / Esc] Back"))

	return panelStyle.Width(m.width - 4).Render(sb.String())
}

func (m Model) viewHelp() string {
	helpText := `KEYBINDINGS & USAGE

Navigation:
  j / down       Move selection down
  k / up         Move selection up
  /              Filter hosts by name or IP
  o              Toggle sort online-first
  r              Refresh presence status immediately

Actions:
  enter          Wake host (or connect immediately if already online)
  c              Connect (wake → wait for ready → launch action)
  w              Send Wake-on-LAN magic packet only
  z              Suspend / Sleep host (ssh / agent / custom)
  p              Manual ping / health check probe
  s              Open settings page (Tailscale auto-management, options)
  a              Add a new host
  e              Edit selected host
  d              Delete selected host
  l              View activity logs
  ?              Toggle this help screen
  q / ctrl+c     Quit

Press '?' or 'esc' to return.`

	return panelStyle.Width(m.width - 4).Render(helpText)
}

func (m Model) viewLogs() string {
	var sb strings.Builder
	sb.WriteString("ACTIVITY LOGS (Press 'l' or 'esc' to return)\n\n")
	if len(m.logs) == 0 {
		sb.WriteString("No logs recorded yet.")
	} else {
		for _, l := range m.logs {
			sb.WriteString(l + "\n")
		}
	}
	return panelStyle.Width(m.width - 4).Render(sb.String())
}

func (m Model) viewForm() string {
	var sb strings.Builder

	title := "ADD NEW HOST"
	if m.isEditingHost {
		title = "EDIT HOST"
	}
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#88C0D0")).Render(title))
	sb.WriteString("\n")
	sb.WriteString(hintStyle.Render("[Tab / Shift+Tab] navigate  •  [← / →] switch options  •  [Esc] cancel"))
	sb.WriteString("\n\n")

	if m.formErrorMsg != "" {
		sb.WriteString(errorStyle.Render("Error: " + m.formErrorMsg))
		sb.WriteString("\n\n")
	}

	renderField := func(label string, input textinput.Model, isFocused bool, hint string) string {
		lStyle := formLabelStyle
		if isFocused {
			lStyle = formFocusLabelStyle
		}
		cursorIndicator := "  "
		if isFocused {
			cursorIndicator = "> "
		}
		res := fmt.Sprintf("%s%s %s", cursorIndicator, lStyle.Render(label), input.View())
		if hint != "" {
			res += "  " + hintStyle.Render(hint)
		}
		return res
	}

	// 1. SECTION: Identity & Network
	sectionTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#81A1C1")).Render("IDENTITY & NETWORK")
	sb.WriteString(sectionTitle + "\n")
	sb.WriteString(renderField("Nickname *", m.formInputs[0], m.formFocus == 0, "") + "\n")
	sb.WriteString(renderField("MAC Address", m.formInputs[1], m.formFocus == 1, "") + "\n")
	sb.WriteString(renderField("IP / Hostname", m.formInputs[2], m.formFocus == 2, "") + "\n")
	sb.WriteString(renderField("Broadcast IP", m.formInputs[3], m.formFocus == 3, "") + "\n")
	sb.WriteString("\n")

	// 2. SECTION: Connect Action
	connSectionTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#81A1C1")).Render("POST-WAKE CONNECT ACTION")
	sb.WriteString(connSectionTitle + "\n")

	connTypes := []string{"SSH", "Parsec", "Mount", "Game", "Custom"}
	connPills := make([]string, len(connTypes))
	for i, ct := range connTypes {
		if i == m.formConnTypeIdx {
			connPills[i] = activeTabStyle.Render(ct)
		} else {
			connPills[i] = inactiveTabStyle.Render(ct)
		}
	}
	connSelectorCursor := "  "
	connSelectorLabel := formLabelStyle
	if m.formFocus == 4 {
		connSelectorCursor = "> "
		connSelectorLabel = formFocusLabelStyle
	}
	sb.WriteString(fmt.Sprintf("%s%s %s  %s\n",
		connSelectorCursor,
		connSelectorLabel.Render("Connect Method"),
		strings.Join(connPills, " "),
		hintStyle.Render("[← / →] to switch"),
	))

	switch m.formConnTypeIdx {
	case 0: // SSH
		sb.WriteString(renderField("SSH User", m.formInputs[4], m.formFocus == 5, "") + "\n")
		sb.WriteString(renderField("SSH Port", m.formInputs[5], m.formFocus == 6, "") + "\n")
	case 1: // Parsec
		sb.WriteString(renderField("Parsec Peer ID *", m.formInputs[4], m.formFocus == 5, "") + "\n")
		sb.WriteString(renderField("Client Settings", m.formInputs[5], m.formFocus == 6, "") + "\n")
	case 2: // Mount
		sb.WriteString(renderField("Share URL *", m.formInputs[4], m.formFocus == 5, "") + "\n")
	case 3: // Game
		sb.WriteString(renderField("Game Client URL *", m.formInputs[4], m.formFocus == 5, "") + "\n")
	case 4: // Custom
		sb.WriteString(renderField("Run Command *", m.formInputs[4], m.formFocus == 5, "") + "\n")
	}
	sb.WriteString("\n")

	// 3. SECTION: Sleep Method
	sleepSectionTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#81A1C1")).Render("REMOTE SUSPEND / SLEEP")
	sb.WriteString(sleepSectionTitle + "\n")

	sleepTypes := []string{"SSH (Default)", "Agent (HTTP)", "Custom Command"}
	sleepPills := make([]string, len(sleepTypes))
	for i, st := range sleepTypes {
		if i == m.formSleepIdx {
			sleepPills[i] = activeTabStyle.Render(st)
		} else {
			sleepPills[i] = inactiveTabStyle.Render(st)
		}
	}
	sleepSelectorCursor := "  "
	sleepSelectorLabel := formLabelStyle
	if m.formFocus == 7 {
		sleepSelectorCursor = "> "
		sleepSelectorLabel = formFocusLabelStyle
	}
	sb.WriteString(fmt.Sprintf("%s%s %s  %s\n",
		sleepSelectorCursor,
		sleepSelectorLabel.Render("Sleep Method"),
		strings.Join(sleepPills, " "),
		hintStyle.Render("[← / →] to switch"),
	))

	switch m.formSleepIdx {
	case 0: // SSH
		sb.WriteString("     " + hintStyle.Render("systemctl suspend (Linux), osascript (macOS), or rundll32 (Windows)") + "\n")
	case 1: // Agent
		sb.WriteString(renderField("Agent Port", m.formInputs[6], m.formFocus == 8, "") + "\n")
	case 2: // Custom
		sb.WriteString(renderField("Sleep Command *", m.formInputs[6], m.formFocus == 8, "") + "\n")
	}
	sb.WriteString("\n")

	// 4. ACTION BUTTONS
	saveBtn := btnStyle.Background(lipgloss.Color("#2E3440")).Foreground(lipgloss.Color("#D8DEE9")).Render("[ Save Host ]")
	if m.formFocus == 9 {
		saveBtn = btnActiveStyle.Render("> [ Save Host ]")
	}

	cancelBtn := btnStyle.Background(lipgloss.Color("#2E3440")).Foreground(lipgloss.Color("#7B88A1")).Render("[ Cancel ]")
	if m.formFocus == 10 {
		cancelBtn = btnStyle.Background(lipgloss.Color("#4C566A")).Foreground(lipgloss.Color("#ECEFF4")).Bold(true).Render("> [ Cancel ]")
	}

	sb.WriteString(fmt.Sprintf("     %s    %s\n", saveBtn, cancelBtn))

	return panelStyle.Width(m.width - 4).Render(sb.String())
}
