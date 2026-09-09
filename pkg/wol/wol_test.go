package wol

import (
	"bytes"
	"net"
	"testing"
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

