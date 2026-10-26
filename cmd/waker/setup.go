package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tomer/waker/pkg/agentinstall"
	"github.com/tomer/waker/pkg/config"
	"github.com/tomer/waker/pkg/distribution"
)

func credentialPath(configPath, name string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(name)))
	return filepath.Join(filepath.Dir(configPath), "credentials", hex.EncodeToString(sum[:])+".token")
}

func newSetupCommand() *cobra.Command {
	var pairing, binary, release, bind string
	var port int
	cmd := &cobra.Command{Use: "setup <host>", Short: "Install a sleep agent over SSH, or pair a locally installed agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadCommandConfig(cmd)
		if err != nil {
			return err
		}
		host, err := cfg.FindHost(args[0])
		if err != nil {
			return fmt.Errorf("add the host first with waker add: %w", err)
		}
		if host.IP == "" {
			return fmt.Errorf("host needs an IP or hostname before setup")
		}
		var provision agentinstall.Config
		path, err := filepath.Abs(credentialPath(cfgFile, host.Name))
		if err != nil {
			return err
		}
		if pairing != "" {
			if cmd.Flags().Changed("bind") || cmd.Flags().Changed("port") || cmd.Flags().Changed("agent-binary") || cmd.Flags().Changed("release") {
				return fmt.Errorf("--pair-file cannot be combined with installation options")
			}
			provision, err = agentinstall.ReadConfig(pairing)
			if err != nil {
				return err
			}
		} else {
			remote := agentinstall.Remote{Target: host.IP, In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
			if host.SSH != nil {
				if host.SSH.User != "" {
					remote.Target = host.SSH.User + "@" + host.IP
				}
				if host.SSH.Port != 0 {
					remote.Args = append(remote.Args, "-p", fmt.Sprint(host.SSH.Port))
				}
				remote.Args = append(remote.Args, host.SSH.Args...)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Detecting target platform over SSH...")
			platform, arch, err := remote.Probe(cmd.Context())
			if err != nil {
				return err
			}
			if bind == "" {
				bind, err = remote.DetectBind(cmd.Context())
				if err != nil {
					return err
				}
			}
			if !cmd.Flags().Changed("port") && host.OnSleep != nil && host.OnSleep.Port != 0 {
				port = host.OnSleep.Port
			}
			// Reuse the client credential on repeated setup, including an interrupted run.
			secret, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			token := strings.TrimSpace(string(secret))
			if token == "" && host.OnSleep != nil {
				if host.OnSleep.TokenFile != "" {
					b, err := os.ReadFile(host.OnSleep.TokenFile)
					if err != nil {
						return err
					}
					token = strings.TrimSpace(string(b))
				}
				if token == "" && host.OnSleep.TokenEnv != "" {
					token = os.Getenv(host.OnSleep.TokenEnv)
				}
				if token == "" {
					token = host.OnSleep.Token
				}
			}
			// Setup upgrades legacy short tokens to generated strong credentials.
			if len(token) < 32 {
				token, err = agentinstall.NewToken()
				if err != nil {
					return err
				}
			}
			provision = agentinstall.Config{Bind: bind, Port: port, Token: token}
			if err := provision.Validate(); err != nil {
				return err
			}
			temp, err := os.MkdirTemp("", "waker-setup-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(temp)
			if binary == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Downloading checksum-verified agent for %s/%s...\n", platform, arch)
				binary, err = distribution.Download(cmd.Context(), "waker-agent", platform, arch, release, temp)
				if err != nil {
					return err
				}
			}
			// Save before touching the target so a failed/interrupted install is recoverable.
			if err := agentinstall.WritePrivate(path, []byte(token+"\n")); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Installing on target; Linux may prompt for its sudo password...")
			if err := remote.Install(cmd.Context(), binary, platform, provision); err != nil {
				return err
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Verifying authenticated access from this client (no sleep request)...")
		ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
		defer cancel()
		if err := agentinstall.Verify(ctx, host.IP, provision.Port, provision.Token); err != nil {
			return err
		}
		if err := agentinstall.WritePrivate(path, []byte(provision.Token+"\n")); err != nil {
			return err
		}
		host.OnSleep = &config.SleepAction{Type: "agent", Port: provision.Port, TokenFile: path}
		if err := cfg.Validate(); err != nil {
			return err
		}
		if err := cfg.Save(cfgFile); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s is ready. Sleep method saved; no token exports needed.\nUse waker sleep %s --wait when you want to test suspension.\n", host.Name, host.Name)
		return nil
	}}
	cmd.Flags().StringVar(&pairing, "pair-file", "", "Pair using the protected file exported by local agent setup")
	cmd.Flags().StringVar(&binary, "agent-binary", "", "Use an existing agent binary built for the target instead of downloading")
	cmd.Flags().StringVar(&release, "release", "latest", "Agent release version or latest")
	cmd.Flags().StringVar(&bind, "bind", "", "Target bind IP (default: detect target Tailscale address)")
	cmd.Flags().IntVar(&port, "port", 9876, "Agent port")
	return cmd
}
func init() {
	rootCmd.Version = distribution.Version
	rootCmd.AddCommand(newSetupCommand())
}
