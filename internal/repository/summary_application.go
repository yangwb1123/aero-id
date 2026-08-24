package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// ErrSummaryApplicationUnavailable indicates that a projection consumer was
// constructed without the required application seam. It is a wiring failure,
// never a signal to fall back to individual repository writes.
var ErrSummaryApplicationUnavailable = errors.New("summary application is not configured")

// SummaryApplyFailureKind classifies the part of a local transaction that is
// known. A commit error is deliberately not called a rollback: PostgreSQL
// commits atomically, but a lost connection after COMMIT cannot tell the
// caller whether the commit reached the server.
type SummaryApplyFailureKind string

const (
	SummaryApplyRolledBack    SummaryApplyFailureKind = "rolled_back"
	SummaryApplyCommitUnknown SummaryApplyFailureKind = "commit_unknown"
)

// SummaryApplicationError is returned for local persistence failures. The
// service layer maps it to a safe stable AppError while retaining the kind for
// retry and workflow decisions.
type SummaryApplicationError struct {
	Kind  SummaryApplyFailureKind
	Cause error
}

func (e *SummaryApplicationError) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Kind)
}

func (e *SummaryApplicationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// SummaryApplyError is a short compatibility name for callers that use the
// apply terminology directly.
type SummaryApplyError = SummaryApplicationError

// SummaryApplication is the single local write seam for a connector summary.
// It owns one PostgreSQL transaction across all three projection classes.
type SummaryApplication interface {
	ApplySummary(context.Context, domain.SummaryApplicationRequest) (domain.SummaryApplicationResult, error)
}

// SummaryApplicationReconciler resolves an ambiguous ApplySummary result by
// checking the durable receipt and retrying the exact idempotent request only
// when that receipt is absent. The concrete PostgreSQL application implements
// this in addition to SummaryApplication.
type SummaryApplicationReconciler interface {
	ReconcileSummary(context.Context, domain.SummaryApplicationRequest) (domain.SummaryApplicationResult, error)
}

// SummaryValidationError identifies a rejected summary before any SQL write is
// attempted. Code is stable and safe to expose through a service error mapper.
type SummaryValidationError struct {
	Code    string
	Message string
}

func (e *SummaryValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func summaryValidationError(code, message string) error {
	return &SummaryValidationError{Code: code, Message: message}
}

type accountSummaryApplication struct{ db *gorm.DB }

// NewAccountSummaryApplication returns the transaction-owning projection
// application. The supplied DB must be the same local database used by the
// account, snapshot, source, and membership repositories.
func NewAccountSummaryApplication(db *gorm.DB) SummaryApplication {
	return &accountSummaryApplication{db: db}
}

func (r *accountSummaryApplication) ApplySummary(ctx context.Context,
	request domain.SummaryApplicationRequest) (domain.SummaryApplicationResult, error) {
	plan, err := prepareSummaryApplication(&request)
	if err != nil {
		return domain.SummaryApplicationResult{}, err
	}
	return r.applyPrepared(ctx, request, plan)
}

func (r *accountSummaryApplication) applyPrepared(ctx context.Context, request domain.SummaryApplicationRequest,
	plan summaryApplicationPlan) (domain.SummaryApplicationResult, error) {
	if r == nil || r.db == nil {
		return domain.SummaryApplicationResult{}, &SummaryApplicationError{Kind: SummaryApplyRolledBack,
			Cause: ErrSummaryApplicationUnavailable}
	}
	tx := r.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return domain.SummaryApplicationResult{}, summaryRolledBack(tx.Error)
	}
	if err := lockSummaryRowsTx(tx, request.AccountID, request.Source); err != nil {
		return domain.SummaryApplicationResult{}, summaryRolledBackTx(tx, err)
	}
	return applySummaryTransaction(tx, request, plan)
}

func applySummaryTransaction(tx *gorm.DB, request domain.SummaryApplicationRequest,
	plan summaryApplicationPlan) (domain.SummaryApplicationResult, error) {
	appliedAt := time.Now().UTC()
	decision, err := summaryReceiptDecisionTx(tx, request, plan)
	if err != nil {
		if isSummaryValidationError(err) {
			return domain.SummaryApplicationResult{}, rollbackValidationTx(tx, err)
		}
		return domain.SummaryApplicationResult{}, summaryRolledBackTx(tx, err)
	}
	if decision.stale {
		if err := tx.Commit().Error; err != nil {
			return domain.SummaryApplicationResult{}, summaryCommitUnknown(err)
		}
		return summaryStaleResult(), nil
	}
	if err := applySummaryProjectionTx(tx, request, decision.decisions, appliedAt); err != nil {
		return domain.SummaryApplicationResult{}, summaryRolledBackTx(tx, err)
	}
	if err := writeSummaryReceiptsTx(tx, request, plan, decision, appliedAt); err != nil {
		return domain.SummaryApplicationResult{}, summaryRolledBackTx(tx, err)
	}
	if err := tx.Commit().Error; err != nil {
		return domain.SummaryApplicationResult{}, summaryCommitUnknown(err)
	}
	return summaryAppliedResult(request, decision), nil
}

func applySummaryProjectionTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	decisions summaryFenceDecisions, appliedAt time.Time) error {
	if !summaryApplicationComplete(request) || !decisions.hasProjectionWork() {
		return nil
	}
	if err := upsertSummarySnapshotsTx(tx, request, decisions, appliedAt); err != nil {
		return err
	}
	if err := upsertSummarySourcesTx(tx, request, decisions, appliedAt); err != nil {
		return err
	}
	if err := upsertSummaryMembershipsTx(tx, request, decisions, appliedAt); err != nil {
		return err
	}
	return applySummaryOmissionsTx(tx, request, decisions, appliedAt)
}

