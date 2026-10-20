package service

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallbackNetworkRejectsWholeResolutionWithoutDialing(t *testing.T) {
	tooMany := make([]net.IP, maxCallbackResolvedIPs+1)
	for i := range tooMany {
		tooMany[i] = net.ParseIP("8.8.8.8")
	}
	for _, test := range []struct {
		name    string
		ips     []net.IP
		err     error
		private bool
	}{
		{"empty", nil, nil, false}, {"failure", nil, errors.New("private resolver diagnostics"), false},
		{"too many", tooMany, nil, false}, {"too many even in development", tooMany, nil, true},
		{"mixed loopback", []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("127.0.0.1")}, nil, false},
		{"mixed metadata", []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("100.100.100.200")}, nil, false},
		{"malformed", []net.IP{net.ParseIP("8.8.8.8"), nil}, nil, false},
		{"malformed development", []net.IP{nil}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dials := 0
			network := callbackNetwork{lookupIP: func(context.Context, string, string) ([]net.IP, error) { return test.ips, test.err },
				dial: func(context.Context, string, string) (net.Conn, error) {
					dials++
					return nil, errors.New("unexpected dial")
				}}
			if _, err := network.dialTarget(t.Context(), "tcp", "receiver.example.test:443", test.private); err == nil || dials != 0 {
				t.Fatalf("unsafe resolution reached dial: error=%v calls=%d", err, dials)
			}
		})
	}
}

func TestCallbackNetworkLateDNSCannotDialAndPortsFailClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var dials atomic.Int32
	network := callbackNetwork{lookupIP: func(ctx context.Context, _, _ string) ([]net.IP, error) {
		cancel()
		<-ctx.Done()
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}, dial: func(context.Context, string, string) (net.Conn, error) { dials.Add(1); return nil, nil }}
	if _, err := network.dialTarget(ctx, "tcp", "receiver.example.test:443", false); !errors.Is(err, context.Canceled) || dials.Load() != 0 {
		t.Fatalf("late DNS result authorized dial: error=%v calls=%d", err, dials.Load())
	}
	if _, err := (callbackNetwork{}).dialTarget(t.Context(), "tcp", "receiver.example.test:443", false); err == nil {
		t.Fatal("missing resolver did not fail closed")
	}
	withoutDial := callbackNetwork{lookupIP: func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}}
	if _, err := withoutDial.dialTarget(t.Context(), "tcp", "receiver.example.test:443", false); err == nil {
		t.Fatal("missing dialer did not fail closed")
	}
}

func TestCallbackNetworkPinsLiteralAddressesAndRejectsRebinding(t *testing.T) {
	lookups, dials := 0, []string{}
	network := callbackNetwork{lookupIP: func(context.Context, string, string) ([]net.IP, error) {
		lookups++
		if lookups > 1 {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}, dial: func(_ context.Context, _, address string) (net.Conn, error) {
		dials = append(dials, address)
		return nil, errors.New("unexpected rebind dial")
	}}
	if err := network.validateEndpoint(t.Context(), callbackTestEndpoint, false); err != nil {
		t.Fatal(err)
	}
	if _, err := network.dialTarget(t.Context(), "tcp", "cb.example.test:443", false); err == nil || len(dials) != 0 {
		t.Fatalf("connection-time private DNS rebound: error=%v dials=%v", err, dials)
	}
	network.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("2606:4700::1111")}, nil
	}
	if _, err := network.dialTarget(t.Context(), "tcp", "cb.example.test:8443", false); err == nil {
		t.Fatal("test dial failure was lost")
	}
	if len(dials) != 2 || dials[0] != "8.8.8.8:8443" || dials[1] != "[2606:4700::1111]:8443" {
		t.Fatalf("dial was not bound to validated literal IPs: %v", dials)
	}
}

func TestCallbackNetworkBoundDialRetainsOwnerAndTransportCancellation(t *testing.T) {
	for _, cancelOwner := range []bool{false, true} {
		owner, cancel := context.WithTimeout(t.Context(), time.Second)
		transport, stop := context.WithCancel(t.Context())
		entered := make(chan struct{})
		network := callbackNetwork{lookupIP: func(ctx context.Context, _, _ string) ([]net.IP, error) {
			deadline, ok := ctx.Deadline()
			ownerDeadline, _ := owner.Deadline()
			if !ok || !deadline.Equal(ownerDeadline) {
				t.Error("transport detached the owning request deadline")
			}
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		done := make(chan error, 1)
		go func() {
			_, err := network.boundDial(owner, false)(transport, "tcp", "cb.example.test:443")
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("resolver did not enter")
		}
		if cancelOwner {
			cancel()
		} else {
			stop()
		}
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled owner/transport outcome = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled resolution did not return")
		}
		cancel()
		stop()
	}
}

func TestCallbackNetworkDiscardsAndClosesLateConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	closed := &callbackTrackedConn{}
	network := callbackNetwork{lookupIP: func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}, dial: func(context.Context, string, string) (net.Conn, error) { cancel(); return closed, nil }}
	conn, err := network.dialTarget(ctx, "tcp", "cb.example.test:443", false)
	if conn != nil || !errors.Is(err, context.Canceled) || closed.closes.Load() != 1 {
		t.Fatalf("late connection adopted/leaked: conn=%v error=%v closes=%d", conn, err, closed.closes.Load())
	}
}

type callbackTrackedConn struct {
	net.Conn
	closes atomic.Int32
}

func (c *callbackTrackedConn) Close() error { c.closes.Add(1); return nil }
