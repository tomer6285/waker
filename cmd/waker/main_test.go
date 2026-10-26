package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tomer/waker/pkg/config"
	"github.com/tomer/waker/pkg/sleeper"
	"github.com/tomer/waker/pkg/tailscale"
	"github.com/tomer/waker/pkg/wol"
)

// TestWakeWaitConnectPipeline simulates the entire pipeline:
// 1. Host is initially offline (no TCP port open).
// 2. WOL magic packet is sent to dummy UDP listener.
// 3. After 1.5 seconds, dummy TCP server opens simulating the machine waking up.
// 4. waitForHostOnline detects host is up.
// 5. Connect executes action.
func TestWakeWaitConnectPipeline(t *testing.T) {
	// 1. UDP server receives WOL
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP failed: %v", err)
	}
	defer udpConn.Close()
	wolPort := udpConn.LocalAddr().(*net.UDPAddr).Port

	// Reserve a local TCP port that is initially CLOSED
	lInit, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen error: %v", err)
	}
	tcpPort := lInit.Addr().(*net.TCPAddr).Port
	lInit.Close() // closed immediately so port is offline

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "hosts.yaml")
	cfg := &config.Config{
		Version: 1,
		Defaults: config.DefaultsConfig{
			Timeout:      5 * time.Second,
			ProbeTimeout: 500 * time.Millisecond,
		},
		Hosts: []config.HostConfig{
			{
				Name:      "test-desktop",
				MAC:       "AA:BB:CC:DD:EE:FF",
				IP:        "127.0.0.1",
				Broadcast: "127.0.0.1",
				Port:      wolPort,
				Checks: []config.CheckConfig{
					{Type: "tcp", Port: tcpPort},
				},
				OnConnect: &config.ConnectAction{
					Type: "custom",
					Run:  "echo online_ok",
				},
			},
		},
	}
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("cfg.Save failed: %v", err)
	}

	// 2. Launch background routine to open TCP port after 1 second (simulating host waking up)
	go func() {
		time.Sleep(1 * time.Second)
		listener, _ := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", tcpPort))
		if listener != nil {
			defer listener.Close()
			time.Sleep(5 * time.Second)
		}
	}()

	// 3. Send WOL packet
	err = wol.Send("AA:BB:CC:DD:EE:FF", wol.Options{
		BroadcastIP: "127.0.0.1",
		Port:        wolPort,
	})
	if err != nil {
		t.Fatalf("wol.Send failed: %v", err)
	}

	// Read UDP packet to verify WOL was received
	buf := make([]byte, 256)
	_ = udpConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := udpConn.ReadFrom(buf)
	if err != nil || n != 102 {
		t.Fatalf("failed to receive valid 102-byte WOL packet: n=%d err=%v", n, err)
	}

	// 4. Test waitForHostOnline
	host, _ := cfg.FindHost("test-desktop")
	err = waitForHostOnline(context.Background(), cfg, host, 4*time.Second)
	if err != nil {
		t.Fatalf("waitForHostOnline failed: %v", err)
	}
}

// TestSleepWaitOfflinePipeline tests sleeper and wait for offline
func TestSleepWaitOfflinePipeline(t *testing.T) {
	// TCP server simulating online host
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port

	host := &config.HostConfig{
		Name: "sleeping-host",
		IP:   "127.0.0.1",
		Checks: []config.CheckConfig{
			{Type: "tcp", Port: port},
		},
		OnSleep: &config.SleepAction{
			Type: "custom",
			Run:  "true",
		},
	}

	sl := sleeper.NewSleeper()
	err = sl.Sleep(context.Background(), host)
	if err != nil {
		t.Fatalf("sl.Sleep failed: %v", err)
	}

	// Close listener to simulate host suspending
	l.Close()

	err = sleeper.WaitForOffline(context.Background(), host, 3*time.Second)
	if err != nil {
		t.Fatalf("WaitForOffline failed: %v", err)
	}
}

type mockTSClient struct {
	isUp      bool
	upCalls   int
	downCalls int
}

func (m *mockTSClient) IsUp(ctx context.Context) (bool, error) {
	return m.isUp, nil
}
func (m *mockTSClient) Up(ctx context.Context) error {
	m.upCalls++
	m.isUp = true
	return nil
}
func (m *mockTSClient) Down(ctx context.Context) error {
	m.downCalls++
	m.isUp = false
	return nil
}
func (m *mockTSClient) Status(ctx context.Context) (*tailscale.Status, error) {
	return &tailscale.Status{Installed: true, IsUp: m.isUp}, nil
}

func TestTailscaleLaunchAndQuitLifecycle(t *testing.T) {
	// Case 1: auto_tailscale is true and Tailscale is down on launch
	// Expect: Up is called on launch, Down is NOT called on quit
	mock1 := &mockTSClient{isUp: false}
	mgr1 := tailscale.NewManagerWithClient(mock1)

	started, err := mgr1.OnLaunch(context.Background(), true)
	if err != nil || !started {
		t.Fatalf("expected started=true, err=nil; got started=%v err=%v", started, err)
	}
	if mock1.upCalls != 1 {
		t.Fatalf("expected 1 Up call, got %d", mock1.upCalls)
	}
	if err := mgr1.OnQuit(context.Background(), true); err != nil {
		t.Fatalf("quit error: %v", err)
	}
	if mock1.downCalls != 0 {
		t.Fatalf("expected no automatic Down call on quit, got %d", mock1.downCalls)
	}

	// Case 2: auto_tailscale is true and Tailscale is ALREADY up on launch
	// Expect: Up is NOT called, Down is NOT called on quit
	mock2 := &mockTSClient{isUp: true}
	mgr2 := tailscale.NewManagerWithClient(mock2)

	started, err = mgr2.OnLaunch(context.Background(), true)
	if err != nil || started {
		t.Fatalf("expected started=false, err=nil; got started=%v err=%v", started, err)
	}
	if mock2.upCalls != 0 {
		t.Fatalf("expected 0 Up calls, got %d", mock2.upCalls)
	}
	if err := mgr2.OnQuit(context.Background(), true); err != nil {
		t.Fatalf("quit error: %v", err)
	}
	if mock2.downCalls != 0 {
		t.Fatalf("expected 0 Down calls on quit because Tailscale was already running, got %d", mock2.downCalls)
	}
}

