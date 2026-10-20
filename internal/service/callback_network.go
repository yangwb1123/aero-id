package service

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

const maxCallbackResolvedIPs = 32

// Private per-instance seams keep tests offline without replacing the process
// resolver or weakening the production egress policy. Construct before use.
type callbackNetwork struct {
	lookupIP func(context.Context, string, string) ([]net.IP, error)
	dial     func(context.Context, string, string) (net.Conn, error)
}

func defaultCallbackNetwork() callbackNetwork {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return callbackNetwork{lookupIP: net.DefaultResolver.LookupIP, dial: dialer.DialContext}
}

func (n callbackNetwork) resolve(ctx context.Context, host string, allowPrivate bool) ([]net.IP, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n.lookupIP == nil {
		return nil, errors.New("callback resolver is unavailable")
	}
	ips, err := n.lookupIP(ctx, "ip", host)
	if ctx.Err() != nil {
		return nil, ctx.Err() // Never adopt a successful answer after cancellation.
	}
	if err != nil || len(ips) == 0 {
		return nil, errors.New("callback target cannot be resolved")
	}
	if len(ips) > maxCallbackResolvedIPs {
		return nil, errors.New("callback resolution exceeds address budget")
	}
	if !callbackResolvedIPsAllowed(ips, allowPrivate) {
		return nil, errors.New("callback target resolved to a disallowed address")
	}
	return ips, nil
}

func callbackResolvedIPsAllowed(ips []net.IP, allowPrivate bool) bool {
	for _, ip := range ips {
		if ip.To16() == nil {
			return false
		}
	}
	return allowPrivate || publicIPsOnly(ips)
}

func (n callbackNetwork) validateEndpoint(ctx context.Context, raw string, allowPrivate bool) error {
	host, err := callbackEndpointHost(raw, allowPrivate)
	if err != nil {
		return err
	}
	if allowPrivate {
		return ctx.Err()
	}
	_, err = n.resolve(ctx, host, false)
	return err
}

func callbackEndpointHost(raw string, allowPrivate bool) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("endpoint must be an https URL without userinfo")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if strings.Contains(host, "%") {
		return "", errors.New("callback address zones are not allowed")
	}
	if !allowPrivate && isPrivateHost(host) {
		return "", errors.New("private callback targets are not allowed")
	}
	return parsed.Hostname(), nil
}

func isPrivateHost(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local")
}

func (n callbackNetwork) dialTarget(ctx context.Context, network, address string, allowPrivate bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := n.resolve(ctx, host, allowPrivate)
	if err != nil {
		return nil, err
	}
	if n.dial == nil {
		return nil, errors.New("callback dialer is unavailable")
	}
	for _, ip := range ips {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, dialErr := n.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		conn, err = callbackDialResult(ctx, conn, dialErr)
		if conn != nil || ctx.Err() != nil {
			return conn, err
		}
	}
	return nil, err
}

func callbackDialResult(ctx context.Context, conn net.Conn, err error) (net.Conn, error) {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && conn != nil {
		return conn, nil
	}
	if err == nil {
		err = errors.New("callback connection is unavailable")
	}
	if conn != nil {
		err = errors.Join(err, conn.Close())
	}
	return nil, err
}

// net/http can detach its dial context from a request to enable connection reuse.
// Retain the owning call's original deadline as well as transport cancellation.
func (n callbackNetwork) boundDial(parent context.Context, allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	return func(transportCtx context.Context, network, address string) (net.Conn, error) {
		if err := transportCtx.Err(); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(parent)
		stop := context.AfterFunc(transportCtx, cancel)
		defer stop()
		defer cancel()
		conn, err := n.dialTarget(ctx, network, address, allowPrivate)
		if err != nil {
			return nil, err
		}
		return bindCallbackConnection(parent, conn)
	}
}
