package health

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tomer/waker/pkg/config"
)

type CheckResult struct {
	Type    string        `json:"type"`
	Target  string        `json:"target"`
	Success bool          `json:"success"`
	Latency time.Duration `json:"latency"`
	Error   string        `json:"error,omitempty"`
}

// CheckHost probes the configured checks for a host and returns individual check results.
// If any check passes, the host is considered reachable.
func CheckHost(ctx context.Context, host *config.HostConfig, timeout time.Duration) []CheckResult {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	checks := host.Checks
	if len(checks) == 0 && host.IP != "" {
		checks = []config.CheckConfig{{Type: "ping"}}
		if host.SSH != nil {
			p := 22
			if host.SSH.Port != 0 {
				p = host.SSH.Port
			}
			checks = append(checks, config.CheckConfig{Type: "tcp", Port: p})
		}
	}

	if len(checks) == 0 {
		// Nothing to check against
		return nil
	}

	results := make([]CheckResult, len(checks))
	for i, c := range checks {
		ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
		res := runSingleCheck(ctxTimeout, host, c)
		cancel()
		results[i] = res
	}

	return results
}

// IsReachable returns true if any check succeeded.
func IsReachable(results []CheckResult) (bool, time.Duration) {
	var minLatency time.Duration
	found := false
	for _, r := range results {
		if r.Success {
			if !found || r.Latency < minLatency {
				minLatency = r.Latency
				found = true
			}
		}
	}
	return found, minLatency
}

func runSingleCheck(ctx context.Context, host *config.HostConfig, c config.CheckConfig) CheckResult {
	start := time.Now()
	res := CheckResult{
		Type:   c.Type,
		Target: host.IP,
	}

	if host.IP == "" && c.Type != "arp" {
		res.Error = "no IP address configured"
		return res
	}

	switch strings.ToLower(c.Type) {
	case "tcp":
		port := c.Port
		if port == 0 {
			port = 22
		}
		target := net.JoinHostPort(host.IP, strconv.Itoa(port))
		res.Target = target

		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", target)
		res.Latency = time.Since(start)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		_ = conn.Close()
		res.Success = true
		return res

	case "ping":
		// Ping via system ping command
		cmd := pingCommand(ctx, host.IP)
		out, err := cmd.CombinedOutput()
		res.Latency = time.Since(start)
		if err != nil {
			res.Error = strings.TrimSpace(string(out))
			if res.Error == "" {
				res.Error = err.Error()
			}
			return res
		}
		res.Success = true
		return res

	case "arp":
		res.Target = host.MAC
		found, err := checkARPCache(ctx, host.MAC, host.IP)
		res.Latency = time.Since(start)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Success = found
		if !found {
			res.Error = "not found in ARP cache"
		}
		return res

	default:
		res.Error = fmt.Sprintf("unsupported check type %s", c.Type)
		return res
	}
}

func pingCommand(ctx context.Context, ip string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		// -c 1 (1 packet), -W 1000 (1000ms wait)
		return exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1000", ip)
	case "windows":
		// -n 1, -w 1000
		return exec.CommandContext(ctx, "ping", "-n", "1", "-w", "1000", ip)
	default: // linux, etc.
		// -c 1, -W 1
		return exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", ip)
	}
}

func checkARPCache(ctx context.Context, mac, ip string) (bool, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "linux" {
		cmd = exec.CommandContext(ctx, "ip", "neigh")
	} else {
		cmd = exec.CommandContext(ctx, "arp", "-a")
	}

	out, err := cmd.Output()
	if err != nil {
		return false, err
	}

	content := strings.ToLower(string(out))
	macNorm := strings.ToLower(mac)
	if strings.Contains(content, macNorm) {
		return true, nil
	}
	if ip != "" && strings.Contains(content, ip) {
		return true, nil
	}
	return false, nil
}
