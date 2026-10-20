package service

import (
	"context"
	"errors"
	"net"
	"sync"
)

// A connection belongs to one network phase, never to another request's pool.
// Keep cancellation active after DialContext returns, including during TLS/body
// work that net/http may perform with its detached transport context.
type callbackBudgetConnection struct {
	net.Conn
	stop     func() bool
	once     sync.Once
	closeErr error
}

func bindCallbackConnection(ctx context.Context, conn net.Conn) (net.Conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, errors.Join(err, conn.Close())
		}
	}
	owned := &callbackBudgetConnection{Conn: conn}
	owned.stop = context.AfterFunc(ctx, owned.closeUnderlying)
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, owned.Close())
	}
	return owned, nil
}

func (c *callbackBudgetConnection) closeUnderlying() {
	c.once.Do(func() { c.closeErr = c.Conn.Close() })
}

func (c *callbackBudgetConnection) Close() error {
	c.stop()
	c.closeUnderlying()
	return c.closeErr
}
