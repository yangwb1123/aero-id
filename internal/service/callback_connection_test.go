package service

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type callbackOwnedPipe struct {
	net.Conn
	closes atomic.Int32
}

func (p *callbackOwnedPipe) Close() error { p.closes.Add(1); return p.Conn.Close() }

func TestCallbackConnectionCancellationClosesSocketAndUnblocksReads(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	raw := &callbackOwnedPipe{Conn: client}
	owned, err := bindCallbackConnection(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, readErr := owned.Read(make([]byte, 1)); done <- readErr }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled connection read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("connection read outlived owner cancellation")
	}
	for i := 0; i < 2; i++ {
		if err := owned.Close(); err != nil {
			t.Error(err)
		}
	}
	if raw.closes.Load() != 1 {
		t.Fatalf("connection close count = %d, want 1", raw.closes.Load())
	}
}

type callbackDeadlineFailureConn struct {
	callbackTrackedConn
	err error
}

func (c *callbackDeadlineFailureConn) SetDeadline(time.Time) error { return c.err }

func TestCallbackConnectionDeadlineFailureClosesAndNeverAdoptsSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	failure := errors.New("cannot bound callback socket")
	conn := &callbackDeadlineFailureConn{err: failure}
	owned, err := bindCallbackConnection(ctx, conn)
	if owned != nil || !errors.Is(err, failure) || conn.closes.Load() != 1 {
		t.Fatalf("unbounded socket adopted/leaked: owned=%v error=%v closes=%d", owned, err, conn.closes.Load())
	}
}
