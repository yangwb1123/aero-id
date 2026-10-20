package app

import (
	"context"
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type callbackPermissionProbe struct{ calls int }

func (p *callbackPermissionProbe) HasPermission(_ context.Context, uid, permission string) (bool, error) {
	if uid == "caller" && permission == domain.PermCallbackManage {
		p.calls++
	}
	return false, nil
}

type callbackWithoutPermissionSetter struct{ service.CallbackService }

func TestCallbackReadPermissionIsAttachedBeforePublishingService(t *testing.T) {
	callbacks := service.NewCallbackService(nil, service.CallbackConfig{})
	checker := &callbackPermissionProbe{}
	wireCallbackDeliveryPermission(callbacks, checker)
	ctx := service.WithIdentity(t.Context(), &domain.TokenIdentity{Verified: true, UID: "caller", Subject: "caller",
		Issuer: "https://snaplink.example", ClientID: "console", TenantID: "tenant-a"})
	_, _, err := callbacks.ListDeliveries(ctx, idgen.NewID(), domain.CallbackDeliveryFilter{})
	var app *apperr.AppError
	if checker.calls != 1 || !errors.As(err, &app) || app.Code != "callback.authorization_denied" {
		t.Fatal("direct checker was not attached", checker.calls, err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("missing permission wiring seam silently accepted")
		}
	}()
	wireCallbackDeliveryPermission(&callbackWithoutPermissionSetter{}, checker)
}
