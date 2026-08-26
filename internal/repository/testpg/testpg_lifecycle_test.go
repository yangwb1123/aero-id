//go:build integration

package testpg

import (
	"context"
	"testing"
)

type recordingTerminator struct {
	events *[]string
	ctx    context.Context
	err    error
}

func (r *recordingTerminator) Terminate(ctx context.Context) error {
	*r.events = append(*r.events, "terminate")
	r.ctx = ctx
	r.err = ctx.Err()
	return nil
}

func TestCleanupFixtureClosesPoolBeforeContainer(t *testing.T) {
	var events []string
	container := &recordingTerminator{events: &events}
	cleanupFixture(t, container.Terminate, func() error {
		events = append(events, "close")
		return nil
	})

	if want := []string{"close", "terminate"}; !equalStrings(events, want) {
		t.Fatalf("cleanup events = %v, want %v", events, want)
	}
	if container.ctx == nil {
		t.Fatal("terminate received no context")
	}
	if _, ok := container.ctx.Deadline(); !ok {
		t.Fatal("terminate context has no deadline")
	}
	if err := container.err; err != nil {
		t.Fatalf("terminate context unexpectedly canceled: %v", err)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
