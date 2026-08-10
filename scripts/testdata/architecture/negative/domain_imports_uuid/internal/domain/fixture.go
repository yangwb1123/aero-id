package domain

import "github.com/google/uuid"

// EX-3 negative fixture: internal/domain/ may import github.com/google/uuid
// ONLY as an exact-string exception (internal/domain/audit.go uses it for
// the AuditEvent.ID field type). Any other third-party import — including a
// different google/* package — still trips rule (b); see the script header
// TODO to delete this exception once AuditEvent.ID moves to internal/pkg/idgen.
type Fixture struct {
	ID uuid.UUID
}
