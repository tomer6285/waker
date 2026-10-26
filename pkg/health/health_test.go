package health

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/tomer/waker/pkg/config"
)

func TestTCPCheck(t *testing.T) {
	// Start a dummy TCP server
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port

	host := &config.HostConfig{
		Name: "test-local",
		IP:   "127.0.0.1",
		Checks: []config.CheckConfig{
			{Type: "tcp", Port: port},
		},
	}

	results := CheckHost(context.Background(), host, 2*time.Second)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Success {
		t.Fatalf("expected TCP check to succeed, error: %s", results[0].Error)
	}

	reachable, lat := IsReachable(results)
	if !reachable {
		t.Fatal("expected reachable true")
	}
	if lat <= 0 {
		t.Errorf("expected positive latency, got %v", lat)
	}
}

func TestRoutedHostIsProbedUnlessLocalGuardExplicitlyEnabled(t *testing.T) {
	restore := SetLocalSubnetCheckerForTesting(func(string) SubnetMatchResult {
		return SubnetMatchResult{IsLocalTarget: true, IsMatched: false, Reason: "no matching local interface"}
	})
	defer restore()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	h := &config.HostConfig{IP: "127.0.0.1", Checks: []config.CheckConfig{{Type: "tcp", Port: l.Addr().(*net.TCPAddr).Port}}}
	res := CheckHost(context.Background(), h, time.Second)
	if ok, _ := IsReachable(res); !ok {
		t.Fatalf("default guard rejected a routed host: %+v", res)
	}
	enabled := true
	h.SubnetCheck = &enabled
	res = CheckHost(context.Background(), h, time.Second)
	if len(res) != 1 || res[0].Type != "subnet" || res[0].Success {
		t.Fatalf("explicit local guard not respected: %+v", res)
	}
}

func TestSSHReadinessUsesSSHPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	h := &config.HostConfig{IP: "127.0.0.1", SSH: &config.SSHConfig{Port: port}, Checks: []config.CheckConfig{{Type: "tcp", Port: port}}}
	res := CheckReady(context.Background(), h, time.Second)
	if ok, _ := IsReachable(res); !ok {
		t.Fatalf("SSH should be ready: %+v", res)
	}
	l.Close()
	// Presence can succeed on a different service while SSH is still starting.
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	h.Checks[0].Port = other.Addr().(*net.TCPAddr).Port
	if ok, _ := IsReachable(CheckHost(context.Background(), h, time.Second)); !ok {
		t.Fatal("expected online presence")
	}
	if ok, _ := IsReachable(CheckReady(context.Background(), h, time.Second)); ok {
		t.Fatal("SSH readiness should fail while its port is closed")
	}
}

// Each ping waits for all three processes to start. Sequential execution would
// time out the first checks, so success verifies overlap without timing thresholds.
func TestHostChecksRunConcurrently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX ping test double")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nmkdir \"$WAKER_PROBE_BARRIER/$$\"\nwhile :; do\n set -- \"$WAKER_PROBE_BARRIER\"/*\n [ \"$#\" -ge 3 ] && exit 0\n sleep 0.01\ndone\n"
	if err := os.WriteFile(filepath.Join(dir, "ping"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Keep barrier entries separate from the executable.
	barrier := filepath.Join(dir, "barrier")
	if err := os.Mkdir(barrier, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WAKER_PROBE_BARRIER", barrier)
	host := &config.HostConfig{IP: "127.0.0.1", Checks: []config.CheckConfig{{Type: "ping"}, {Type: "ping"}, {Type: "ping"}, {Type: "unsupported"}}}
	results := CheckHost(context.Background(), host, 2*time.Second)
	if len(results) != 4 {
		t.Fatalf("missing results: %+v", results)
	}
	for i := 0; i < 3; i++ {
		if !results[i].Success {
			t.Fatalf("checks did not overlap: %+v", results)
		}
	}
	if results[3].Type != "unsupported" || results[3].Success {
		t.Fatalf("check order or errors lost: %+v", results)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, result := range CheckHost(ctx, host, time.Second) {
		if result.Success {
			t.Fatal("canceled check succeeded")
		}
	}
}
