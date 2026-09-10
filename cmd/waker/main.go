package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
		st, _ := store.LoadStore("")
		poller := presence.NewPoller(cfg, st)
		poller.PollOnce(context.Background())

		p := tea.NewProgram(tui.NewModel(cfg, st, poller, cfgFile), tea.WithAltScreen())
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
		_ = starter.Save(path)
		return starter, nil
	}

	return config.Load(path)
}

// Subcommands

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured hosts and their status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadOrCreateConfig()
		if err != nil {
			return err
		}
		st, _ := store.LoadStore("")
		poller := presence.NewPoller(cfg, st)
		statuses := poller.PollOnce(context.Background())

		watch, _ := cmd.Flags().GetBool("watch")
		if watch {
			for {
				printList(cfg, statuses)
				time.Sleep(cfg.Defaults.PollInterval)
				statuses = poller.PollOnce(context.Background())
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
		cfg, err := loadOrCreateConfig()
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
			info := poller.ProbeOne(context.Background(), host)
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
	Short: "Send Wake-on-LAN packet to a host",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadOrCreateConfig()
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return err
		}

		bcastFlag, _ := cmd.Flags().GetString("broadcast")
		portFlag, _ := cmd.Flags().GetInt("port")
		waitFlag, _ := cmd.Flags().GetBool("wait")
		relayFlag, _ := cmd.Flags().GetString("relay")

		opts := wol.Options{
			BroadcastIP: host.Broadcast,
			Port:        host.Port,
			IfaceName:   host.Interface,
		}
		if bcastFlag != "" {
			opts.BroadcastIP = bcastFlag
		}
		if portFlag != 0 {
			opts.Port = portFlag
		}

		relay := host.Relay
		if relay == nil {
			relay = cfg.Defaults.Relay
		}
		if relayFlag != "" {
			relay = &wol.RelayConfig{Host: relayFlag}
		}

		if relay != nil && relay.Host != "" {
			fmt.Printf("Sending Wake-on-LAN packet to %s (%s) via SSH relay %s...\n",
				host.Name, host.MAC, relay.Host)
		} else {
			fmt.Printf("Sending Wake-on-LAN packet to %s (%s) via %s:%d...\n",
				host.Name, host.MAC, opts.BroadcastIP, opts.Port)
		}

		if err := wol.SendWithRelay(context.Background(), relay, host.MAC, opts); err != nil {
			return fmt.Errorf("failed to send WOL: %w", err)
		}
		fmt.Println("✓ Magic packet sent!")

		st, _ := store.LoadStore("")
		st.RecordWake(host.Name)
		_ = st.Save()

		if waitFlag {
			fmt.Printf("Waiting for %s to become reachable...\n", host.Name)
			return waitForHostOnline(context.Background(), cfg, host, cfg.Defaults.Timeout)
		}
		return nil
	},
}

var connectCmd = &cobra.Command{
	Use:   "connect <name> [-- extra args]",
	Short: "Wake machine on demand, wait until ready, and connect",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadOrCreateConfig()
		if err != nil {
			return err
		}
		hostName := args[0]
		extraArgs := args[1:]

		host, err := cfg.FindHost(hostName)
		if err != nil {
			return err
		}

		timeoutFlag, _ := cmd.Flags().GetDuration("timeout")
		noWake, _ := cmd.Flags().GetBool("no-wake")

		timeout := cfg.Defaults.Timeout
		if timeoutFlag > 0 {
			timeout = timeoutFlag
		}

		st, _ := store.LoadStore("")
		poller := presence.NewPoller(cfg, st)

		// Probe initial state
		fmt.Printf("Checking if %s is already online...\n", host.Name)
		info := poller.ProbeOne(context.Background(), host)

		if info.Status == presence.StatusOnline {
			fmt.Printf("Host %s is already online (%dms latency). Connecting directly...\n",
				host.Name, info.Latency.Milliseconds())
		} else {
			if noWake {
				return fmt.Errorf("host %s is offline and --no-wake was specified", host.Name)
			}
			fmt.Printf("Host %s is %s. Sending Wake-on-LAN packet...\n", host.Name, info.Status)
			relay := host.Relay
			if relay == nil {
				relay = cfg.Defaults.Relay
			}
			opts := wol.Options{
				BroadcastIP: host.Broadcast,
				Port:        host.Port,
				IfaceName:   host.Interface,
			}
			if err := wol.SendWithRelay(context.Background(), relay, host.MAC, opts); err != nil {
				return fmt.Errorf("failed to send WOL packet: %w", err)
			}
			fmt.Printf("WOL packet sent. Waiting up to %v for host to wake and become reachable...\n", timeout)
			if err := waitForHostOnline(context.Background(), cfg, host, timeout); err != nil {
				return err
			}
			fmt.Println("✓ Host is online!")
		}

		// Connect action
		runner := actions.NewRunner(os.Stdout, os.Stderr, os.Stdin)
		return runner.Connect(context.Background(), host, extraArgs, true)
	},
}

