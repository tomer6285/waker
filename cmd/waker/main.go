package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/tomer/waker/pkg/actions"
	"github.com/tomer/waker/pkg/config"
	"github.com/tomer/waker/pkg/health"
	"github.com/tomer/waker/pkg/presence"
	"github.com/tomer/waker/pkg/sleeper"
	"github.com/tomer/waker/pkg/store"
	"github.com/tomer/waker/pkg/tailscale"
	"github.com/tomer/waker/pkg/tui"
	"github.com/tomer/waker/pkg/wol"
)

var (
	cfgFile string
	jsonOut bool
)

var rootCmd = &cobra.Command{
	Use:   "waker",
	Short: "Wake-on-LAN manager with TUI & CLI to wake, monitor, and connect to hosts",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Default action without subcommands launches TUI
		cfg, err := loadOrCreateConfig()
		if err != nil {
			return err
		}
		st, err := store.LoadStore("")
		if err != nil {
			return err
		}

		tsMgr := newTailscaleManager()

		poller := presence.NewPoller(cfg, st)

		model := tui.NewModelWithTailscale(cfg, st, poller, cfgFile, tsMgr)
		defer model.Close()
		p := tea.NewProgram(model, tea.WithAltScreen())
		_, err = p.Run()
		return err
	},
}

func loadOrCreateConfig() (*config.Config, error) {
	path, err := config.ResolveConfigPath(cfgFile)
	if err != nil {
		return nil, err
	}
	// Keep cfgFile updated with resolved path so subsequent saves write to the exact file
	cfgFile = path

	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Create empty starter config
		starter := &config.Config{
			Version: 1,
			Defaults: config.DefaultsConfig{
				Broadcast:    "255.255.255.255",
				WOLPort:      9,
				Timeout:      120 * time.Second,
				PollInterval: 15 * time.Second,
				ProbeTimeout: 2 * time.Second,
			},
			Hosts: []config.HostConfig{},
		}
		if err := starter.Save(path); err != nil {
			return nil, err
		}
		return starter, nil
	}

	return config.Load(path)
}

// Auto-start applies to every network command. Keep the tunnel running after
// return: SSH exec and detached Parsec/mount/game clients still depend on it.
var newTailscaleManager = tailscale.NewManager

func loadCommandConfig(cmd *cobra.Command) (*config.Config, error) {
	cfg, err := loadOrCreateConfig()
	if err != nil {
		return nil, err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if cfg.Settings.AutoTailscale && cmd.Name() != "add" && !dryRun {
		fmt.Fprintln(cmd.ErrOrStderr(), "Checking and connecting to Tailscale (up to 20s)...")
		ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
		defer cancel()
		if _, err := newTailscaleManager().OnLaunch(ctx, true); err != nil {
			return nil, fmt.Errorf("Tailscale startup failed: %w", err)
		}
	}
	return cfg, nil
}

func wakeHost(cmd *cobra.Command, cfg *config.Config, host *config.HostConfig) error {
	relay := host.Relay
	if relay == nil {
		relay = cfg.Defaults.Relay
	}
	if cmd.Flags().Changed("relay") {
		name, _ := cmd.Flags().GetString("relay")
		relay = &wol.RelayConfig{Host: name}
	}
	broadcast := host.Broadcast
	port := host.Port
	if cmd.Flags().Changed("broadcast") {
		broadcast, _ = cmd.Flags().GetString("broadcast")
	}
	if cmd.Flags().Changed("port") {
		port, _ = cmd.Flags().GetInt("port")
	}
	return wol.SendWithRelay(cmd.Context(), relay, host.MAC, wol.Options{BroadcastIP: broadcast, Port: port, IfaceName: host.Interface})
}

// Subcommands

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured hosts and their status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadCommandConfig(cmd)
		if err != nil {
			return err
		}
		st, _ := store.LoadStore("")

		poller := presence.NewPoller(cfg, st)
		statuses := poller.PollOnce(cmd.Context())

		watch, _ := cmd.Flags().GetBool("watch")
		if watch {
			for {
				printList(cfg, statuses)
				time.Sleep(cfg.Defaults.PollInterval)
				statuses = poller.PollOnce(cmd.Context())
				fmt.Print("\033[H\033[2J") // Clear screen
			}
		}

		return printList(cfg, statuses)
	},
}

