package hub

import "github.com/aero/aero-id/internal/service"

// EX-1 negative fixture: hub may import service solely for the compile-time
// assertion below; service must NOT import hub. internal/hub is excluded
// from rule (d) by design (see the script header).
var _ service.NotificationPusher = (*Hub)(nil)

type Hub struct{}
