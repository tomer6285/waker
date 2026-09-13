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
		st, _ := store.LoadStore("")

		tsMgr := tailscale.NewManager()
		var tsBroughtUp bool
		var tsLaunchErr error
		if cfg.Settings.AutoTailscale {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			tsBroughtUp, tsLaunchErr = tsMgr.OnLaunch(ctx, cfg.Settings.AutoTailscale)
			cancel()
		}

		defer func() {
			if cfg.Settings.AutoTailscale {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = tsMgr.OnQuit(ctx, cfg.Settings.AutoTailscale)
				cancel()
			}
		}()

		poller := presence.NewPoller(cfg, st)
		poller.PollOnce(context.Background())

		model := tui.NewModelWithTailscale(cfg, st, poller, cfgFile, tsMgr)
		if tsBroughtUp {
			model.AddLog("Tailscale connected on launch ✓")
		} else if tsLaunchErr != nil {
			model.AddLog(fmt.Sprintf("Tailscale launch failed: %v", tsLaunchErr))
		}
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

		tsMgr := tailscale.NewManager()
		if cfg.Settings.AutoTailscale {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			_, _ = tsMgr.OnLaunch(ctx, cfg.Settings.AutoTailscale)
			cancel()
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = tsMgr.OnQuit(ctx, cfg.Settings.AutoTailscale)
				cancel()
			}()
		}

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
	Short: "Send Wake-on-LAN magic packet to a host",
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

		fmt.Printf("Sending magic packet to %s (%s)...\n", host.Name, host.MAC)
		opts := wol.Options{
			BroadcastIP: host.Broadcast,
			Port:        host.Port,
			IfaceName:   host.Interface,
		}

		relay := host.Relay
		if relay == nil {
			relay = cfg.Defaults.Relay
		}

		err = wol.SendWithRelay(context.Background(), relay, host.MAC, opts)
		if err != nil {
			return fmt.Errorf("failed to send WOL packet: %w", err)
		}
		fmt.Println("WOL packet sent successfully.")
		return nil
	},
}

var connectCmd = &cobra.Command{
	Use:   "connect <name>",
	Short: "Wake host, wait until it comes online, and execute post-wake action",
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

		nowait, _ := cmd.Flags().GetBool("no-wait")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		timeoutFlag, _ := cmd.Flags().GetDuration("timeout")
		if timeoutFlag == 0 {
			timeoutFlag = cfg.Defaults.Timeout
		}

		// 1. Send WOL packet
		opts := wol.Options{
			BroadcastIP: host.Broadcast,
			Port:        host.Port,
			IfaceName:   host.Interface,
		}
		relay := host.Relay
		if relay == nil {
			relay = cfg.Defaults.Relay
		}

		fmt.Printf("Waking %s (%s)...\n", host.Name, host.MAC)
		if err := wol.SendWithRelay(context.Background(), relay, host.MAC, opts); err != nil {
			return fmt.Errorf("failed sending WOL: %w", err)
		}

		if nowait {
			fmt.Println("Sent WOL. (--no-wait requested, exiting)")
			return nil
		}

		// 2. Wait for host to come online
		fmt.Printf("Waiting up to %v for %s to come online...\n", timeoutFlag, host.Name)
		if err := waitForHostOnline(context.Background(), cfg, host, timeoutFlag); err != nil {
			return err
		}
		fmt.Printf("Host %s is online!\n", host.Name)

		// 3. Execute connect action
		runner := actions.NewRunner(nil, nil, nil)
		extraArgs := args[1:]
		return runner.Connect(context.Background(), host, extraArgs, dryRun)
	},
}

func waitForHostOnline(ctx context.Context, cfg *config.Config, host *config.HostConfig, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	st, _ := store.LoadStore("")
	poller := presence.NewPoller(cfg, st)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s to become online", host.Name)
		case <-ticker.C:
			res := poller.ProbeOne(ctx, host)
			if res.Status == presence.StatusOnline {
				return nil
			}
		}
	}
}

var sleepCmd = &cobra.Command{
	Use:   "sleep <name>",
	Short: "Put a host into sleep/suspend mode",
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

		s := sleeper.NewSleeper()
		fmt.Printf("Sending suspend command to %s...\n", host.Name)
		if err := s.Sleep(context.Background(), host); err != nil {
			return fmt.Errorf("sleep failed: %w", err)
		}
		fmt.Println("Suspend command issued successfully.")

		waitOffline, _ := cmd.Flags().GetBool("wait")
		if waitOffline {
			fmt.Print("Waiting for host to shut down / go offline...")
			if err := sleeper.WaitForOffline(context.Background(), host, 60*time.Second); err != nil {
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
		cfg, err := loadOrCreateConfig()
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return err
		}

		st, _ := store.LoadStore("")
		poller := presence.NewPoller(cfg, st)
		res := poller.ProbeOne(context.Background(), host)

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

		cfg, err := loadOrCreateConfig()
		if err != nil {
			return err
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

		h := config.HostConfig{
			Name:      name,
			MAC:       mac,
			IP:        ip,
			Broadcast: bcast,
		}

		if sshUser != "" || sshPort != 0 {
			if sshPort == 0 {
				sshPort = 22
			}
			h.SSH = &config.SSHConfig{
				User: sshUser,
				Port: sshPort,
			}
			h.OnConnect = &config.ConnectAction{Type: "ssh"}
		}

		if err := cfg.UpsertHost(h); err != nil {
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

	connectCmd.Flags().Bool("no-wait", false, "Send WOL and exit immediately without waiting for host to become ready")
	connectCmd.Flags().Bool("dry-run", false, "Print connect action command without running it")
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
