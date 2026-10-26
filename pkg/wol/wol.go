package wol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// NormalizeMAC normalizes MAC strings of forms like AA:BB:CC:DD:EE:FF, AA-BB-CC-DD-EE-FF, or AABBCCDDEEFF.
func NormalizeMAC(raw string) (net.HardwareAddr, error) {
	cleaned := strings.TrimSpace(raw)
	// Try standard net.ParseMAC first (handles : and -)
	hw, err := net.ParseMAC(cleaned)
	if err == nil {
		if len(hw) != 6 {
			return nil, fmt.Errorf("invalid MAC length: expected 6 bytes (EUI-48), got %d", len(hw))
		}
		return hw, nil
	}

	// Try removing spaces, colons, hyphens, periods if plain hex
	s := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			return r
		}
		return -1
	}, cleaned)

	if len(s) == 12 {
		var parts []string
		for i := 0; i < 12; i += 2 {
			parts = append(parts, s[i:i+2])
		}
		return net.ParseMAC(strings.Join(parts, ":"))
	}

	return nil, fmt.Errorf("invalid MAC address %q: %w", raw, err)
}

// BuildMagicPacket constructs a 102-byte Wake-on-LAN magic packet.
// 6 bytes of 0xFF followed by 16 repetitions of the 6-byte target MAC address.
func BuildMagicPacket(mac net.HardwareAddr) ([]byte, error) {
	if len(mac) != 6 {
		return nil, errors.New("MAC address must be 6 bytes")
	}
	packet := make([]byte, 102)
	// 6 bytes of 0xFF
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}
	// 16 repetitions of MAC
	for i := 0; i < 16; i++ {
		copy(packet[6+i*6:6+(i+1)*6], mac)
	}
	return packet, nil
}

// Options configure sending a WOL packet.
type Options struct {
	BroadcastIP string
	Port        int
	IfaceName   string // Optional local network interface name
}

// Send sends a WOL magic packet to the specified target MAC address.
func Send(macStr string, opts Options) error {
	mac, err := NormalizeMAC(macStr)
	if err != nil {
		return err
	}

	packet, err := BuildMagicPacket(mac)
	if err != nil {
		return err
	}

	bcastIP := opts.BroadcastIP
	if bcastIP == "" {
		bcastIP = "255.255.255.255"
	}
	port := opts.Port
	if port <= 0 {
		port = 9
	}

	destAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", bcastIP, port))
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address %s:%d: %w", bcastIP, port, err)
	}

	var laddr *net.UDPAddr
	if opts.IfaceName != "" {
		ifi, err := net.InterfaceByName(opts.IfaceName)
		if err != nil {
			return fmt.Errorf("interface %s not found: %w", opts.IfaceName, err)
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			return fmt.Errorf("failed to get addrs for interface %s: %w", opts.IfaceName, err)
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					laddr = &net.UDPAddr{IP: ip4, Port: 0}
					break
				}
			}
		}
	}

	conn, err := net.DialUDP("udp4", laddr, destAddr)
	if err != nil {
		return fmt.Errorf("failed to dial UDP broadcast: %w", err)
	}
	defer conn.Close()

	n, err := conn.Write(packet)
	if err != nil {
		return fmt.Errorf("failed to send magic packet: %w", err)
	}
	if n != len(packet) {
		return fmt.Errorf("short write sending magic packet: %d/%d bytes", n, len(packet))
	}

	return nil
}

// RelayConfig specifies an always-on SSH host on the target's physical LAN.
// The relay needs a POSIX shell and Python 3; it does not run waker-agent.
type RelayConfig struct {
	Host     string   `yaml:"host"`
	User     string   `yaml:"user,omitempty"`
	Port     int      `yaml:"port,omitempty"`
	Args     []string `yaml:"args,omitempty"`
	SourceIP string   `yaml:"source_ip,omitempty"` // Relay's LAN IPv4 address, for multi-NIC hosts.
}

func (r RelayConfig) Validate() error {
	if strings.TrimSpace(r.Host) == "" || strings.HasPrefix(r.Host, "-") || strings.ContainsAny(r.Host, " \t\r\n/@") {
		return errors.New("relay host must be a hostname, SSH alias, or IP without a username")
	}
	if strings.HasPrefix(r.User, "-") || strings.ContainsAny(r.User, " \t\r\n/@") {
		return errors.New("invalid relay SSH user")
	}
	if r.Port < 0 || r.Port > 65535 {
		return errors.New("relay SSH port must be between 1 and 65535")
	}
	if r.SourceIP != "" && (net.ParseIP(r.SourceIP) == nil || net.ParseIP(r.SourceIP).To4() == nil) {
		return errors.New("relay source_ip must be the relay's LAN IPv4 address")
	}
	return nil
}