func printList(cfg *config.Config, statuses map[string]*presence.HostStatusInfo) error {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(statuses)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "STATUS\tNAME\tIP\tMAC\tLATENCY\tCONNECT\tLAST SEEN")
	for _, h := range cfg.Hosts {
		st := statuses[h.Name]
		statusStr := "unknown"
		latencyStr := "-"
		lastSeenStr := "never"
		if st != nil {
			statusStr = string(st.Status)
			if st.Latency > 0 {
				latencyStr = fmt.Sprintf("%dms", st.Latency.Milliseconds())
			}
			if !st.LastSeen.IsZero() {
				lastSeenStr = time.Since(st.LastSeen).Truncate(time.Second).String() + " ago"
			}
		}
		connType := "ssh"
		if h.OnConnect != nil {
			connType = h.OnConnect.Type
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			statusStr, h.Name, h.IP, h.MAC, latencyStr, connType, lastSeenStr)
	}
	return w.Flush()
}

var statusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show status of a specific host",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadCommandConfig(cmd)
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return err
		}

		watch, _ := cmd.Flags().GetBool("watch")
		st, _ := store.LoadStore("")
		poller := presence.NewPoller(cfg, st)

		for {
			info := poller.ProbeOne(cmd.Context(), host)
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(info)
			} else {
				fmt.Printf("Host:      %s\n", host.Name)
				fmt.Printf("Status:    %s\n", info.Status)
				fmt.Printf("IP:        %s\n", host.IP)
				fmt.Printf("MAC:       %s\n", host.MAC)
				fmt.Printf("Latency:   %v\n", info.Latency)
				if !info.LastSeen.IsZero() {
					fmt.Printf("Last Seen: %s\n", info.LastSeen.Format(time.RFC3339))
				}
				for _, c := range info.Checks {
					fmt.Printf("  Check [%s]: success=%v latency=%v error=%s\n", c.Type, c.Success, c.Latency, c.Error)
				}
			}

			if !watch {
				break
			}
			time.Sleep(3 * time.Second)
			fmt.Print("\033[H\033[2J")
		}
		return nil
	},
}

var wakeCmd = &cobra.Command{
	Use:   "wake <name>",
	Short: "Send Wake-on-LAN magic packet to a host",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadCommandConfig(cmd)
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return err
		}

		fmt.Printf("Sending magic packet to %s (%s)...\n", host.Name, host.MAC)
		if err := wakeHost(cmd, cfg, host); err != nil {
			return fmt.Errorf("failed to send WOL packet: %w", err)
		}
		fmt.Println("WOL packet sent; this does not confirm the host is awake.")
		wait, _ := cmd.Flags().GetBool("wait")
		if wait {
			return waitForHostOnline(cmd.Context(), cfg, host, cfg.Defaults.Timeout)
		}
		return nil
	},
}

