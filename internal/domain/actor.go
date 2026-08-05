package domain

import (
	"context"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

type ctxKey int

const actorKey ctxKey = iota

func WithActor(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, actorKey, uid)
}

func ActorFrom(ctx context.Context) (string, bool) {
	uid, _ := ctx.Value(actorKey).(string)
	return uid, uid != ""
}

func MustActor(ctx context.Context) (string, error) {
	uid, ok := ActorFrom(ctx)
	if !ok {
		return "", apperr.Unauthorized("auth.unauthorized", "missing authenticated actor")
	}
	return uid, nil
}
