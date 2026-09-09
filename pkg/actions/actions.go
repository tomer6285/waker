package actions

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/tomer/waker/pkg/config"
)

// Runner executes post-wake actions.
type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

func NewRunner(stdout, stderr io.Writer, stdin io.Reader) *Runner {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	if stdin == nil {
		stdin = os.Stdin
	}
	return &Runner{Stdout: stdout, Stderr: stderr, Stdin: stdin}
}

// Connect runs the appropriate action for connecting to a host.
// replaceProcess determines whether we exec via syscall.Exec (CLI) or run child process (TUI).
func (r *Runner) Connect(ctx context.Context, host *config.HostConfig, extraArgs []string, replaceProcess bool) error {
	action := host.OnConnect
	if action == nil {
		// Default to ssh if ssh defined
		if host.SSH != nil || host.IP != "" {
			action = &config.ConnectAction{Type: "ssh"}
		} else {
			return fmt.Errorf("no connect action configured for host %s", host.Name)
		}
	}

	switch strings.ToLower(action.Type) {
	case "ssh":
		return r.connectSSH(ctx, host, extraArgs, replaceProcess)
	case "parsec":
		return r.connectParsec(ctx, host, action)
	case "mount":
		return r.connectMount(ctx, host, action)
	case "game":
		return r.connectGame(ctx, host, action)
	case "custom":
		return r.connectCustom(ctx, host, action)
	default:
		return fmt.Errorf("unsupported connect action type: %s", action.Type)
	}
}

func (r *Runner) connectSSH(ctx context.Context, host *config.HostConfig, extraArgs []string, replaceProcess bool) error {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found in PATH: %w", err)
	}

	var args []string
	if host.SSH != nil {
		if host.SSH.Port != 0 && host.SSH.Port != 22 {
			args = append(args, "-p", strconv.Itoa(host.SSH.Port))
		}
		args = append(args, host.SSH.Args...)
	}

	// Add destination
	target := host.IP
	if target == "" {
		target = host.Name
	}
	if host.SSH != nil && host.SSH.User != "" {
		target = fmt.Sprintf("%s@%s", host.SSH.User, target)
	}
	args = append(args, target)
	args = append(args, extraArgs...)

	fullArgs := append([]string{"ssh"}, args...)

	if replaceProcess && runtime.GOOS != "windows" {
		return syscall.Exec(sshPath, fullArgs, os.Environ())
	}

	cmd := exec.CommandContext(ctx, sshPath, args...)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	cmd.Stdin = r.Stdin
	return cmd.Run()
}

// LocateParsec locates the Parsec executable across platforms
func LocateParsec() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		candidates := []string{
			"/Applications/Parsec.app/Contents/MacOS/parsecd",
			"/Applications/Parsec.app/Contents/MacOS/parsec",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
	case "windows":
		candidates := []string{
			`C:\Program Files\Parsec\parsecd.exe`,
			`C:\Program Files\Parsec\parsec.exe`,
			`C:\Program Files (x86)\Parsec\parsecd.exe`,
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
	default: // Linux
		if p, err := exec.LookPath("parsecd"); err == nil {
			return p, nil
		}
		if p, err := exec.LookPath("parsec"); err == nil {
			return p, nil
		}
		candidates := []string{"/usr/bin/parsecd", "/usr/local/bin/parsecd", "/usr/bin/parsec"}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
	}

	if p, err := exec.LookPath("parsecd"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("parsec executable not found on system (check /Applications/Parsec.app or PATH)")
}

func (r *Runner) connectParsec(ctx context.Context, host *config.HostConfig, action *config.ConnectAction) error {
	if action.PeerID == "" {
		return fmt.Errorf("host %s is missing parsec peer_id", host.Name)
	}

	bin, err := LocateParsec()
	if err != nil {
		return err
	}

	// Format: parsecd peer_id=ID[:settings]
	arg := fmt.Sprintf("peer_id=%s", action.PeerID)
	if action.Settings != "" {
		arg = fmt.Sprintf("peer_id=%s:%s", action.PeerID, action.Settings)
	}

	cmd := exec.CommandContext(ctx, bin, arg)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	cmd.Stdin = r.Stdin
	return cmd.Start()
}

func (r *Runner) connectMount(ctx context.Context, host *config.HostConfig, action *config.ConnectAction) error {
	url := action.URL
	if url == "" {
		url = action.Run
	}
	if url == "" {
		return fmt.Errorf("mount action requires a url")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", url)
	case "windows":
		cmd = exec.CommandContext(ctx, "explorer", url)
	default: // Linux
		cmd = exec.CommandContext(ctx, "xdg-open", url)
	}

	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	return cmd.Run()
}

func (r *Runner) connectGame(ctx context.Context, host *config.HostConfig, action *config.ConnectAction) error {
	url := action.URL
	if url == "" {
		url = action.Run
	}
	if url == "" {
		return fmt.Errorf("game action requires a url")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", url)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url)
	default: // Linux
		cmd = exec.CommandContext(ctx, "xdg-open", url)
	}

	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	return cmd.Run()
}

func (r *Runner) connectCustom(ctx context.Context, host *config.HostConfig, action *config.ConnectAction) error {
	if action.Run == "" {
		return fmt.Errorf("custom connect action requires run command")
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", action.Run)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", action.Run)
	}

	// Expose host environment variables to the command
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("WAKER_HOST_NAME=%s", host.Name),
		fmt.Sprintf("WAKER_HOST_IP=%s", host.IP),
		fmt.Sprintf("WAKER_HOST_MAC=%s", host.MAC),
	)

	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	cmd.Stdin = r.Stdin
	return cmd.Run()
}
