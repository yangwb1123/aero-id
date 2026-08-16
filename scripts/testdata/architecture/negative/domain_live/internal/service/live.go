package service

import "github.com/aero/aero-id/internal/domain"

// Live consumers of the domain exports declared in the domain_live fixture:
// every symbol enumerated by rule (f) for that fixture is referenced here
// with the qualified domain.X form. Trap's NotificationType field label must
// not count as a reference to any symbol.
func liveDomainConsumers() {
	_ = domain.Account{}
	_ = domain.SnapshotStatus("stale")
	_ = domain.SnapshotFresh
	_ = domain.Trap{NotificationType: "trap"}
}
