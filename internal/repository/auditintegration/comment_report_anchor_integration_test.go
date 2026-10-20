//go:build integration

package auditintegration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// This file is the Postgres-backed half of the report-resolution EventID
// anchor acceptance (spec 2026-08-06-internal-comment-report-resolve-audit-
// eventid-anchor §3). The service unit half lives in
// internal/service/audit_ledger_test.go (T-10.1-C) and the emitter tests in
// internal/{comment/service_test.go,service/comment_test.go} (T-10.1-D/F).

// TestReportResolveAnchoredEventIDAppendsOnce (T-10.1-A): a simulated retry
// of comment.report.resolve with the identical report-ID anchor yields exactly
// one audit_ledger row AND one audit_publications row, and the second append
// returns ErrAuditDuplicate through the pre-check path — so the retry never
// bumps chain_sequence (the pre-check runs before the chain-head load).
func TestReportResolveAnchoredEventIDAppendsOnce(t *testing.T) {
	ctx := context.Background()
	db, ledger := startAnchorLedger(t)
	reportID := idgen.NewID()
	fact := func() *domain.AuditFact {
		return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "tenant-a:moderator-1",
			TenantID: "tenant-a", EventID: reportID, Action: domain.AuditReportResolve,
			ResourceType: "comment_report", ResourceID: reportID.String(), OccurredAt: time.Now().UTC()}
	}
	if err := ledger.Append(ctx, fact()); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	var firstSeq int64
	if err := db.Raw(`SELECT chain_sequence FROM audit_ledger WHERE event_id = ?`, reportID).Scan(&firstSeq).Error; err != nil {
		t.Fatalf("read chain_sequence after first append: %v", err)
	}
	if err := ledger.Append(ctx, fact()); !errors.Is(err, repository.ErrAuditDuplicate) {
		t.Fatalf("second Append err = %v, want ErrAuditDuplicate", err)
	}
	if n := countAuditLedgerRows(t, db, reportID); n != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1", n)
	}
	if n := countAuditPublicationRows(t, db, reportID); n != 1 {
		t.Fatalf("publication rows = %d, want exactly 1 (one publication per fact)", n)
	}
	var secondSeq int64
	if err := db.Raw(`SELECT chain_sequence FROM audit_ledger WHERE event_id = ?`, reportID).Scan(&secondSeq).Error; err != nil {
		t.Fatalf("read chain_sequence after second append: %v", err)
	}
	if secondSeq != firstSeq {
		t.Fatalf("chain_sequence changed on duplicate append: %d -> %d, want stable", firstSeq, secondSeq)
	}
}

// TestReportResolveAnchoredFactListRoundTrip (T-10.1-B): after the two
// append attempts, List under the (tenant, source, action) filter returns
// exactly one fact whose EventID is the report ID.
func TestReportResolveAnchoredFactListRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, ledger := startAnchorLedger(t)
	reportID := idgen.NewID()
	fact := func() *domain.AuditFact {
		return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "tenant-a:moderator-1",
			TenantID: "tenant-a", EventID: reportID, Action: domain.AuditReportResolve,
			ResourceType: "comment_report", ResourceID: reportID.String(), OccurredAt: time.Now().UTC()}
	}
	if err := ledger.Append(ctx, fact()); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if err := ledger.Append(ctx, fact()); !errors.Is(err, repository.ErrAuditDuplicate) {
		t.Fatalf("second Append err = %v, want ErrAuditDuplicate", err)
	}
	facts, next, err := ledger.List(ctx, domain.AuditFactFilter{
		TenantID: "tenant-a", Source: "aero-id", Action: domain.AuditReportResolve,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("List returned %d facts, want exactly 1", len(facts))
	}
	if next != "" {
		t.Fatalf("List next cursor = %q, want empty", next)
	}
	if facts[0].EventID != reportID {
		t.Fatalf("fact EventID = %s, want report id %s", facts[0].EventID, reportID)
	}
	if facts[0].ResourceID != reportID.String() || facts[0].Action != domain.AuditReportResolve {
		t.Fatalf("fact resource/action = %q/%q, want %s/%s", facts[0].ResourceID, facts[0].Action, reportID.String(), domain.AuditReportResolve)
	}
}
