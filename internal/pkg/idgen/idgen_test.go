package idgen

import (
	"testing"
	"time"
)

func TestNewID_uniqueAndTimeOrdered(t *testing.T) {
	prev := NewID()
	for i := 0; i < 100; i++ {
		time.Sleep(time.Millisecond)
		curr := NewID()
		if curr == prev {
			t.Fatal("two consecutive IDs collided")
		}
		if curr.String() < prev.String() {
			t.Fatalf("expected time-ordered, got %s < %s", curr, prev)
		}
		prev = curr
	}
}

func TestParse_roundtrip(t *testing.T) {
	id := NewID()
	parsed, err := Parse(id.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != id {
		t.Fatalf("parsed=%s want=%s", parsed, id)
	}
}