var connectCmd = &cobra.Command{
	Use:   "connect <name> [-- ssh-options...]",
	Short: "Wake host, wait until it comes online, and execute post-wake action",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadCommandConfig(cmd)
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return err
		}

		nowait, _ := cmd.Flags().GetBool("no-wait")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		timeoutFlag, _ := cmd.Flags().GetDuration("timeout")
		if timeoutFlag == 0 {
			timeoutFlag = cfg.Defaults.Timeout
		}

		if dryRun {
			fmt.Printf("Host: %s (%s)\nConnect action: %+v\nSSH settings: %+v\n", host.Name, host.IP, host.OnConnect, host.SSH)
			return nil
		}
		reachable, _ := health.IsReachable(health.CheckHost(cmd.Context(), host, cfg.Defaults.ProbeTimeout))
		if !reachable {
			noWake, _ := cmd.Flags().GetBool("no-wake")
			if noWake {
				return fmt.Errorf("%s is offline and --no-wake was requested", host.Name)
			}
			fmt.Printf("Waking %s (%s)...\n", host.Name, host.MAC)
			if err := wakeHost(cmd, cfg, host); err != nil {
				return fmt.Errorf("failed sending WOL: %w", err)
			}
			if nowait {
				fmt.Println("WOL sent; not waiting for readiness.")
				return nil
			}
		}
		fmt.Printf("Waiting up to %v for %s to be ready...\n", timeoutFlag, host.Name)
		if err := waitForHostOnline(cmd.Context(), cfg, host, timeoutFlag); err != nil {
			return err
		}
		fmt.Printf("Host %s is ready!\n", host.Name)

		// 3. Execute connect action
		runner := actions.NewRunner(nil, nil, nil)
		extraArgs := args[1:]
		return runner.Connect(cmd.Context(), host, extraArgs, true)
	},
}

func waitForHostOnline(ctx context.Context, cfg *config.Config, host *config.HostConfig, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var last []health.CheckResult
	for {
		last = health.CheckReady(ctx, host, cfg.Defaults.ProbeTimeout)
		if ready, _ := health.IsReachable(last); ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s (%s): %w; last checks: %+v", host.Name, host.IP, ctx.Err(), last)
		case <-ticker.C:
		}
	}
}

var sleepCmd = &cobra.Command{
	Use:   "sleep <name>",
	Short: "Put a host into sleep/suspend mode",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadCommandConfig(cmd)
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return err
		}

		s := sleeper.NewSleeper()
		fmt.Printf("Sending suspend command to %s...\n", host.Name)
		if err := s.Sleep(cmd.Context(), host); err != nil {
			return fmt.Errorf("sleep failed: %w", err)
		}
		fmt.Println("Suspend command issued successfully.")

		waitOffline, _ := cmd.Flags().GetBool("wait")
		if waitOffline {
			fmt.Print("Waiting for host to shut down / go offline...")
			if err := sleeper.WaitForOffline(cmd.Context(), host, 60*time.Second); err != nil {
				return err
			}
			fmt.Println(" Host is now offline.")
		}
		return nil
	},
}

var pingCmd = &cobra.Command{
	Use:   "ping <name>",
	Short: "Run instant health checks / ping on a host",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadCommandConfig(cmd)
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return err
		}

		st, _ := store.LoadStore("")
		poller := presence.NewPoller(cfg, st)
		res := poller.ProbeOne(cmd.Context(), host)

		if jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		}

		fmt.Printf("Status:  %s\n", res.Status)
		fmt.Printf("Latency: %v\n", res.Latency)
		for _, c := range res.Checks {
			fmt.Printf("  - Check [%s]: success=%v latency=%v err=%s\n", c.Type, c.Success, c.Latency, c.Error)
		}
		return nil
	},
}

var addCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Quickly add or update a host in the config file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mac, _ := cmd.Flags().GetString("mac")
		ip, _ := cmd.Flags().GetString("ip")
		bcast, _ := cmd.Flags().GetString("broadcast")
		sshUser, _ := cmd.Flags().GetString("ssh-user")
		sshPort, _ := cmd.Flags().GetInt("ssh-port")

		cfg, err := loadCommandConfig(cmd)
		if err != nil {
			return err
		}

		if existing, err := cfg.FindHost(name); err == nil && !cmd.Flags().Changed("mac") {
			mac = existing.MAC
		}
		if mac == "" && ip != "" {
			fmt.Printf("Auto-detecting MAC for IP %s...\n", ip)
			detectedMAC, err := wol.ResolveMACFromIP(ip)
			if err != nil {
				return fmt.Errorf("could not auto-detect MAC: %w (please provide --mac)", err)
			}
			mac = detectedMAC
			fmt.Printf("Found MAC: %s\n", mac)
		}

		if mac == "" {
			return fmt.Errorf("--mac is required if IP cannot be resolved")
		}

		h := config.HostConfig{Name: name}
		if existing, err := cfg.FindHost(name); err == nil {
			h = *existing
		}
		h.MAC = mac
		if cmd.Flags().Changed("ip") {
			h.IP = ip
		}
		if cmd.Flags().Changed("broadcast") {
			h.Broadcast = bcast
		}
		if cmd.Flags().Changed("relay") {
			relayHost, _ := cmd.Flags().GetString("relay")
			relayUser, _ := cmd.Flags().GetString("relay-user")
			relayPort, _ := cmd.Flags().GetInt("relay-port")
			h.Relay = &wol.RelayConfig{Host: relayHost, User: relayUser, Port: relayPort}
		}

		if cmd.Flags().Changed("ssh-user") || cmd.Flags().Changed("ssh-port") {
			if sshPort == 0 {
				sshPort = 22
			}
			ssh := config.SSHConfig{}
			if h.SSH != nil {
				ssh = *h.SSH
			}
			if cmd.Flags().Changed("ssh-user") {
				ssh.User = sshUser
			}
			if cmd.Flags().Changed("ssh-port") {
				ssh.Port = sshPort
			}
			h.SSH = &ssh
			h.OnConnect = &config.ConnectAction{Type: "ssh"}
		}

		if err := cfg.UpsertHost(h); err != nil {
			return err
		}

		if err := cfg.Validate(); err != nil {
			return err
		}
		if err := cfg.Save(cfgFile); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("Host %q successfully saved to %s\n", name, cfgFile)
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "Config file path (default ~/.config/waker/hosts.yaml)")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "Output results in JSON format")

	listCmd.Flags().BoolP("watch", "w", false, "Continuously watch status")
	statusCmd.Flags().BoolP("watch", "w", false, "Continuously watch host status")

	for _, cmd := range []*cobra.Command{wakeCmd, connectCmd} {
		cmd.Flags().String("relay", "", "SSH relay hostname/IP or alias (overrides configured relay)")
		cmd.Flags().String("broadcast", "", "Broadcast IPv4 address on the target LAN")
		cmd.Flags().Int("port", 9, "WOL UDP destination port")
	}
	wakeCmd.Flags().Bool("wait", false, "Wait for the target to become reachable")
	addCmd.Flags().String("relay", "", "Always-on SSH relay hostname/IP or alias")
	addCmd.Flags().String("relay-user", "", "SSH username on the relay")
	addCmd.Flags().Int("relay-port", 22, "SSH port on the relay")
	connectCmd.Flags().Bool("no-wake", false, "Connect only if the host is already reachable")
	connectCmd.Flags().Bool("no-wait", false, "Send WOL and exit immediately without waiting for host to become ready")
	connectCmd.Flags().Bool("dry-run", false, "Show connect action/settings without waking or connecting")
	connectCmd.Flags().Duration("timeout", 0, "Override wake ready timeout (e.g. 90s)")

	sleepCmd.Flags().Bool("wait", false, "Wait until host goes offline")

	addCmd.Flags().String("mac", "", "Host MAC address (e.g. AA:BB:CC:DD:EE:FF)")
	addCmd.Flags().String("ip", "", "Host IP address or hostname (e.g. 192.168.1.100 or desktop.tailnet.ts.net)")
	addCmd.Flags().String("broadcast", "", "Custom broadcast IP (optional)")
	addCmd.Flags().String("ssh-user", "", "SSH username for connect/sleep")
	addCmd.Flags().Int("ssh-port", 22, "SSH port")

	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(wakeCmd)
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(sleepCmd)
	rootCmd.AddCommand(pingCmd)
	rootCmd.AddCommand(addCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
