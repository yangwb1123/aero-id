package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type fakeLegacyMigrationRepo struct {
	files    *repository.LegacyDataDigest
	messages *repository.LegacyDataDigest
	comments *repository.LegacyDataDigest
	err      error
}

func (f *fakeLegacyMigrationRepo) LegacyFileDigest(ctx context.Context, ownerUID string, limit int) (*repository.LegacyDataDigest, error) {
	return f.files, f.err
}
func (f *fakeLegacyMigrationRepo) LegacyMessageDigest(ctx context.Context, uid string, limit int) (*repository.LegacyDataDigest, error) {
	return f.messages, f.err
}
func (f *fakeLegacyMigrationRepo) LegacyCommentDigest(ctx context.Context, ownerUID string, limit int) (*repository.LegacyDataDigest, error) {
	return f.comments, f.err
}

type fakeMigrationConnector struct {
	source  connector.Source
	summary *connector.Summary
	err     error
}

func (f *fakeMigrationConnector) Source() connector.Source { return f.source }
func (f *fakeMigrationConnector) Health(ctx context.Context) error { return nil }
func (f *fakeMigrationConnector) Summary(ctx context.Context, req connector.SummaryRequest) (*connector.Summary, error) {
	return f.summary, f.err
}

func migrationDigest(count int64) *repository.LegacyDataDigest {
	return &repository.LegacyDataDigest{Count: count, Digest: "abc"}
}

// TestLegacyMigrationCompare_Matched: legacy counts equal the source counters.
func TestLegacyMigrationCompare_Matched(t *testing.T) {
	repo := &fakeLegacyMigrationRepo{
		files: migrationDigest(7), messages: migrationDigest(3), comments: migrationDigest(5),
	}
	client := &fakeMigrationConnector{source: connector.SourceAeroVault, summary: &connector.Summary{
		Source: connector.SourceAeroVault,
		Snapshots: []*domain.AccountSnapshot{{
			Source: string(connector.SourceAeroVault), Data: map[string]any{"object_count": float64(7)},
		}},
	}}
	svc := NewLegacyMigrationService(repo, client)
	result, err := svc.Compare(context.Background(), LegacyCompareInput{OwnerUID: "u-1", Source: connector.SourceAeroVault})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !result.Matched {
		t.Errorf("drift = %v, want matched", result.Drift)
	}
	if !result.SourceAvailable || result.Partial {
		t.Errorf("available/partial = %v/%v", result.SourceAvailable, result.Partial)
	}
}

// TestLegacyMigrationCompare_DriftDetected: counter mismatch is reported.
func TestLegacyMigrationCompare_DriftDetected(t *testing.T) {
	repo := &fakeLegacyMigrationRepo{files: migrationDigest(10), messages: migrationDigest(0), comments: migrationDigest(0)}
	client := &fakeMigrationConnector{source: connector.SourceAeroVault, summary: &connector.Summary{
		Source: connector.SourceAeroVault,
		Snapshots: []*domain.AccountSnapshot{{
			Source: string(connector.SourceAeroVault), Data: map[string]any{"object_count": float64(7)},
		}},
	}}
	svc := NewLegacyMigrationService(repo, client)
	result, err := svc.Compare(context.Background(), LegacyCompareInput{OwnerUID: "u-1", Source: connector.SourceAeroVault})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched || len(result.Drift) == 0 {
		t.Fatalf("drift must be reported: %v", result)
	}
}

// TestLegacyMigrationCompare_SourceUnavailable: no connector or source outage
// yields an honest unavailable result with legacy digests intact.
func TestLegacyMigrationCompare_SourceUnavailable(t *testing.T) {
	repo := &fakeLegacyMigrationRepo{files: migrationDigest(2), messages: migrationDigest(1), comments: migrationDigest(0)}
	svc := NewLegacyMigrationService(repo) // no connectors
	result, err := svc.Compare(context.Background(), LegacyCompareInput{OwnerUID: "u-1", Source: connector.SourceAeroVault})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceAvailable {
		t.Error("source must be reported unavailable without a connector")
	}
	if result.Legacy.Files == nil || result.Legacy.Files.Count != 2 {
		t.Errorf("legacy digests must still be reported: %+v", result.Legacy)
	}

	// Connector present but the source is down.
	down := &fakeMigrationConnector{source: connector.SourceAeroVault, err: errors.New("source down")}
	svc = NewLegacyMigrationService(repo, down)
	result, err = svc.Compare(context.Background(), LegacyCompareInput{OwnerUID: "u-1", Source: connector.SourceAeroVault})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceAvailable || result.SourceErrors["source"] == "" {
		t.Errorf("source outage must be reported: %+v", result.SourceErrors)
	}
}

// TestLegacyMigrationCompare_TruncatedIsPartial: a limit-hit digest flags
// partial comparison instead of a false match.
func TestLegacyMigrationCompare_TruncatedIsPartial(t *testing.T) {
	repo := &fakeLegacyMigrationRepo{
		files: &repository.LegacyDataDigest{Count: 1000, Digest: "x", Truncated: true},
		messages: migrationDigest(0), comments: migrationDigest(0),
	}
	client := &fakeMigrationConnector{source: connector.SourceAeroVault, summary: &connector.Summary{
		Source: connector.SourceAeroVault,
		Snapshots: []*domain.AccountSnapshot{{
			Source: string(connector.SourceAeroVault), Data: map[string]any{"object_count": float64(1000)},
		}},
	}}
	svc := NewLegacyMigrationService(repo, client)
	result, err := svc.Compare(context.Background(), LegacyCompareInput{OwnerUID: "u-1", Source: connector.SourceAeroVault, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial {
		t.Error("truncated digest must flag partial")
	}
}

// TestLegacyMigrationCompare_RequiresOwner.
func TestLegacyMigrationCompare_RequiresOwner(t *testing.T) {
	svc := NewLegacyMigrationService(&fakeLegacyMigrationRepo{})
	if _, err := svc.Compare(context.Background(), LegacyCompareInput{Source: connector.SourceAeroVault}); err == nil {
		t.Fatal("missing owner_uid must be rejected")
	}
}
