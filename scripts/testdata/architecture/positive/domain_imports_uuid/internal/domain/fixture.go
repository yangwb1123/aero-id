package domain

import "github.com/google/uuid"

// Rule (b) positive fixture: internal/domain/ may import only stdlib and
// internal/pkg/* (the exact-string google/uuid exception was deleted when
// AuditEvent.ID switched to the domain ID alias). google/uuid is now an
// ordinary third-party import and trips rule (b).
type Fixture struct {
	ID uuid.UUID
}
