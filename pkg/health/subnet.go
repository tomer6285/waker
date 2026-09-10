package health

import (
	"fmt"
	"net"
	"strings"
)

var (
	_, tailscaleIPv4Net, _ = net.ParseCIDR("100.64.0.0/10")
	_, tailscaleIPv6Net, _ = net.ParseCIDR("fd7a:115c:a1e0::/48")
)

// SubnetMatchResult describes whether an IP is considered on a local network.
type SubnetMatchResult struct {
	TargetIP      net.IP
	IsLocalTarget bool       // true if target is private, link-local, loopback, or Tailscale
	IsMatched     bool       // true if target is reachable via local interface subnet or Tailscale
	MatchedIface  string     // name of interface that matched (if any)
	MatchedSubnet *net.IPNet // subnet that matched (if any)
	Reason        string     // explanation
}

// IsTailscaleIP returns true if the IP is in Tailscale's IPv4 (100.64.0.0/10) or IPv6 (fd7a:115c:a1e0::/48) range.
func IsTailscaleIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return tailscaleIPv4Net.Contains(ip) || tailscaleIPv6Net.Contains(ip)
}

// IsLocalOrPrivateIP returns true if the IP is a private LAN, link-local, loopback, or Tailscale address.
func IsLocalOrPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return IsTailscaleIP(ip)
}

// CheckLocalSubnet checks whether targetIPStr is in any active local interface subnet.
func CheckLocalSubnet(targetIPStr string) SubnetMatchResult {
	return CheckLocalSubnetWithInterfaces(
		targetIPStr,
		net.Interfaces,
		func(iface *net.Interface) ([]net.Addr, error) {
			return iface.Addrs()
		},
	)
}

// CheckLocalSubnetWithInterfaces allows dependency injection of interfaces and address resolvers for testing.
func CheckLocalSubnetWithInterfaces(
	targetIPStr string,
	getInterfaces func() ([]net.Interface, error),
	getAddrs func(iface *net.Interface) ([]net.Addr, error),
) SubnetMatchResult {
	trimmed := strings.TrimSpace(targetIPStr)
	if trimmed == "" {
		return SubnetMatchResult{
			IsMatched: true,
			Reason:    "no IP address specified",
		}
	}

	ip := net.ParseIP(trimmed)
	if ip == nil {
		return SubnetMatchResult{
			IsMatched: true,
			Reason:    fmt.Sprintf("invalid IP address %q", trimmed),
		}
	}

	// If it's a public IP address, it is routed over the Internet through the default gateway.
	if !IsLocalOrPrivateIP(ip) {
		return SubnetMatchResult{
			TargetIP:      ip,
			IsLocalTarget: false,
			IsMatched:     true,
			Reason:        "public routable IP address",
		}
	}

	ifaces, err := getInterfaces()
	if err != nil {
		// If listing interfaces fails, fallback to assuming matched so we don't break health checking.
		return SubnetMatchResult{
			TargetIP:      ip,
			IsLocalTarget: true,
			IsMatched:     true,
			Reason:        fmt.Sprintf("failed to inspect local network interfaces: %v", err),
		}
	}

	// If the target is a Tailscale IP, check if the client machine has an active Tailscale interface.
	if IsTailscaleIP(ip) {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 {
				continue
			}
			addrs, err := getAddrs(&iface)
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				var addrIP net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					addrIP = v.IP
				case *net.IPAddr:
					addrIP = v.IP
				}
				if addrIP != nil && IsTailscaleIP(addrIP) {
					return SubnetMatchResult{
						TargetIP:      ip,
						IsLocalTarget: true,
						IsMatched:     true,
						MatchedIface:  iface.Name,
						Reason:        fmt.Sprintf("connected to Tailscale via interface %s (%s)", iface.Name, addrIP.String()),
					}
				}
			}
		}
		return SubnetMatchResult{
			TargetIP:      ip,
			IsLocalTarget: true,
			IsMatched:     false,
			Reason:        fmt.Sprintf("target %s is on Tailscale, but no active Tailscale connection was detected on this device", ip.String()),
		}
	}

	// Standard local private subnet matching (LAN, loopback, link-local)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := getAddrs(&iface)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok {
				if ipNet.Contains(ip) {
					return SubnetMatchResult{
						TargetIP:      ip,
						IsLocalTarget: true,
						IsMatched:     true,
						MatchedIface:  iface.Name,
						MatchedSubnet: ipNet,
						Reason:        fmt.Sprintf("target %s is in local subnet %s on %s", ip.String(), ipNet.String(), iface.Name),
					}
				}
			}
		}
	}

	return SubnetMatchResult{
		TargetIP:      ip,
		IsLocalTarget: true,
		IsMatched:     false,
		Reason:        fmt.Sprintf("target IP %s is not in any active local subnet (not on local LAN)", ip.String()),
	}
}

// IsSubnetUnreachable checks if any of the check results indicate an unreachable subnet/network.
func IsSubnetUnreachable(results []CheckResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if r.Type == "subnet" && !r.Success {
			return true
		}
	}
	allRouteErrors := true
	for _, r := range results {
		if r.Success {
			return false
		}
		errLower := strings.ToLower(r.Error)
		if !strings.Contains(errLower, "network is unreachable") &&
			!strings.Contains(errLower, "no route to host") &&
			!strings.Contains(errLower, "not on local") &&
			!strings.Contains(errLower, "tailscale") {
			allRouteErrors = false
			break
		}
	}
	return allRouteErrors
}
