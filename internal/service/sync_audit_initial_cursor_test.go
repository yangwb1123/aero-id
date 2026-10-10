package service

import (
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestSyncAuditExportInitialCursorAcceptsPersistedNormalizedRequest(t *testing.T) {
	svc := &syncJobService{}
	for _, request := range []map[string]any{nil, {}, {"datasets": []domain.AccountDataset{"snaplink.identity"}}} {
		cursor, err := svc.auditExportStartCursor(t.Context(), &domain.SyncJob{Request: request})
		if err != nil || cursor != "" {
			t.Fatalf("initial persisted request %v = %q/%v", request, cursor, err)
		}
	}
	for _, value := range []any{nil, "", 42, "not-a-uuid"} {
		_, err := svc.auditExportStartCursor(t.Context(), &domain.SyncJob{Request: map[string]any{auditExportContinuationRequestKey: value}})
		if requireAppError(t, err).Code != "audit_export.invalid_continuation" {
			t.Fatal("present malformed continuation treated as initial request", value, err)
		}
	}
}
