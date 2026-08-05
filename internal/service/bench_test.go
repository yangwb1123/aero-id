package service

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/region"
	"github.com/aero/aero-id/internal/repository"
)

// P0-06 capacity baselines: micro-benchmarks of the hottest deterministic
// paths. Run with: go test -bench=. -benchmem ./internal/service/ -run '^$'
// These measure per-operation CPU cost; end-to-end p95/p99 must be measured
// with the k6/ghz load harness against a deployed cell (scripts/load).

// benchReceiptRepo applies every event (no dedup) so the ingest path cost is
// dominated by envelope handling and projection folding.
type benchReceiptRepo struct{}

func (benchReceiptRepo) ApplySnapshotEvent(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation) (repository.EventApplyResult, error) {
	return repository.EventApplyResult{Status: repository.EventApplied}, nil
}
func (benchReceiptRepo) ApplySnapshotEventWithAudit(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation, fact *domain.AuditFact) (repository.EventApplyResult, error) {
	return repository.EventApplyResult{Status: repository.EventApplied}, nil
}
func (benchReceiptRepo) QuarantineEvent(ctx context.Context, envelope *event.Envelope, code string) error {
	return nil
}

func benchEnvelope() *event.Envelope {
	accountID := idgen.NewID()
	return &event.Envelope{
		EventID: idgen.NewID(), Source: "aero-vault", SourceRegion: "us-west",
		EventType: "vault.usage.updated", SchemaVersion: 1, AggregateType: "object",
		AggregateID: "obj-1", AggregateVersion: 42, TenantID: "tenant-a",
		OperationID: idgen.NewID(), CorrelationID: idgen.NewID(), CausationID: idgen.NewID(),
		TraceID: "trace-1", ActorUID: "u-1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"account_id": accountID.String(), "dataset": "aero-vault.buckets", "status": "fresh",
			"version": float64(42), "data": map[string]any{"usage_bytes": 1024},
		},
	}
}

func BenchmarkEventIngest(b *testing.B) {
	svc := NewEventIngestService(benchReceiptRepo{})
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := svc.Ingest(ctx, benchEnvelope()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEnvelopeValidate(b *testing.B) {
	envelope := benchEnvelope()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := envelope.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEventRedactPayload(b *testing.B) {
	envelope := benchEnvelope()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = event.RedactPayload(envelope.Payload)
	}
}

func BenchmarkRegionResolver(b *testing.B) {
	resolver := newRegionResolverFixture()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = resolver.Resolve("https://snaplink.example.com", "subject-"+string(rune(i%1000)))
	}
}

func newRegionResolverFixture() *region.Resolver {
	return region.NewResolver("v1", []string{"us-east", "eu-west", "ap-southeast", "local"})
}

func BenchmarkProjectionReplayFolding(b *testing.B) {
	accountID := idgen.NewID()
	now := time.Now().UTC()
	envelopes := make([]*event.Envelope, 100)
	for i := range envelopes {
		envelopes[i] = benchEnvelope()
		envelopes[i].AggregateVersion = int64(i + 1)
		envelopes[i].Payload["version"] = float64(i + 1)
		envelopes[i].Payload["account_id"] = accountID.String()
		envelopes[i].OccurredAt = now.Add(time.Duration(i) * time.Second)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rebuildShadowProjection(envelopes); err != nil {
			b.Fatal(err)
		}
	}
}