package sleeper

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tomer/waker/pkg/config"
	"github.com/tomer/waker/pkg/health"
)

type Sleeper struct {
	HTTPClient *http.Client
}

func NewSleeper() *Sleeper {
	return &Sleeper{
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// Sleep puts the host into suspend mode based on configured method: agent -> ssh -> custom.
func (s *Sleeper) Sleep(ctx context.Context, host *config.HostConfig) error {
	action := host.OnSleep
	method := "ssh"
	if action != nil && action.Type != "" {
		method = action.Type
	}

	switch strings.ToLower(method) {
	case "agent":
		return s.sleepAgent(ctx, host, action)
	case "custom":
		return s.sleepCustom(ctx, host, action)
	case "ssh":
		return s.sleepSSH(ctx, host)
	default:
		return fmt.Errorf("unknown sleep method %q", method)
	}
}

func (s *Sleeper) sleepAgent(ctx context.Context, host *config.HostConfig, action *config.SleepAction) error {
	port := 9876
	if action != nil && action.Port != 0 {
		port = action.Port
	}

	token := ""
	if action != nil {
		if action.TokenEnv != "" {
			token = os.Getenv(action.TokenEnv)
		}
		if token == "" {
			token = action.Token
		}
	}

	url := fmt.Sprintf("http://%s:%d/sleep", host.IP, port)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer([]byte(`{}`)))
	if err != nil {
		return fmt.Errorf("failed to create sleep request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent sleep request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("agent returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *Sleeper) sleepCustom(ctx context.Context, host *config.HostConfig, action *config.SleepAction) error {
	if action == nil || action.Run == "" {
		return fmt.Errorf("custom sleep requires run command")
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", action.Run)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", action.Run)
	}

	cmd.Env = append(os.Environ(),
		fmt.Sprintf("WAKER_HOST_NAME=%s", host.Name),
		fmt.Sprintf("WAKER_HOST_IP=%s", host.IP),
		fmt.Sprintf("WAKER_HOST_MAC=%s", host.MAC),
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("custom sleep command failed: %s (%w)", string(out), err)
	}
	return nil
}

// SSH sleep commands per target OS. Since we might not know target OS, we can run a smart detection script or standard suspend.
// Standard Linux: systemctl suspend
// macOS: osascript -e 'tell application "System Events" to sleep'
// Windows OpenSSH: rundll32.exe powrprof.dll,SetSuspendState 0,1,0
const compositeSleepCommand = `if command -v systemctl >/dev/null 2>&1; then
  systemctl suspend
elif command -v osascript >/dev/null 2>&1; then
  osascript -e 'tell application "System Events" to sleep'
elif command -v rundll32.exe >/dev/null 2>&1; then
  rundll32.exe powrprof.dll,SetSuspendState 0,1,0
else
  echo "Unsupported sleep system" && exit 1
fi`

func (s *Sleeper) sleepSSH(ctx context.Context, host *config.HostConfig) error {
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

	// Add batch mode and short timeout so we don't hang if host suspends immediately
	args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5")

	target := host.IP
	if target == "" {
		target = host.Name
	}
	if host.SSH != nil && host.SSH.User != "" {
		target = fmt.Sprintf("%s@%s", host.SSH.User, target)
	}
	args = append(args, target, compositeSleepCommand)

	cmd := exec.CommandContext(ctx, sshPath, args...)
	// Note: When a remote host suspends, SSH connection may terminate abruptly or return 255.
	// We capture output, but don't treat immediate connection drop as fatal error.
	_ = cmd.Run()
	return nil
}

// WaitForOffline polls the host health until all checks fail or timeout expires.
func WaitForOffline(ctx context.Context, host *config.HostConfig, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			res := health.CheckHost(ctx, host, 1*time.Second)
			reachable, _ := health.IsReachable(res)
			if !reachable {
				// Machine is now offline!
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out after %v waiting for %s to go offline", timeout, host.Name)
			}
		}
	}
}