func waitForHostOnline(ctx context.Context, cfg *config.Config, host *config.HostConfig, timeout time.Duration) error {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	start := time.Now()
	for {
		select {
		case <-ctxTimeout.Done():
			return fmt.Errorf("no response in %v — check BIOS/allow WOL, same subnet?", timeout)
		case <-ticker.C:
			res := health.CheckHost(ctxTimeout, host, cfg.Defaults.ProbeTimeout)
			reachable, latency := health.IsReachable(res)
			if reachable {
				st, _ := store.LoadStore("")
				st.RecordSeen(host.Name, latency)
				_ = st.Save()
				return nil
			}
			fmt.Printf("  ...still waiting (%ds elapsed)\n", int(time.Since(start).Seconds()))
		}
	}
}

var sleepCmd = &cobra.Command{
	Use:   "sleep <name>",
	Short: "Suspend/sleep a machine without extra prompts",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadOrCreateConfig()
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return err
		}

		methodFlag, _ := cmd.Flags().GetString("method")
		timeoutFlag, _ := cmd.Flags().GetDuration("timeout")
		confirm, _ := cmd.Flags().GetBool("confirm")

		if methodFlag != "" {
			if host.OnSleep == nil {
				host.OnSleep = &config.SleepAction{}
			}
			host.OnSleep.Type = methodFlag
		}

		// Check if host is online before sleeping
		res := health.CheckHost(context.Background(), host, cfg.Defaults.ProbeTimeout)
		reachable, _ := health.IsReachable(res)
		if !reachable {
			fmt.Printf("Host %s is already offline.\n", host.Name)
			return nil
		}

		if confirm {
			fmt.Printf("Are you sure you want to suspend %s? [y/N]: ", host.Name)
			var resp string
			_, _ = fmt.Scanln(&resp)
			if strings.ToLower(resp) != "y" && strings.ToLower(resp) != "yes" {
				fmt.Println("Sleep canceled.")
				return nil
			}
		}

		fmt.Printf("Suspending host %s...\n", host.Name)
		sl := sleeper.NewSleeper()
		if err := sl.Sleep(context.Background(), host); err != nil {
			return fmt.Errorf("failed to trigger sleep: %w", err)
		}

		timeout := 10 * time.Second
		if timeoutFlag > 0 {
			timeout = timeoutFlag
		}

		fmt.Printf("Waiting for %s to transition to offline...\n", host.Name)
		if err := sleeper.WaitForOffline(context.Background(), host, timeout); err != nil {
			fmt.Printf("Notice: %v (host may still be suspending)\n", err)
		} else {
			fmt.Printf("✓ Host %s is now offline.\n", host.Name)
		}

		st, _ := store.LoadStore("")
		st.RecordStatus(host.Name, "offline")
		_ = st.Save()
		return nil
	},
}

var pingCmd = &cobra.Command{
	Use:   "ping <name>",
	Short: "Probe host health check targets immediately",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadOrCreateConfig()
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return err
		}

		fmt.Printf("Probing %s (%s)...\n", host.Name, host.IP)
		results := health.CheckHost(context.Background(), host, cfg.Defaults.ProbeTimeout)
		for _, r := range results {
			status := "FAIL"
			if r.Success {
				status = "OK"
			}
			fmt.Printf("  %-6s %-20s [%s] %v %s\n", r.Type, r.Target, status, r.Latency, r.Error)
		}

		reachable, minLat := health.IsReachable(results)
		if reachable {
			fmt.Printf("Result: ONLINE (%v)\n", minLat)
			return nil
		}
		if health.IsSubnetUnreachable(results) {
			fmt.Println("Result: UNREACHABLE (not on local network)")
			os.Exit(1)
			return nil
		}
		fmt.Println("Result: OFFLINE")
		os.Exit(1)
		return nil
	},
}