func (r *accountSummaryApplication) ReconcileSummary(ctx context.Context,
	request domain.SummaryApplicationRequest) (domain.SummaryApplicationResult, error) {
	plan, err := prepareSummaryApplication(&request)
	if err != nil {
		return domain.SummaryApplicationResult{}, err
	}
	if r == nil || r.db == nil {
		return domain.SummaryApplicationResult{}, &SummaryApplicationError{Kind: SummaryApplyCommitUnknown,
			Cause: ErrSummaryApplicationUnavailable}
	}
	decision, err := readSummaryReceiptDecision(ctx, r.db, request, plan)
	if err != nil {
		return domain.SummaryApplicationResult{}, summaryCommitUnknown(err)
	}
	if decision.resolved {
		if decision.stale {
			return summaryStaleResult(), nil
		}
		return summaryAppliedResult(request, decision), nil
	}
	// No receipt can exist without the projection transaction's receipt write;
	// retrying the same request is therefore safe and resolves a rolled-back or
	// lost-before-commit attempt without duplicating a successful application.
	return r.applyPrepared(ctx, request, plan)
}

func summaryApplicationComplete(request domain.SummaryApplicationRequest) bool {
	return request.Complete && len(request.SkippedDatasets) == 0
}

func summaryRolledBack(err error) error {
	return &SummaryApplicationError{Kind: SummaryApplyRolledBack, Cause: err}
}

func summaryRolledBackTx(tx *gorm.DB, err error) error {
	_ = tx.Rollback().Error
	return summaryRolledBack(err)
}

func rollbackValidationTx(tx *gorm.DB, err error) error {
	_ = tx.Rollback().Error
	return err
}

func summaryCommitUnknown(err error) error {
	return &SummaryApplicationError{Kind: SummaryApplyCommitUnknown, Cause: err}
}

func isSummaryValidationError(err error) bool {
	var validation *SummaryValidationError
	return errors.As(err, &validation)
}

func summaryStaleResult() domain.SummaryApplicationResult {
	return domain.SummaryApplicationResult{Outcome: domain.SummaryStaleIgnored, StaleIgnored: true}
}

func summaryAppliedResult(request domain.SummaryApplicationRequest, decision summaryReceiptDecision) domain.SummaryApplicationResult {
	result := domain.SummaryApplicationResult{Outcome: domain.SummaryApplied, Applied: true}
	if !summaryApplicationComplete(request) {
		result.Outcome = domain.SummaryIncomplete
	}
	if summaryApplicationComplete(request) && request.Reconcile && decision.reconciled {
		result.Outcome = domain.SummaryReconciled
		result.Reconciled = true
	}
	return result
}