func TestEveryNetworkCommandEnsuresTailscaleAndLeavesItConnected(t *testing.T) {
	oldFactory, oldCfg := newTailscaleManager, cfgFile
	t.Cleanup(func() { newTailscaleManager = oldFactory; cfgFile = oldCfg })
	cfgFile = filepath.Join(t.TempDir(), "hosts.yaml")
	cfg := &config.Config{Version: 1, Settings: config.SettingsConfig{AutoTailscale: true}}
	if err := cfg.Save(cfgFile); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"waker", "list", "status", "wake", "connect", "sleep", "ping", "add"} {
		t.Run(name, func(t *testing.T) {
			mock := &mockTSClient{}
			newTailscaleManager = func() *tailscale.Manager { return tailscale.NewManagerWithClient(mock) }
			cmd := &cobra.Command{Use: name}
			cmd.SetContext(context.Background())
			if _, err := loadCommandConfig(cmd); err != nil {
				t.Fatal(err)
			}
			want := 1
			if name == "add" {
				want = 0
			}
			if mock.upCalls != want || mock.downCalls != 0 {
				t.Fatalf("up=%d down=%d", mock.upCalls, mock.downCalls)
			}
		})
	}
}

func TestTailscaleStartupErrorStopsCommand(t *testing.T) {
	oldFactory, oldCfg := newTailscaleManager, cfgFile
	t.Cleanup(func() { newTailscaleManager = oldFactory; cfgFile = oldCfg })
	cfgFile = filepath.Join(t.TempDir(), "hosts.yaml")
	cfg := &config.Config{Version: 1, Settings: config.SettingsConfig{AutoTailscale: true}}
	if err := cfg.Save(cfgFile); err != nil {
		t.Fatal(err)
	}
	newTailscaleManager = func() *tailscale.Manager { return tailscale.NewManagerWithClient(&failingTSClient{}) }
	cmd := &cobra.Command{Use: "wake"}
	cmd.SetContext(context.Background())
	if _, err := loadCommandConfig(cmd); err == nil || !strings.Contains(err.Error(), "Tailscale startup failed") {
		t.Fatalf("startup failure lost: %v", err)
	}
}

type failingTSClient struct{ mockTSClient }

func (*failingTSClient) IsUp(context.Context) (bool, error) {
	return false, errors.New("daemon unavailable")
}

func TestConnectThroughRelayThenSkipsRelayWhenAwake(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test double")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3 not installed")
	}
	dir := t.TempDir()
	oldCfg := cfgFile
	t.Cleanup(func() { cfgFile = oldCfg })
	cfgFile = filepath.Join(dir, "hosts.yaml")
	marker := filepath.Join(dir, "connected")
	t.Setenv("WAKER_TEST_CONNECTED", marker)
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nfor arg do remote=$arg; done\nexec sh -c \"$remote\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reserved.Addr().String()
	port := reserved.Addr().(*net.TCPAddr).Port
	reserved.Close()
	cfg := &config.Config{Version: 1, Defaults: config.DefaultsConfig{Timeout: 5 * time.Second, ProbeTimeout: 100 * time.Millisecond}, Hosts: []config.HostConfig{{
		Name: "desktop", MAC: "AA:BB:CC:DD:EE:FF", IP: "127.0.0.1", Broadcast: "127.0.0.1",
		Port: udp.LocalAddr().(*net.UDPAddr).Port, Relay: &wol.RelayConfig{Host: "home-pi"},
		Checks:    []config.CheckConfig{{Type: "tcp", Port: port}},
		OnConnect: &config.ConnectAction{Type: "custom", Run: `printf connected > "$WAKER_TEST_CONNECTED"`},
	}}}
	if err := cfg.Save(cfgFile); err != nil {
		t.Fatal(err)
	}
	packet := make(chan []byte, 1)
	ready := make(chan net.Listener, 1)
	go func() {
		udp.SetReadDeadline(time.Now().Add(4 * time.Second))
		buf := make([]byte, 256)
		n, _, err := udp.ReadFromUDP(buf)
		if err != nil {
			packet <- nil
			ready <- nil
			return
		}
		packet <- buf[:n]
		l, _ := net.Listen("tcp", addr)
		ready <- l
	}()
	cmd := &cobra.Command{Use: "connect"}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("no-wait", false, "")
	cmd.Flags().Bool("no-wake", false, "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Duration("timeout", 5*time.Second, "")
	if err := connectCmd.RunE(cmd, []string{"desktop"}); err != nil {
		t.Fatal(err)
	}
	l := <-ready
	if l == nil {
		t.Fatal("target never woke")
	}
	defer l.Close()
	got := <-packet
	mac, _ := wol.NormalizeMAC(cfg.Hosts[0].MAC)
	want, _ := wol.BuildMagicPacket(mac)
	if !bytes.Equal(got, want) {
		t.Fatalf("invalid relay packet: %x", got)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "connected" {
		t.Fatalf("connect action not executed: %q %v", got, err)
	}
	// A reachable host must connect even when the relay subsequently fails.
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := connectCmd.RunE(cmd, []string{"desktop"}); err != nil {
		t.Fatalf("online target depended on relay: %v", err)
	}
}
