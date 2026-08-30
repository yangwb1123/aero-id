package service

import (
	"errors"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

// compatibilitySummaryRequest is the sole legacy construction seam. Bound
// source requests must go through connector.NewTrustedSummaryRequest so a
// caller cannot supply source identity fields as authority.
func compatibilitySummaryRequest(source connector.Source, account *domain.Account, actorUID,
	tenantID, requestID, operationID string, datasets []domain.AccountDataset, reconcile bool) (connector.SummaryRequest, error) {
	if source == connector.SourceAeroIM || source == connector.SourceAeroVault {
		return connector.SummaryRequest{}, errors.New("bound source requires trusted summary request")
	}
	if account == nil {
		return connector.SummaryRequest{}, errors.New("summary account is required")
	}
	return connector.SummaryRequest{
		AccountID: account.ID, CanonicalUID: account.CanonicalUID, ActorUID: actorUID,
		TenantID: tenantID, RequestID: requestID, OperationID: operationID,
		Region: account.HomeRegion, Datasets: append([]domain.AccountDataset(nil), datasets...),
		Reconcile: reconcile,
	}, nil
}
