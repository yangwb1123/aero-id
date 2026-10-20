package service

import (
	"net"
	"net/netip"
)

// Conservative callback egress policy, not a full or dynamically refreshed IANA
// registry. Deny private/shared, protocol, documentation, benchmark, transition
// and reserved ranges. Network egress controls are still required: a public IP
// alone cannot attest routing, proxies, receiver identity or authorization.
var callbackBlockedIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

var (
	callbackIPv6Unicast = netip.MustParsePrefix("2000::/3")
	callbackBlockedIPv6 = []netip.Prefix{
		netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
	}
)

func publicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if address.Is4() {
		return !callbackAddressBlocked(address, callbackBlockedIPv4)
	}
	return callbackIPv6Unicast.Contains(address) && !callbackAddressBlocked(address, callbackBlockedIPv6)
}

func callbackAddressBlocked(address netip.Addr, blocked []netip.Prefix) bool {
	for _, prefix := range blocked {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func publicIPsOnly(ips []net.IP) bool {
	for _, ip := range ips {
		if !publicIP(ip) {
			return false
		}
	}
	return len(ips) > 0
}
