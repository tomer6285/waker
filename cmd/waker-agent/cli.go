package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tomer/waker/pkg/agentinstall"
	"github.com/tomer/waker/pkg/distribution"
)

func elevate() (bool, error) {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	fmt.Fprintln(os.Stderr, "Installing the system service requires administrator privileges (sudo).")
	cmd := exec.Command("sudo", append([]string{"--", exe}, os.Args[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return true, cmd.Run()
}

func newAgentCommand() *cobra.Command {
	var bind, token, configPath string
	var port int
	root := &cobra.Command{Use: "waker-agent", Short: "Sleep agent: setup once, then manage it here", Version: distribution.Version, SilenceUsage: true}
	run := func(cmd *cobra.Command, args []string) error {
		if configPath != "" {
			c, err := agentinstall.ReadConfig(configPath)
			if err != nil {
				return err
			}
			return serve(c.Bind, c.Port, c.Token)
		}
		if token == "" {
			token = os.Getenv("WAKER_AGENT_TOKEN")
		}
		if token == "" {
			return fmt.Errorf("no token configured; run waker-agent setup, or set WAKER_AGENT_TOKEN for manual use")
		}
		return serve(bind, port, token)
	}
	root.RunE = run
	root.Args = cobra.NoArgs
	root.Flags().StringVar(&bind, "bind", "127.0.0.1", "Manual mode bind IP")
	root.Flags().IntVar(&port, "port", 9876, "Manual mode port")
	root.Flags().StringVar(&token, "token", "", "Manual mode token (prefer WAKER_AGENT_TOKEN)")
	root.Flags().StringVar(&configPath, "config", "", "Read protected agent configuration")
	runCmd := &cobra.Command{Use: "run", Short: "Run using installed configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if configPath == "" {
			p, err := agentinstall.DefaultPaths()
			if err != nil {
				return err
			}
			configPath = p.Config
		}
		return run(cmd, args)
	}}
	runCmd.Flags().StringVar(&configPath, "config", "", "Agent configuration path")
	root.AddCommand(runCmd)

	var setupBind, provision, pairing string
	var setupPort int
	var noPair bool
	setup := &cobra.Command{Use: "setup", Short: "Generate credentials, install autostart, and verify the agent", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if done, err := elevate(); done || err != nil {
			return err
		}
		if runtime.GOOS == "darwin" && os.Geteuid() == 0 {
			return fmt.Errorf("run setup as the logged-in desktop user, without sudo")
		}
		m, err := agentinstall.NewManager(cmd.OutOrStdout(), cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		cfg, err := agentinstall.ReadConfig(m.Paths.Config)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if os.IsNotExist(err) {
			cfg = agentinstall.Config{Port: 9876}
		}
		if provision != "" {
			cfg, err = agentinstall.ReadConfig(provision)
			if err != nil {
				return err
			}
		}
		if setupBind != "" {
			cfg.Bind = setupBind
		}
		if cfg.Bind == "" {
			cfg.Bind, err = agentinstall.DetectBind()
			if err != nil {
				return err
			}
		}
		if cmd.Flags().Changed("port") {
			cfg.Port = setupPort
		}
		if cfg.Token == "" {
			cfg.Token, err = agentinstall.NewToken()
			if err != nil {
				return err
			}
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		pairPath := ""
		if !noPair {
			pairPath, err = filepath.Abs(pairing)
			if err != nil {
				return err
			}
			for _, reserved := range []string{m.Paths.Config, m.Paths.Binary, m.Paths.Service, m.Paths.Log, exe, provision} {
				if reserved == "" {
					continue
				}
				reserved, err = filepath.Abs(reserved)
				if err != nil {
					return err
				}
				same := pairPath == reserved
				if runtime.GOOS == "windows" {
					same = strings.EqualFold(pairPath, reserved)
				}
				if same {
					return fmt.Errorf("pairing file must be separate from executable, configuration, and service files")
				}
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Installing agent and enabling autostart...")
		if err := m.Install(exe, cfg); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Checking connection and authentication (no sleep request)...")
		ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
		defer cancel()
		if err := agentinstall.Verify(ctx, cfg.Bind, cfg.Port, cfg.Token); err != nil {
			return err
		}
		if !noPair {
			path := pairPath
			if err := agentinstall.WriteConfig(path, cfg); err != nil {
				return err
			}
			if runtime.GOOS == "linux" && os.Getenv("SUDO_UID") != "" {
				uid, e1 := strconv.Atoi(os.Getenv("SUDO_UID"))
				gid, e2 := strconv.Atoi(os.Getenv("SUDO_GID"))
				if e1 != nil || e2 != nil {
					return fmt.Errorf("invalid sudo user IDs")
				}
				if err := os.Lchown(path, uid, gid); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pairing file: %s\nCopy it securely to your client, then run:\n  waker setup HOST --pair-file %s\nThe file contains a credential; delete the transferred copies after pairing.\n", path, filepath.Base(path))
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Ready. Use waker-agent status or waker-agent logs to inspect the installation.")
		if runtime.GOOS == "darwin" {
			fmt.Fprintln(cmd.OutOrStdout(), "Starts at login and stops at logout. macOS may ask for Automation permission on the first sleep request.")
		}
		return nil
	}}
	setup.Flags().StringVar(&setupBind, "bind", "", "Bind IP (default: detect Tailscale; reuse existing on repair)")
	setup.Flags().IntVar(&setupPort, "port", 9876, "Sleep endpoint port")
	setup.Flags().StringVar(&provision, "provision-file", "", "Provision with a protected configuration file (SSH installer)")
	setup.Flags().StringVar(&pairing, "pair-file", "waker-agent-pair.json", "Write protected client pairing file here")
	setup.Flags().BoolVar(&noPair, "no-pair", false, "Do not export a pairing file")
	root.AddCommand(setup)
	for _, name := range []string{"status", "logs", "uninstall", "update"} {
		name := name
		sub := &cobra.Command{Use: name, Short: map[string]string{"status": "Show service status", "logs": "Show recent agent logs", "uninstall": "Stop agent and remove autostart (retain files)", "update": "Download and install an agent release"}[name], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			if name != "status" {
				if done, err := elevate(); done || err != nil {
					return err
				}
			}
			m, err := agentinstall.NewManager(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			switch name {
			case "status":
				return m.Status()
			case "logs":
				return m.Logs()
			case "uninstall":
				if err := m.Uninstall(); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Autostart removed and agent stopped. Configuration, binary, and logs are retained for reinstallation.")
				return nil
			default:
				cfg, err := agentinstall.ReadConfig(m.Paths.Config)
				if err != nil {
					return err
				}
				if runtime.GOOS == "windows" {
					return startWindowsUpdate(cmd, m)
				}
				dir, err := os.MkdirTemp("", "waker-update-*")
				if err != nil {
					return err
				}
				defer os.RemoveAll(dir)
				release, _ := cmd.Flags().GetString("release")
				binary, err := distribution.Download(cmd.Context(), "waker-agent", runtime.GOOS, runtime.GOARCH, release, dir)
				if err != nil {
					return err
				}
				if err := m.Install(binary, cfg); err != nil {
					return err
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
				defer cancel()
				return agentinstall.Verify(ctx, cfg.Bind, cfg.Port, cfg.Token)
			}
		}}
		if name == "update" {
			sub.Flags().String("release", "latest", "Release version or latest")
		}
		root.AddCommand(sub)
	}
	return root
}
func main() {
	if err := newAgentCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

// Windows cannot replace the currently running executable. A detached helper waits
// for this CLI process to exit, then runs the downloaded installer's setup command.
func startWindowsUpdate(cmd *cobra.Command, m *agentinstall.Manager) error {
	dir, err := os.MkdirTemp(m.Paths.Dir, "update-*")
	if err != nil {
		return err
	}
	launched := false
	defer func() {
		if !launched {
			_ = os.RemoveAll(dir)
		}
	}()
	release, _ := cmd.Flags().GetString("release")
	binary, err := distribution.Download(cmd.Context(), "waker-agent", runtime.GOOS, runtime.GOARCH, release, dir)
	if err != nil {
		return err
	}
	logPath := filepath.Join(m.Paths.Dir, "update.log")
	scriptPath := filepath.Join(dir, "update.ps1")
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
Wait-Process -Id %d -ErrorAction SilentlyContinue
try {
 & %s setup --no-pair *>> %s
 if ($LASTEXITCODE -ne 0) { throw 'Agent update failed; inspect the setup output above.' }
} catch {
 $_ | Out-File -Append %s
} finally {
 Remove-Item -Recurse -Force %s
}
`, os.Getpid(), agentinstall.PSQuote(binary), agentinstall.PSQuote(logPath), agentinstall.PSQuote(logPath), agentinstall.PSQuote(dir))
	if err := agentinstall.WritePrivate(scriptPath, []byte(script)); err != nil {
		return err
	}
	args := agentinstall.PSQuote(`-NoProfile -ExecutionPolicy Bypass -File "` + scriptPath + `"`)
	if err := m.Run("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop'; Start-Process -FilePath powershell.exe -WindowStyle Hidden -ArgumentList "+args); err != nil {
		return err
	}
	launched = true
	fmt.Fprintf(cmd.OutOrStdout(), "Update scheduled after this command exits. Setup results and errors: %s\n", logPath)
	return nil
}
