package outbox

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/google/uuid"
)

func TestChain_RunsAllHandlers(t *testing.T) {
	ctx := context.Background()
	e := &Event{ID: uuid.Must(uuid.NewV7()), EventType: "test"}
	var order []string
	h1 := HandlerFunc(func(_ context.Context, _ *Event) error { order = append(order, "h1"); return nil })
	h2 := HandlerFunc(func(_ context.Context, _ *Event) error { order = append(order, "h2"); return nil })
	c := NewChain(h1, h2)
	if err := c.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "h1" || order[1] != "h2" {
		t.Fatalf("order = %v", order)
	}
}

func TestChain_StopsOnError(t *testing.T) {
	ctx := context.Background()
	e := &Event{ID: uuid.Must(uuid.NewV7()), EventType: "test"}
	var called2 bool
	h1 := HandlerFunc(func(_ context.Context, _ *Event) error { return stderrors.New("boom") })
	h2 := HandlerFunc(func(_ context.Context, _ *Event) error { called2 = true; return nil })
	c := NewChain(h1, h2)
	if err := c.Handle(ctx, e); err == nil {
		t.Fatal("expected error")
	}
	if called2 {
		t.Fatal("h2 should not be called after h1 error")
	}
}

func TestChain_Empty(t *testing.T) {
	ctx := context.Background()
	e := &Event{ID: uuid.Must(uuid.NewV7()), EventType: "test"}
	c := NewChain()
	if err := c.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
}
