package domain

import (
	"context"
	"testing"
)

func TestActorRoundTrip(t *testing.T) {
	ctx := WithActor(context.Background(), "u1")
	uid, ok := ActorFrom(ctx)
	if !ok || uid != "u1" {
		t.Fatalf("uid=%q ok=%v", uid, ok)
	}
}

func TestActorFrom_missing(t *testing.T) {
	if _, ok := ActorFrom(context.Background()); ok {
		t.Fatal("expected no actor in empty context")
	}
}

func TestActorFrom_emptyStringIsNotPresent(t *testing.T) {
	ctx := WithActor(context.Background(), "")
	if _, ok := ActorFrom(ctx); ok {
		t.Fatal("empty actor must be treated as absent")
	}
}

func TestMustActor_error(t *testing.T) {
	if _, err := MustActor(context.Background()); err == nil {
		t.Fatal("expected error when actor is missing")
	}
}

func TestMustActor_returnsUID(t *testing.T) {
	ctx := WithActor(context.Background(), "u1")
	uid, err := MustActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if uid != "u1" {
		t.Fatalf("uid=%q", uid)
	}
}
