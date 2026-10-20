package service

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestCallbackEgressRejectsSpecialUseAndMappedAddresses(t *testing.T) {
	for _, address := range []string{
		"0.0.0.0", "0.12.34.56", "10.0.0.1", "100.64.0.0", "100.100.100.200", "100.127.255.255",
		"127.0.0.1", "169.254.169.254", "172.16.0.1", "172.31.255.255", "192.0.0.9", "192.0.2.1",
		"192.88.99.1", "192.168.0.1", "198.18.0.1", "198.19.255.255", "198.51.100.1", "203.0.113.1",
		"224.0.0.1", "239.255.255.255", "240.0.0.1", "255.255.255.255", "::", "::1", "fc00::1", "fe80::1",
		"ff02::1", "64:ff9b::a9fe:a9fe", "64:ff9b:1::a00:1", "2001::1", "2001:2::1", "2001:20::1",
		"2001:db8::1", "2002:7f00:1::1", "3fff::1", "4000::1", "5f00::1",
		"::ffff:127.0.0.1", "::ffff:100.100.100.200", "::ffff:169.254.169.254", "::ffff:192.168.0.1",
	} {
		t.Run(address, func(t *testing.T) {
			ip := net.ParseIP(address)
			if ip == nil {
				t.Fatal("invalid test address")
			}
			if publicIP(ip) {
				t.Fatal("special-use address was eligible for production callback egress")
			}
		})
	}
	for _, address := range []string{"1.1.1.1", "8.8.8.8", "100.63.255.255", "100.128.0.0", "172.32.0.1", "2606:4700::1111", "2001:4860:4860::8888", "::ffff:8.8.8.8"} {
		if !publicIP(net.ParseIP(address)) {
			t.Errorf("public unicast address rejected: %s", address)
		}
	}
	if publicIP(nil) || publicIPsOnly(nil) || publicIPsOnly([]net.IP{net.ParseIP("8.8.8.8"), nil}) {
		t.Fatal("empty/malformed resolution was accepted")
	}
}

func TestCallbackEgressRejectsUnsafeURLsBeforeDNS(t *testing.T) {
	calls := 0
	network := callbackNetwork{lookupIP: func(context.Context, string, string) ([]net.IP, error) {
		calls++
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}}
	for _, endpoint := range []string{
		"", "http://example.test", "https:///path", "https://name:password@example.test", "https://example.test:invalid/",
		"https://localhost/", "https://LOCALHOST./", "https://service.local/", "https://service.local./", "https://a.localhost/",
		"https://[fe80::1%25eth0]/", "https://[2001:4860::1%25eth0]/",
	} {
		if err := network.validateEndpoint(t.Context(), endpoint, false); err == nil {
			t.Errorf("invalid endpoint accepted: %q", endpoint)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid URL made %d resolver calls", calls)
	}
	if err := network.validateEndpoint(t.Context(), "https://localhost/", true); err != nil {
		t.Fatalf("explicit development-only private-target policy changed: %v", err)
	}
	if _, err := callbackEndpointHost("https://[fe80::1%25eth0]/", true); err == nil || !strings.Contains(err.Error(), "zones") {
		t.Fatal("scoped address bypassed validation in development")
	}
}
