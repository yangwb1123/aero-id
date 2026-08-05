package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// projectionReplicationNamespace anchors deterministic event ids so re-publishing
// the same snapshot version dedups on (tenant, source, event_id).
var projectionReplicationNamespace = uuid.MustParse("a3e0f5c8-1b2d-4c6e-9f8a-7d6c5b4a3921")

// DefaultReplicationBatch caps one replication run; the worker loops until the
// batch is not full so a large backlog drains without one unbounded scan.
const DefaultReplicationBatch = 500

// ProjectionReplicationService publishes approved local projections to remote
// regions as canonical events (P6-03). Cross-region transport carries only
// approved projection data — never raw source content.
type ProjectionReplicationService interface {
	// ReplicateOnce publishes up to one batch and returns the count. Safe to
	// call concurrently with Start (cursor CAS is idempotent).
	ReplicateOnce(ctx context.Context) (int, error)
	// Start runs the replication loop on interval until the stop func is called.
	Start(ctx context.Context, interval time.Duration) func()
}

type projectionReplicationService struct {
	region    string
	snapshots repository.SnapshotRepository
	cursors   repository.ReplicationCursorRepository
	publisher event.EventPublisher
	batch     int
	metrics   ControlPlaneMetrics
}

func NewProjectionReplicationService(region string, snapshots repository.SnapshotRepository,
	cursors repository.ReplicationCursorRepository, publisher event.EventPublisher,
	options ...func(*projectionReplicationService)) ProjectionReplicationService {
	svc := &projectionReplicationService{region: region, snapshots: snapshots, cursors: cursors,
		publisher: publisher, batch: DefaultReplicationBatch, metrics: noopControlPlaneMetrics{}}
	for _, opt := range options {
		opt(svc)
	}
	return svc
}

// WithProjectionReplicationMetrics attaches the observability sink.
func WithProjectionReplicationMetrics(metrics ControlPlaneMetrics) func(*projectionReplicationService) {
	return func(s *projectionReplicationService) {
		if metrics != nil {
			s.metrics = metrics
		}
	}
}

func (s *projectionReplicationService) ReplicateOnce(ctx context.Context) (int, error) {
	if s.publisher == nil {
		return 0, nil
	}
	lastID, published, err := s.cursors.GetCursor(ctx, s.region, s.publisher.Target())
	if err != nil {
		return 0, fmt.Errorf("load replication cursor: %w", err)
	}
	items, err := s.snapshots.ListReplicableSnapshots(ctx, s.region, lastID, s.batch)
	if err != nil {
		return 0, fmt.Errorf("scan replicable snapshots: %w", err)
	}
	count := 0
	for _, item := range items {
		envelope := replicationEnvelope(s.region, item.TenantID, item.Snapshot)
		if s.metrics != nil {
			s.metrics.ObserveProjectionLag(item.Snapshot.Source, string(item.Snapshot.Dataset), time.Since(item.Snapshot.UpdatedAt.UTC()))
		}
		if err := s.publisher.Publish(ctx, envelope); err != nil {
			// Stop at the first failure; the cursor stays behind so the next
			// run resumes here (at-least-once, never skip).
			return count, fmt.Errorf("publish projection: %w", err)
		}
		count++
		if err := s.cursors.SaveCursor(ctx, s.region, s.publisher.Target(), item.Snapshot.ID, published+int64(count)); err != nil {
			return count, fmt.Errorf("save replication cursor: %w", err)
		}
	}
	return count, nil
}

func (s *projectionReplicationService) Start(ctx context.Context, interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Second
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-ticker.C:
				if _, err := s.ReplicateOnce(workerCtx); err != nil {
					// Transient failures keep the cursor; the next tick retries.
					continue
				}
			case <-workerCtx.Done():
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

// replicationEnvelope builds the canonical projection.snapshot event. The
// event id is deterministic per (tenant, source, account, dataset, version) so
// identical re-publishes are receipt-deduped at the consumer.
func replicationEnvelope(sourceRegion, tenantID string, snapshot *domain.AccountSnapshot) *event.Envelope {
	payload := map[string]any{
		"account_id": snapshot.AccountID.String(),
		"dataset":    string(snapshot.Dataset),
		"status":     string(snapshot.Status),
		"version":    float64(snapshot.Version),
		"data":       snapshot.Data,
	}
	if !snapshot.GeneratedAt.IsZero() {
		payload["generated_at"] = snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano)
	}
	stable := uuid.NewSHA1(projectionReplicationNamespace, []byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d",
		tenantID, "aero-id", snapshot.AccountID.String(), snapshot.Dataset, snapshot.Version)))
	return &event.Envelope{
		EventID:          idgen.ID(stable),
		Source:           "aero-id",
		SourceRegion:     sourceRegion,
		EventType:        "projection.snapshot",
		SchemaVersion:    1,
		AggregateType:    "projection",
		AggregateID:      snapshot.AccountID.String(),
		AggregateVersion: snapshot.Version,
		TenantID:         tenantID,
		OccurredAt:       snapshot.UpdatedAt.UTC(),
		Payload:          payload,
	}
}
