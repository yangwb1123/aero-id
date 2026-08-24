package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

// summaryReceiptDecision is the durable revision decision for one summary.
// Fences are the receipt: a committed fence means the summary (including an
// incomplete summary) has been observed and older revisions cannot insert
// stale rows into a previously empty natural key.
type summaryReceiptDecision struct {
	decisions  summaryFenceDecisions
	stale      bool
	resolved   bool
	reconciled bool
}

func prepareSummaryApplication(request *domain.SummaryApplicationRequest) (summaryApplicationPlan, error) {
	if request == nil {
		return summaryApplicationPlan{}, summaryValidationError("summary.request_invalid", "summary request is required")
	}
	if err := validateSummaryApplicationRequest(request); err != nil {
		return summaryApplicationPlan{}, err
	}
	return newSummaryApplicationPlan(*request)
}

func summaryReceiptDecisionTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	plan summaryApplicationPlan) (summaryReceiptDecision, error) {
	fences, err := loadSummaryFencesTx(tx, request.AccountID, request.Source, plan.tokens)
	if err != nil {
		return summaryReceiptDecision{}, err
	}
	decisions, err := plan.decide(fences)
	if err != nil {
		return summaryReceiptDecision{}, err
	}
	return newSummaryReceiptDecision(decisions), nil
}

func readSummaryReceiptDecision(ctx context.Context, db *gorm.DB, request domain.SummaryApplicationRequest,
	plan summaryApplicationPlan) (summaryReceiptDecision, error) {
	if db == nil {
		return summaryReceiptDecision{}, ErrSummaryApplicationUnavailable
	}
	// The read is intentionally not used as an authorization or projection
	// write. It only resolves an ambiguous commit by observing the durable
	// fence; the subsequent ApplySummary takes the locking transaction path.
	query := db.WithContext(ctx).Model(&summaryFenceRow{}).
		Where("account_id = ? AND source = ? AND coverage IN ?", request.AccountID, request.Source, summaryCoverageKeys(plan.tokens)).
		Clauses(clause.Locking{Strength: "KEY SHARE"})
	var rows []summaryFenceRow
	if len(plan.tokens) == 0 {
		return summaryReceiptDecision{resolved: true}, nil
	}
	if err := query.Find(&rows).Error; err != nil {
		return summaryReceiptDecision{}, err
	}
	fences := make(map[string]*summaryFenceRow, len(rows))
	for i := range rows {
		row := rows[i]
		fences[row.Coverage] = &row
	}
	decisions, err := plan.decide(fences)
	if err != nil {
		return summaryReceiptDecision{}, err
	}
	result := newSummaryReceiptDecision(decisions)
	result.resolved = true
	for _, decision := range decisions {
		if decision.advance {
			result.resolved = false
			break
		}
	}
	return result, nil
}

func summaryCoverageKeys(tokens []summaryCoverageToken) []string {
	keys := make([]string, len(tokens))
	for i, token := range tokens {
		keys[i] = token.key
	}
	return keys
}

func newSummaryReceiptDecision(decisions summaryFenceDecisions) summaryReceiptDecision {
	result := summaryReceiptDecision{decisions: decisions, resolved: true}
	for _, decision := range decisions {
		result.stale = result.stale || decision.stale
		result.reconciled = result.reconciled || decision.reconcile
		if decision.advance {
			result.resolved = false
		}
	}
	return result
}

func writeSummaryReceiptsTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	plan summaryApplicationPlan, decision summaryReceiptDecision, appliedAt time.Time) error {
	return persistSummaryFencesTx(tx, request, decision.decisions, appliedAt)
}
