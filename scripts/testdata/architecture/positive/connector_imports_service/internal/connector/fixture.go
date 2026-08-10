package connector

import "github.com/aero/aero-id/internal/service"

// Rule (d) positive fixture: infra layers
// (internal/{connector,event,outbox,region,sso}, including subpackages) must
// not import internal/service or internal/repository.