var addCmd = &cobra.Command{
	Use:   "add",
	Short: "Add or update a host in the configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadOrCreateConfig()
		if err != nil {
			return err
		}

		name, _ := cmd.Flags().GetString("name")
		mac, _ := cmd.Flags().GetString("mac")
		ip, _ := cmd.Flags().GetString("ip")
		bcast, _ := cmd.Flags().GetString("broadcast")
		connType, _ := cmd.Flags().GetString("connect")
		peerID, _ := cmd.Flags().GetString("peer-id")
		sshUser, _ := cmd.Flags().GetString("user")
		sshPort, _ := cmd.Flags().GetInt("port")

		if name == "" {
			return fmt.Errorf("--name is required")
		}

		if mac == "" {
			if ip == "" {
				return fmt.Errorf("--mac is required (or provide an active --ip to auto-detect)")
			}
			fmt.Printf("MAC address not specified. Probing %s to auto-detect MAC...\n", ip)
			detectedMAC, err := wol.ResolveMACFromIP(ip)
			if err != nil {
				return fmt.Errorf("could not auto-detect MAC from %s: %w (please provide --mac manually)", ip, err)
			}
			fmt.Printf("✓ Auto-detected MAC: %s\n", detectedMAC)
			mac = detectedMAC
		}

		relayHost, _ := cmd.Flags().GetString("relay")

		h := config.HostConfig{
			Name:      name,
			MAC:       mac,
			IP:        ip,
			Broadcast: bcast,
		}

		if relayHost != "" {
			h.Relay = &wol.RelayConfig{Host: relayHost}
		}

		if connType != "" {
			h.OnConnect = &config.ConnectAction{Type: connType}
			if connType == "parsec" {
				h.OnConnect.PeerID = peerID
			}
		}

		if sshUser != "" || sshPort != 0 {
			if sshPort == 0 {
				sshPort = 22
			}
			h.SSH = &config.SSHConfig{User: sshUser, Port: sshPort}
		}

		if err := cfg.UpsertHost(h); err != nil {
			return err
		}

		if err := cfg.Save(cfgFile); err != nil {
			return err
		}

		fmt.Printf("✓ Host %s added/updated in %s\n", name, cfgFile)
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "Config file path (default ~/.config/waker/hosts.yaml)")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	listCmd.Flags().Bool("watch", false, "Continuously watch host presence")
	statusCmd.Flags().Bool("watch", false, "Continuously watch host status")

	wakeCmd.Flags().String("broadcast", "", "Custom broadcast address")
	wakeCmd.Flags().Int("port", 0, "Custom UDP port (default 9)")
	wakeCmd.Flags().Bool("wait", false, "Wait for host to become reachable after waking")
	wakeCmd.Flags().String("relay", "", "Remote SSH relay host (e.g. home-pi)")

	connectCmd.Flags().Duration("timeout", 0, "Connection/wake timeout")
	connectCmd.Flags().Bool("no-wake", false, "Do not send WOL packet if offline; fail immediately")

	sleepCmd.Flags().String("method", "", "Sleep method (ssh, agent, custom)")
	sleepCmd.Flags().Duration("timeout", 10*time.Second, "Timeout waiting for machine to go offline")
	sleepCmd.Flags().Bool("confirm", false, "Prompt for confirmation before suspending")

	addCmd.Flags().String("name", "", "Host identifier name")
	addCmd.Flags().String("mac", "", "Host MAC address")
	addCmd.Flags().String("ip", "", "Host IP address")
	addCmd.Flags().String("broadcast", "", "Broadcast IP address")
	addCmd.Flags().String("connect", "ssh", "Connect action type (ssh, parsec, mount, game, custom)")
	addCmd.Flags().String("peer-id", "", "Parsec peer ID (when connect=parsec)")
	addCmd.Flags().String("user", "", "SSH username")
	addCmd.Flags().Int("port", 22, "SSH/Service port")
	addCmd.Flags().String("relay", "", "Remote SSH relay host (e.g. home-pi)")

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
