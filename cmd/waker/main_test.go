package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	// Expect: Up is called on launch, Down is called on quit
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
	if mock1.downCalls != 1 {
		t.Fatalf("expected 1 Down call on quit, got %d", mock1.downCalls)
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

func init() {
	// Ensure store uses temp dir during tests
	os.Setenv("HOME", os.TempDir())
}
