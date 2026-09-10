package health

import (
	"net"
	"testing"
)

func TestIsTailscaleIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"100.64.0.1", true},
		{"100.100.50.25", true},
		{"100.127.255.254", true},
		{"100.128.0.1", false},
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"127.0.0.1", false},
		{"8.8.8.8", false},
		{"fd7a:115c:a1e0::1", true},
		{"2607:f8b0:4005:805::200e", false},
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		got := IsTailscaleIP(ip)
		if got != tt.want {
			t.Errorf("IsTailscaleIP(%s) = %v; want %v", tt.ip, got, tt.want)
		}
	}

	if IsTailscaleIP(nil) {
		t.Errorf("IsTailscaleIP(nil) should be false")
	}
}

func TestIsLocalOrPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"192.168.1.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"169.254.1.1", true},
		{"100.100.50.25", true}, // Tailscale
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"142.250.190.46", false},
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		got := IsLocalOrPrivateIP(ip)
		if got != tt.want {
			t.Errorf("IsLocalOrPrivateIP(%s) = %v; want %v", tt.ip, got, tt.want)
		}
	}

	if IsLocalOrPrivateIP(nil) {
		t.Errorf("IsLocalOrPrivateIP(nil) should be false")
	}
}

func TestCheckLocalSubnetWithInterfaces(t *testing.T) {
	_, subnet24, _ := net.ParseCIDR("192.168.1.100/24")
	_, loopbackNet, _ := net.ParseCIDR("127.0.0.1/8")
	_, tsNet, _ := net.ParseCIDR("100.80.90.10/32")
	_, downNet, _ := net.ParseCIDR("10.0.0.1/24")

	mockIfaces := []net.Interface{
		{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback},
		{Name: "en0", Flags: net.FlagUp | net.FlagBroadcast},
		{Name: "utun4", Flags: net.FlagUp | net.FlagPointToPoint},
		{Name: "en1", Flags: 0}, // Down
	}

	mockAddrs := map[string][]net.Addr{
		"lo0":   {loopbackNet},
		"en0":   {subnet24},
		"utun4": {tsNet},
		"en1":   {downNet},
	}

	getIfaces := func() ([]net.Interface, error) {
		return mockIfaces, nil
	}
	getAddrs := func(iface *net.Interface) ([]net.Addr, error) {
		return mockAddrs[iface.Name], nil
	}

	// 1. Loopback target
	res := CheckLocalSubnetWithInterfaces("127.0.0.1", getIfaces, getAddrs)
	if !res.IsMatched || res.MatchedIface != "lo0" {
		t.Errorf("expected 127.0.0.1 to match lo0, got %+v", res)
	}

	// 2. Same LAN target
	res = CheckLocalSubnetWithInterfaces("192.168.1.50", getIfaces, getAddrs)
	if !res.IsMatched || res.MatchedIface != "en0" {
		t.Errorf("expected 192.168.1.50 to match en0, got %+v", res)
	}

	// 3. Different LAN target (not on local network)
	res = CheckLocalSubnetWithInterfaces("192.168.2.50", getIfaces, getAddrs)
	if res.IsMatched {
		t.Errorf("expected 192.168.2.50 to NOT match, got %+v", res)
	}

	// 4. Down interface should not match
	res = CheckLocalSubnetWithInterfaces("10.0.0.50", getIfaces, getAddrs)
	if res.IsMatched {
		t.Errorf("expected 10.0.0.50 (on down interface) to NOT match, got %+v", res)
	}

	// 5. Tailscale target with Tailscale interface up
	res = CheckLocalSubnetWithInterfaces("100.100.200.2", getIfaces, getAddrs)
	if !res.IsMatched || res.MatchedIface != "utun4" {
		t.Errorf("expected Tailscale target to match utun4, got %+v", res)
	}

	// 6. Tailscale target without Tailscale interface up
	getNoTailscaleIfaces := func() ([]net.Interface, error) {
		return []net.Interface{
			{Name: "en0", Flags: net.FlagUp | net.FlagBroadcast},
		}, nil
	}
	res = CheckLocalSubnetWithInterfaces("100.100.200.2", getNoTailscaleIfaces, getAddrs)
	if res.IsMatched {
		t.Errorf("expected Tailscale target without Tailscale active to NOT match, got %+v", res)
	}

	// 7. Public routable IP
	res = CheckLocalSubnetWithInterfaces("8.8.8.8", getIfaces, getAddrs)
	if !res.IsMatched || res.IsLocalTarget {
		t.Errorf("expected public IP to be matched and not local target, got %+v", res)
	}
}

func TestIsSubnetUnreachable(t *testing.T) {
	// Subnet failure
	res1 := []CheckResult{
		{Type: "subnet", Success: false, Error: "not on local LAN"},
	}
	if !IsSubnetUnreachable(res1) {
		t.Errorf("expected res1 to be unreachable")
	}

	// Route error failures
	res2 := []CheckResult{
		{Type: "tcp", Success: false, Error: "dial tcp 10.0.0.1:22: connect: network is unreachable"},
	}
	if !IsSubnetUnreachable(res2) {
		t.Errorf("expected res2 to be unreachable")
	}

	// Host offline (connection refused / timeout)
	res3 := []CheckResult{
		{Type: "tcp", Success: false, Error: "connection refused"},
	}
	if IsSubnetUnreachable(res3) {
		t.Errorf("expected connection refused to not be categorized as subnet unreachable")
	}

	// Success check
	res4 := []CheckResult{
		{Type: "tcp", Success: true},
	}
	if IsSubnetUnreachable(res4) {
		t.Errorf("expected successful check to not be unreachable")
	}
}
