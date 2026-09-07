package connector

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

var (
	errOAuthTransportUnavailable = errors.New("OAuth token authority transport is unavailable")
	errOAuthDestinationRejected  = errors.New("OAuth token authority destination is not permitted")
)

// oauthHTTPClient creates a client whose TLS profile starts from a clean
// configuration. An injected client is a test seam, not a source of trust:
// its source CA, SNI, client certificate, proxy, and dialer must not become
// implicit trust for the token authority.
func oauthHTTPClient(cfg HTTPConfig) (*http.Client, error) {
	if cfg.Client == nil {
		return newSourceHTTPClient(cfg)
	}
	transport, err := cloneSourceTransport(cfg.Client)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = nil
	tlsConfig, err := sourceTLSConfig(nil, cfg)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsConfig
	client := *cfg.Client
	client.Transport = transport
	client.Jar = nil
	client.CheckRedirect = rejectSourceRedirect
	return &client, nil
}

// restrictOAuthDestination installs an IP-pinning dialer for one validated
// token endpoint. DNS is resolved for every new connection and the selected
// IP is dialed literally, so a DNS answer cannot change between validation and
// connect. Every answer must be a permitted public destination, except for
// the explicitly approved exact-loopback development tuple.
func restrictOAuthDestination(client *http.Client, endpoint *url.URL, allowLoopback bool) error {
	if client == nil || endpoint == nil || endpoint.Hostname() == "" {
		return errOAuthTransportUnavailable
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return errOAuthTransportUnavailable
	}
	clone := transport.Clone()
	clone.Proxy = nil
	clone.Dial = nil //nolint:staticcheck // force the destination policy below
	clone.DialContext = oauthAuthorityDialer(endpoint.Hostname(), allowLoopback)
	client.Transport = clone
	return nil
}

func oauthAuthorityDialer(expectedHost string, allowLoopback bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(host, expectedHost) {
			return nil, errOAuthDestinationRejected
		}
		if ctx == nil {
			ctx = context.Background()
		}
		ips, err := oauthAuthorityIPs(ctx, network, host, allowLoopback)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{}
		for _, ip := range ips {
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, errOAuthDestinationRejected
	}
}

func oauthAuthorityIPs(ctx context.Context, network, host string, allowLoopback bool) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return oauthLiteralAuthorityIP(network, host, ip, allowLoopback)
	}
	return oauthHostnameAuthorityIPs(ctx, network, host, allowLoopback)
}

func oauthLiteralAuthorityIP(network, host string, ip net.IP, allowLoopback bool) ([]net.IP, error) {
	if !sourceauth.OAuthDestinationAllowed(host, ip, allowLoopback) || !oauthNetworkMatches(network, ip) {
		return nil, errOAuthDestinationRejected
	}
	return []net.IP{ip}, nil
}

func oauthHostnameAuthorityIPs(ctx context.Context, network, host string, allowLoopback bool) ([]net.IP, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errOAuthDestinationRejected
	}
	if !allOAuthIPsAllowed(host, ips, allowLoopback) {
		return nil, errOAuthDestinationRejected
	}
	usable := filterOAuthNetworkIPs(network, ips)
	if len(usable) == 0 {
		return nil, errOAuthDestinationRejected
	}
	return usable, nil
}

func allOAuthIPsAllowed(host string, ips []net.IP, allowLoopback bool) bool {
	// Reject the entire answer set if any record is unsafe. Filtering a private
	// record while using a public one would make DNS policy depend on resolver
	// ordering and leaves a rebinding window.
	for _, ip := range ips {
		if !sourceauth.OAuthDestinationAllowed(host, ip, allowLoopback) {
			return false
		}
	}
	return true
}

func filterOAuthNetworkIPs(network string, ips []net.IP) []net.IP {
	usable := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if oauthNetworkMatches(network, ip) {
			usable = append(usable, ip)
		}
	}
	return usable
}

func oauthNetworkMatches(network string, ip net.IP) bool {
	switch network {
	case "tcp4":
		return ip.To4() != nil
	case "tcp6":
		return ip.To4() == nil
	default:
		return true
	}
}
