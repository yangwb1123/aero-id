package service

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
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
	if _, err := mustActor(context.Background()); err == nil {
		t.Fatal("expected unauthorized error when actor is missing")
	}
}

func TestCanReadFile(t *testing.T) {
	pub := &domain.File{OwnerUID: "owner", Visibility: domain.VisibilityPublic}
	priv := &domain.File{OwnerUID: "owner", Visibility: domain.VisibilityPrivate}
	if !canReadFile("stranger", pub) {
		t.Error("public file should be readable by anyone")
	}
	if canReadFile("stranger", priv) {
		t.Error("private file should not be readable by a stranger")
	}
	if !canReadFile("owner", priv) {
		t.Error("owner should read its own private file")
	}
}

func TestCanReadMessage(t *testing.T) {
	m := &domain.Message{FromUID: "a", ToUID: "b"}
	if !canReadMessage("a", m) || !canReadMessage("b", m) {
		t.Error("both participants should read the message")
	}
	if canReadMessage("c", m) {
		t.Error("non-participant should not read the message")
	}
}
