package repository

import (
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestSummaryApplicationPlanFencesUnseenRowsAndEqualPayloads(t *testing.T) {
	accountID := idgen.NewID()
	newer := summaryPlanRequest(accountID, 12, false, "observed")
	newerPlan, err := prepareSummaryApplication(&newer)
	if err != nil {
		t.Fatalf("prepare newer: %v", err)
	}
	newerDecisions, err := newerPlan.decide(nil)
	if err != nil {
		t.Fatalf("decide newer: %v", err)
	}
	fences := fenceRowsForDecisions(newer, newerDecisions)

	older := summaryPlanRequest(accountID, 11, true, "older")
	olderPlan, err := prepareSummaryApplication(&older)
	if err != nil {
		t.Fatalf("prepare older: %v", err)
	}
	olderDecisions, err := olderPlan.decide(fences)
	if err != nil {
		t.Fatalf("decide older: %v", err)
	}
	if len(olderDecisions) == 0 {
		t.Fatal("older request produced no coverage decisions")
	}
	for _, decision := range olderDecisions {
		if !decision.stale || decision.advance || decision.apply {
			t.Fatalf("older decision = %+v, want stale no-op", decision)
		}
	}

	duplicatePlan, err := prepareSummaryApplication(&newer)
	if err != nil {
		t.Fatalf("prepare duplicate: %v", err)
	}
	duplicate, err := duplicatePlan.decide(fences)
	if err != nil {
		t.Fatalf("identical duplicate: %v", err)
	}
	for _, decision := range duplicate {
		if decision.advance || decision.stale {
			t.Fatalf("identical duplicate changed fence: %+v", decision)
		}
	}

	conflict := summaryPlanRequest(accountID, 12, false, "different")
	conflictPlan, err := prepareSummaryApplication(&conflict)
	if err != nil {
		t.Fatalf("prepare conflict: %v", err)
	}
	if _, err := conflictPlan.decide(fences); err == nil {
		t.Fatal("equal-version conflicting payload was accepted")
	} else {
		var validation *SummaryValidationError
		if !errors.As(err, &validation) || validation.Code != "summary.version_conflict" {
			t.Fatalf("conflict error = %v, want summary.version_conflict", err)
		}
	}
}

func TestSummaryApplicationRejectsRowsOutsideExplicitReferenceCoverage(t *testing.T) {
	request := summaryPlanRequest(idgen.NewID(), 1, true, "value")
	request.Coverage = domain.SummaryCoverage{Explicit: true, Datasets: []domain.AccountDataset{datasetForSummaryTest}}
	request.Sources = []*domain.AccountSource{{SourceAccountID: "source-1", Source: request.Source,
		SourceRegion: request.SourceRegion, Version: request.Version}}
	if _, err := prepareSummaryApplication(&request); err == nil {
		t.Fatal("source reference outside explicit coverage was accepted")
	} else {
		var validation *SummaryValidationError
		if !errors.As(err, &validation) || validation.Code != "summary.reference_out_of_scope" {
			t.Fatalf("coverage error = %v, want summary.reference_out_of_scope", err)
		}
	}
}

const datasetForSummaryTest = domain.AccountDataset("aero-vault.buckets")

func summaryPlanRequest(accountID domain.ID, version int64, complete bool, value string) domain.SummaryApplicationRequest {
	return domain.SummaryApplicationRequest{AccountID: accountID, Source: "aero-vault", SourceRegion: "west",
		Version: version, RequestedDatasets: []domain.AccountDataset{datasetForSummaryTest}, Complete: complete,
		Snapshots: []*domain.AccountSnapshot{{AccountID: accountID, Source: "aero-vault", SourceRegion: "west",
			Dataset: datasetForSummaryTest, Version: version, Status: domain.SnapshotFresh,
			Data: map[string]any{"value": value}}}}
}

func fenceRowsForDecisions(request domain.SummaryApplicationRequest,
	decisions summaryFenceDecisions) map[string]*summaryFenceRow {
	result := make(map[string]*summaryFenceRow, len(decisions))
	plan, err := prepareSummaryApplication(&request)
	if err != nil {
		panic(err)
	}
	for _, decision := range decisions {
		result[decision.token.key] = &summaryFenceRow{AccountID: request.AccountID, Source: request.Source,
			Coverage: decision.token.key, SourceRegion: request.SourceRegion, Version: request.Version,
			Fingerprint: plan.fingerprints[decision.token.key], Complete: decision.complete,
			Reconciled: decision.reconciled}
	}
	return result
}
