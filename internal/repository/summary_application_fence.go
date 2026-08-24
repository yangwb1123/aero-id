package repository

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

const (
	summaryCoverageSources     = "sources"
	summaryCoverageMemberships = "memberships"
	summaryCoverageDataset     = "dataset:"
)

type summaryFenceRow struct {
	AccountID    uuid.UUID `gorm:"column:account_id;primaryKey"`
	Source       string    `gorm:"column:source;primaryKey"`
	Coverage     string    `gorm:"column:coverage;primaryKey"`
	SourceRegion string    `gorm:"column:source_region"`
	Version      int64     `gorm:"column:version"`
	Fingerprint  string    `gorm:"column:fingerprint"`
	Complete     bool      `gorm:"column:complete"`
	Reconciled   bool      `gorm:"column:reconciled"`
	ObservedAt   time.Time `gorm:"column:observed_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

func (summaryFenceRow) TableName() string { return "account_summary_fences" }

type summaryCoverageToken struct {
	key              string
	kind             string
	dataset          domain.AccountDataset
	omissionCoverage bool
}

type summaryApplicationPlan struct {
	tokens       []summaryCoverageToken
	fingerprints map[string]string
	request      domain.SummaryApplicationRequest
}

type summaryFenceDecision struct {
	token       summaryCoverageToken
	fingerprint string
	previous    *summaryFenceRow
	stale       bool
	advance     bool
	apply       bool
	reconcile   bool
	reconciled  bool
	complete    bool
}

type summaryFenceDecisions []summaryFenceDecision

func newSummaryApplicationPlan(request domain.SummaryApplicationRequest) (summaryApplicationPlan, error) {
	tokens := summaryCoverageTokens(request)
	fingerprints, err := summaryApplicationFingerprints(request, tokens)
	if err != nil {
		return summaryApplicationPlan{}, err
	}
	return summaryApplicationPlan{tokens: tokens, fingerprints: fingerprints, request: request}, nil
}

func summaryCoverageTokens(request domain.SummaryApplicationRequest) []summaryCoverageToken {
	result := make([]summaryCoverageToken, 0, len(request.Coverage.Datasets)+2)
	seen := make(map[string]struct{})
	result = appendDatasetCoverageTokens(result, seen, request)
	result = appendReferenceCoverageTokens(result, seen, request)
	sort.Slice(result, func(i, j int) bool { return result[i].key < result[j].key })
	return result
}

func appendDatasetCoverageTokens(values []summaryCoverageToken, seen map[string]struct{},
	request domain.SummaryApplicationRequest) []summaryCoverageToken {
	omissionCoverage := request.Reconcile && summaryApplicationComplete(request)
	for _, dataset := range request.Coverage.Datasets {
		values = appendSummaryCoverageToken(values, seen, summaryCoverageToken{key: summaryCoverageDataset + string(dataset),
			kind: summaryCoverageDataset, dataset: dataset, omissionCoverage: omissionCoverage})
	}
	return values
}

func appendReferenceCoverageTokens(values []summaryCoverageToken, seen map[string]struct{},
	request domain.SummaryApplicationRequest) []summaryCoverageToken {
	legacy := !request.Coverage.Explicit
	complete := request.Reconcile && summaryApplicationComplete(request)
	sourcesIncluded := referenceCoverageIncluded(request.Coverage.Explicit, request.Coverage.Sources,
		len(request.Sources), legacy)
	membershipsIncluded := referenceCoverageIncluded(request.Coverage.Explicit, request.Coverage.Memberships,
		len(request.Memberships), legacy)
	values = appendReferenceCoverageToken(values, seen, summaryCoverageSources, sourcesIncluded,
		referenceOmissionCoverage(complete, request.Coverage.Sources, legacy))
	return appendReferenceCoverageToken(values, seen, summaryCoverageMemberships, membershipsIncluded,
		referenceOmissionCoverage(complete, request.Coverage.Memberships, legacy))
}

func referenceCoverageIncluded(explicit, declared bool, rowCount int, legacy bool) bool {
	if declared || legacy {
		return true
	}
	return !explicit && rowCount > 0
}

func referenceOmissionCoverage(complete, declared, legacy bool) bool {
	return complete && (declared || legacy)
}

func appendReferenceCoverageToken(values []summaryCoverageToken, seen map[string]struct{},
	key string, included, omissionCoverage bool) []summaryCoverageToken {
	if !included {
		return values
	}
	return appendSummaryCoverageToken(values, seen, summaryCoverageToken{key: key,
		kind: key, omissionCoverage: omissionCoverage})
}

func appendSummaryCoverageToken(values []summaryCoverageToken, seen map[string]struct{}, token summaryCoverageToken) []summaryCoverageToken {
	if _, exists := seen[token.key]; exists {
		return values
	}
	seen[token.key] = struct{}{}
	return append(values, token)
}

func (p summaryApplicationPlan) decide(fences map[string]*summaryFenceRow) (summaryFenceDecisions, error) {
	decisions := make(summaryFenceDecisions, 0, len(p.tokens))
	for _, token := range p.tokens {
		decision, err := p.decideToken(token, fences[token.key])
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, decision)
	}
	return decisions, nil
}

func (p summaryApplicationPlan) decideToken(token summaryCoverageToken,
	previous *summaryFenceRow) (summaryFenceDecision, error) {
	decision := summaryFenceDecision{token: token, previous: previous, fingerprint: p.fingerprints[token.key],
		complete: summaryApplicationComplete(p.request)}
	if previous == nil {
		return p.newerDecision(decision), nil
	}
	if previous.Version > p.request.Version {
		decision.stale = true
		return p.finishDecision(decision), nil
	}
	if previous.Version == p.request.Version {
		return p.equalDecision(decision)
	}
	return p.newerDecision(decision), nil
}

func (p summaryApplicationPlan) equalDecision(decision summaryFenceDecision) (summaryFenceDecision, error) {
	previous := decision.previous
	if previous.SourceRegion != p.request.SourceRegion ||
		(previous.Fingerprint != "" && previous.Fingerprint != decision.fingerprint) {
		return summaryFenceDecision{}, summaryValidationError("summary.version_conflict", "source revision has a different payload")
	}
	if !previous.Complete && decision.complete {
		decision.advance, decision.apply = true, true
	}
	if decision.complete && decision.token.omissionCoverage && p.request.Reconcile && !previous.Reconciled {
		decision.advance, decision.apply, decision.reconcile = true, true, true
	}
	return p.finishDecision(decision), nil
}

func (p summaryApplicationPlan) newerDecision(decision summaryFenceDecision) summaryFenceDecision {
	decision.advance = true
	decision.apply = decision.complete
	decision.reconcile = decision.complete && decision.token.omissionCoverage && p.request.Reconcile
	return p.finishDecision(decision)
}

func (p summaryApplicationPlan) finishDecision(decision summaryFenceDecision) summaryFenceDecision {
	decision.reconciled = decision.reconcile
	if !decision.reconciled && decision.previous != nil {
		decision.reconciled = decision.previous.Reconciled
	}
	return decision
}

func (d summaryFenceDecisions) hasProjectionWork() bool {
	for _, decision := range d {
		if decision.apply {
			return true
		}
	}
	return false
}

func (d summaryFenceDecisions) canApply(key string) bool {
	for _, decision := range d {
		if decision.token.key == key {
			return decision.apply
		}
	}
	return false
}

func loadSummaryFencesTx(tx *gorm.DB, accountID domain.ID, source string,
	tokens []summaryCoverageToken) (map[string]*summaryFenceRow, error) {
	result := make(map[string]*summaryFenceRow, len(tokens))
	if len(tokens) == 0 {
		return result, nil
	}
	keys := make([]string, len(tokens))
	for i, token := range tokens {
		keys[i] = token.key
	}
	var rows []summaryFenceRow
	if err := tx.Where("account_id = ? AND source = ? AND coverage IN ?", accountID, source, keys).
		Order("coverage ASC").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		row := rows[i]
		result[row.Coverage] = &row
	}
	return result, nil
}

func persistSummaryFencesTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	decisions summaryFenceDecisions, appliedAt time.Time) error {
	for _, decision := range decisions {
		if !decision.advance {
			continue
		}
		row := &summaryFenceRow{AccountID: request.AccountID, Source: request.Source, Coverage: decision.token.key,
			SourceRegion: request.SourceRegion, Version: request.Version, Fingerprint: decision.fingerprint,
			Complete: decision.complete, Reconciled: decision.reconciled, ObservedAt: appliedAt, UpdatedAt: appliedAt}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_id"}, {Name: "source"}, {Name: "coverage"}},
			DoUpdates: clause.AssignmentColumns([]string{"source_region", "version", "fingerprint", "complete", "reconciled", "observed_at", "updated_at"}),
		}).Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}
