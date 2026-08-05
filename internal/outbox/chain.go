package outbox

import "context"

// Chain is a composite Handler that dispatches to multiple handlers in order.
// If any handler returns an error, the chain stops and returns that error.
type Chain struct {
	handlers []Handler
}

func NewChain(handlers ...Handler) *Chain {
	return &Chain{handlers: handlers}
}

func (c *Chain) Handle(ctx context.Context, e *Event) error {
	for _, h := range c.handlers {
		if err := h.Handle(ctx, e); err != nil {
			return err
		}
	}
	return nil
}
