package wol

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		wantErr  bool
	}{
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff", false},
		{"aa-bb-cc-dd-ee-ff", "aa:bb:cc:dd:ee:ff", false},
		{"AABBCCDDEEFF", "aa:bb:cc:dd:ee:ff", false},
		{"001122334455", "00:11:22:33:44:55", false},
		{"invalid", "", true},
		{"AA:BB:CC:DD:EE", "", true},
	}

	for _, tt := range tests {
		hw, err := NormalizeMAC(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("NormalizeMAC(%q) expected error, got nil", tt.input)
			}
		} else {
			if err != nil {
				t.Errorf("NormalizeMAC(%q) unexpected error: %v", tt.input, err)
			} else if hw.String() != tt.expected {
				t.Errorf("NormalizeMAC(%q) = %q, want %q", tt.input, hw.String(), tt.expected)
			}
		}
	}
}

func TestBuildMagicPacket(t *testing.T) {
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	packet, err := BuildMagicPacket(mac)
	if err != nil {
		t.Fatalf("BuildMagicPacket failed: %v", err)
	}

	if len(packet) != 102 {
		t.Fatalf("expected packet length 102, got %d", len(packet))
	}

	for i := 0; i < 6; i++ {
		if packet[i] != 0xFF {
			t.Errorf("byte %d should be 0xFF, got 0x%02x", i, packet[i])
		}
	}

	for i := 0; i < 16; i++ {
		chunk := packet[6+i*6 : 6+(i+1)*6]
		if !bytes.Equal(chunk, mac) {
			t.Errorf("chunk %d does not match MAC: %v vs %v", i, chunk, mac)
		}
	}
}

func TestSendUDP(t *testing.T) {
	// Start a dummy UDP listener on localhost
	addr, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to resolve UDP: %v", err)
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		t.Fatalf("ListenUDP failed: %v", err)
	}
	defer conn.Close()

	port := conn.LocalAddr().(*net.UDPAddr).Port

	macStr := "AA:BB:CC:DD:EE:FF"
	err = Send(macStr, Options{
		BroadcastIP: "127.0.0.1",
		Port:        port,
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	buf := make([]byte, 1024)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read from dummy UDP server failed: %v", err)
	}

	if n != 102 {
		t.Fatalf("expected 102 bytes received, got %d", n)
	}
}

func TestParseARPOutput(t *testing.T) {
	sampleDarwin := `
? (10.40.32.1) at 94:24:e1:ea:74:8d on en0 ifscope [ethernet]
? (192.168.1.50) at de:ad:be:ef:00:01 on en0 ifscope [ethernet]
`
	mac, err := parseARPOutput(sampleDarwin, "192.168.1.50")
	if err != nil {
		t.Fatalf("parseARPOutput failed: %v", err)
	}
	if mac != "de:ad:be:ef:00:01" {
		t.Errorf("expected de:ad:be:ef:00:01, got %s", mac)
	}

	sampleLinux := `
192.168.1.50 dev eth0 lladdr aa:bb:cc:dd:ee:ff REACHABLE
`
	mac2, err := parseARPOutput(sampleLinux, "192.168.1.50")
	if err != nil {
		t.Fatalf("parseARPOutput linux failed: %v", err)
	}
	if mac2 != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected aa:bb:cc:dd:ee:ff, got %s", mac2)
	}

	_, err = parseARPOutput(sampleDarwin, "10.0.0.99")
	if err == nil {
		t.Fatal("expected error for missing IP, got nil")
	}
}

// Run the exact stdin/remote command used by SSH, without needing an SSH daemon.
func TestSSHRelayDeliversPacket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX SSH relay test double")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3 not installed")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	t.Setenv("WAKER_TEST_SSH_ARGS", argsFile)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$WAKER_TEST_SSH_ARGS\"\nfor arg do remote=$arg; done\nexec sh -c \"$remote\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	relay := RelayConfig{Host: "home-pi", User: "pi", Port: 2222, SourceIP: "127.0.0.1", Args: []string{"-i", "/path with spaces/key"}}
	err = SendWithRelay(context.Background(), &relay, "AA:BB:CC:DD:EE:FF", Options{BroadcastIP: "127.0.0.1", Port: conn.LocalAddr().(*net.UDPAddr).Port})
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 256)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	mac, _ := NormalizeMAC("AA:BB:CC:DD:EE:FF")
	want, _ := BuildMagicPacket(mac)
	if !bytes.Equal(buf[:n], want) {
		t.Fatalf("invalid magic packet: %x", buf[:n])
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "-i\n/path with spaces/key\n-p\n2222\n--\npi@home-pi\npython3 -\n") {
		t.Fatalf("unexpected SSH args: %s", args)
	}
}

func TestSSHRelayRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		relay RelayConfig
		opts  Options
	}{
		{"option host", RelayConfig{Host: "-oProxyCommand=bad"}, Options{}},
		{"bad user", RelayConfig{Host: "home", User: "bad\nuser"}, Options{}},
		{"invalid source", RelayConfig{Host: "home", SourceIP: "not-an-ip"}, Options{}},
		{"injected broadcast", RelayConfig{Host: "home"}, Options{BroadcastIP: "127.0.0.1'); exit(0)#"}},
		{"invalid port", RelayConfig{Host: "home"}, Options{Port: -1}},
		{"local interface", RelayConfig{Host: "home"}, Options{IfaceName: "en0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := SendViaSSHRelay(context.Background(), tc.relay, "AA:BB:CC:DD:EE:FF", tc.opts); err == nil {
				t.Fatal("expected validation error before SSH")
			}
		})
	}
}

func TestSSHRelayReportsFailureAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test double")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	err := SendViaSSHRelay(context.Background(), RelayConfig{Host: "home-pi"}, "AA:BB:CC:DD:EE:FF", Options{})
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("lost relay error: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := SendViaSSHRelay(ctx, RelayConfig{Host: "home-pi"}, "AA:BB:CC:DD:EE:FF", Options{}); err == nil {
		t.Fatal("expected cancellation error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("relay did not respect context cancellation")
	}
}
