package service

import (
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestCallbackDeliveryReadCheckerIsImmutableAfterAttachmentOrPublication(t *testing.T) {
	_, checker := callbackReadFixture()
	t.Run("constructor attachment and concurrent snapshots", func(t *testing.T) {
		svc := NewCallbackService(nil, CallbackConfig{}, WithCallbackDeliveryPermission(checker)).(*callbackService)
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				if svc.callbackDeliveryPermission() != checker {
					t.Error("published checker changed")
				}
			})
		}
		wg.Wait()
		assertCallbackReadAttachmentRefused(t, svc, nil)
		if svc.callbackDeliveryPermission() != checker {
			t.Fatal("refused setter replaced the checker")
		}
	})
	t.Run("read freezes even a missing checker", func(t *testing.T) {
		svc := NewCallbackService(nil, CallbackConfig{}).(*callbackService)
		_, _, err := svc.ListDeliveries(callbackReadContext(), idgen.NewID(), domain.CallbackDeliveryFilter{})
		if err == nil {
			t.Fatal("missing checker granted a read")
		}
		assertCallbackReadAttachmentRefused(t, svc, checker)
	})
	t.Run("starting workers freezes missing configuration", func(t *testing.T) {
		svc := NewCallbackService(nil, CallbackConfig{}).(*callbackService)
		stop := svc.Start(t.Context(), time.Hour)
		t.Cleanup(stop)
		assertCallbackReadAttachmentRefused(t, svc, checker)
	})
}

func assertCallbackReadAttachmentRefused(t *testing.T, svc *callbackService, checker PermissionChecker) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("late/replacement permission attachment accepted")
		}
	}()
	svc.SetCallbackDeliveryPermission(checker)
}
