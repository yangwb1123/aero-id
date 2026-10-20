package app

import "github.com/aero/aero-id/internal/service"

// The audit stack remains independent of identity construction; attach the
// direct Snaplink checker at the server composition root, before Start. Never
// use the legacy delegating/local authorization checker for these new reads.
func wireCallbackDeliveryPermission(callbacks service.CallbackService, checker service.PermissionChecker) {
	setter, ok := callbacks.(service.CallbackDeliveryPermissionSetter)
	if !ok {
		panic("app: callback service must implement SetCallbackDeliveryPermission")
	}
	setter.SetCallbackDeliveryPermission(checker)
}