// SendViaSSHRelay sends Python over stdin, keeping configuration out of shell code.
// A timeout bounds the entire SSH operation, including authentication and remote execution.
func SendViaSSHRelay(ctx context.Context, relay RelayConfig, macStr string, opts Options) error {
	if err := relay.Validate(); err != nil {
		return err
	}
	mac, err := NormalizeMAC(macStr)
	if err != nil {
		return err
	}
	packet, err := BuildMagicPacket(mac)
	if err != nil {
		return err
	}
	bcastIP := opts.BroadcastIP
	if bcastIP == "" {
		bcastIP = "255.255.255.255"
	}
	ip := net.ParseIP(bcastIP)
	if ip == nil || ip.To4() == nil {
		return errors.New("relay broadcast must be an IPv4 address on the target LAN")
	}
	port := opts.Port
	if port == 0 {
		port = 9
	}
	if port < 1 || port > 65535 {
		return errors.New("WOL port must be between 1 and 65535")
	}
	if opts.IfaceName != "" {
		return errors.New("interface selects a local interface and cannot be used with a relay; use relay.source_ip to select the relay's LAN address")
	}
	payload, _ := json.Marshal(struct {
		Packet    string `json:"packet"`
		Broadcast string `json:"broadcast"`
		Port      int    `json:"port"`
		Source    string `json:"source"`
	}{base64.StdEncoding.EncodeToString(packet), ip.String(), port, relay.SourceIP})
	// JSON is loaded as a quoted Python string, never interpreted as executable code.
	quoted, _ := json.Marshal(string(payload))
	script := fmt.Sprintf(`import base64, json, socket
cfg = json.loads(%s)
packet = base64.b64decode(cfg["packet"])
with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
    sock.settimeout(5)
    if cfg["source"]:
        sock.bind((cfg["source"], 0))
    sent = sock.sendto(packet, (cfg["broadcast"], cfg["port"]))
    if sent != len(packet):
        raise RuntimeError("short write sending magic packet")
`, quoted)
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh executable not found: %w", err)
	}
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=1"}
	args = append(args, relay.Args...)
	if relay.Port != 0 {
		args = append(args, "-p", strconv.Itoa(relay.Port))
	}
	destination := relay.Host
	if relay.User != "" {
		destination = relay.User + "@" + destination
	}
	args = append(args, "--", destination, "python3 -")
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sshPath, args...)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fmt.Errorf("wake relay %s failed (check Tailscale, SSH key/host trust, Python 3, and LAN broadcast): %s: %w", destination, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// SendWithRelay sends a WOL magic packet either directly via local UDP broadcast,
// or via remote SSH relay if a RelayConfig is provided.
func SendWithRelay(ctx context.Context, relay *RelayConfig, macStr string, opts Options) error {
	if relay != nil && strings.TrimSpace(relay.Host) != "" {
		return SendViaSSHRelay(ctx, *relay, macStr, opts)
	}
	return Send(macStr, opts)
}

// ResolveMACFromIP attempts to look up the hardware MAC address for a given IP address
// using the OS ARP cache. It sends a quick ping probe to ensure the ARP table is populated.
func ResolveMACFromIP(ipStr string) (string, error) {
	trimmed := strings.TrimSpace(ipStr)
	ip := net.ParseIP(trimmed)
	if ip == nil {
		ips, err := net.LookupIP(trimmed)
		if err == nil && len(ips) > 0 {
			for _, candidate := range ips {
				if candidate.To4() != nil {
					ip = candidate
					break
				}
			}
			if ip == nil {
				ip = ips[0]
			}
		}
	}
	if ip == nil {
		return "", fmt.Errorf("invalid IP or unresolvable hostname: %s", ipStr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	// 1. Send quick ping probe to populate ARP cache if host is up
	var pingCmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		pingCmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "500", ip.String())
	case "windows":
		pingCmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", "500", ip.String())
	default: // linux, etc.
		pingCmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", ip.String())
	}
	_ = pingCmd.Run()

	// 2. Query ARP table
	var arpCmd *exec.Cmd
	if runtime.GOOS == "linux" {
		arpCmd = exec.Command("ip", "neigh")
	} else {
		arpCmd = exec.Command("arp", "-a")
	}

	out, err := arpCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to query ARP cache: %w", err)
	}

	return parseARPOutput(string(out), ip.String())
}

func parseARPOutput(output, targetIP string) (string, error) {
	lines := strings.Split(output, "\n")
	macRegex := regexp.MustCompile(`(?i)([0-9a-f]{1,2}[:-][0-9a-f]{1,2}[:-][0-9a-f]{1,2}[:-][0-9a-f]{1,2}[:-][0-9a-f]{1,2}[:-][0-9a-f]{1,2})`)

	for _, line := range lines {
		lineLower := strings.ToLower(line)
		// Look for target IP in line (handles "192.168.1.10", "(192.168.1.10)", etc.)
		if strings.Contains(lineLower, targetIP) {
			match := macRegex.FindString(line)
			if match != "" {
				hw, err := NormalizeMAC(match)
				if err == nil {
					return hw.String(), nil
				}
			}
		}
	}

	return "", fmt.Errorf("MAC address for %s not found in ARP cache (is the host online?)", targetIP)
}
