package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

func TestEventIngestMapsReceiptIntegrityConflictToBoundedConflict(t *testing.T) {
	receipts := &integrityApplyReceiptRepo{}
	svc := NewEventIngestService(receipts)
	_, err := svc.Ingest(context.Background(), testEnvelope("us-west"))
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %v, want AppError", err)
	}
	if appErr.Code != "event.integrity_conflict" || appErr.HTTPStatus() != http.StatusConflict {
		t.Fatalf("error = %s (%d), want event.integrity_conflict (409)", appErr.Code, appErr.HTTPStatus())
	}
	if len(receipts.applied) != 0 {
		t.Fatalf("conflicting event applied despite rejection: %v", receipts.applied)
	}
}

func TestEventIngestMapsQuarantineIntegrityConflictToBoundedConflict(t *testing.T) {
	receipts := &fakeReceiptRepo{quarantineErr: repository.ErrEventIntegrityConflict}
	svc := NewEventIngestService(receipts, WithEventAllowedRegions([]string{"us-west"}))
	_, err := svc.Ingest(context.Background(), testEnvelope("cn-north"))
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %v, want AppError", err)
	}
	if appErr.Code != "event.integrity_conflict" || appErr.HTTPStatus() != http.StatusConflict {
		t.Fatalf("error = %s (%d), want event.integrity_conflict (409)", appErr.Code, appErr.HTTPStatus())
	}
}

type integrityApplyReceiptRepo struct{ fakeReceiptRepo }

func (f *integrityApplyReceiptRepo) ApplySnapshotEvent(context.Context, *event.Envelope,
	*event.SnapshotMutation) (repository.EventApplyResult, error) {
	return repository.EventApplyResult{}, repository.ErrEventIntegrityConflict
}
