package audit

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
)

func TestDomainToRow(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)
	e := &domain.AuditEvent{
		ID:          id,
		ActorUID:    "actor-1",
		Action:      domain.AuditUserBlock,
		SubjectType: "user",
		SubjectID:   "target-1",
		TargetUID:   "target-1",
		Meta:        map[string]any{"reason": "spam"},
		CreatedAt:   now,
	}
	row := domainToRow(e)
	if row.ID != id {
		t.Errorf("ID = %v, want %v", row.ID, id)
	}
	if row.ActorUID != "actor-1" {
		t.Errorf("ActorUID = %q", row.ActorUID)
	}
	if row.Action != domain.AuditUserBlock {
		t.Errorf("Action = %q", row.Action)
	}
	if row.SubjectType != "user" {
		t.Errorf("SubjectType = %q", row.SubjectType)
	}
	if row.SubjectID != "target-1" {
		t.Errorf("SubjectID = %q", row.SubjectID)
	}
	if row.TargetUID != "target-1" {
		t.Errorf("TargetUID = %q", row.TargetUID)
	}
	if row.Meta["reason"] != "spam" {
		t.Errorf("Meta = %v", row.Meta)
	}
	if !row.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", row.CreatedAt, now)
	}
}

func TestDomainToRowEmptyMeta(t *testing.T) {
	e := &domain.AuditEvent{
		ActorUID:    "a1",
		Action:      "test",
		SubjectType: "message",
		SubjectID:   "m1",
		CreatedAt:   time.Now().UTC(),
	}
	row := domainToRow(e)
	if row.Meta != nil && len(row.Meta) != 0 {
		t.Errorf("Meta = %v, want nil", row.Meta)
	}
}
