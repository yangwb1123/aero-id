package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// projectionReplicationNamespace anchors deterministic event ids so re-publishing
// the same snapshot revision dedups on (tenant, source, event_id).
var projectionReplicationNamespace = uuid.MustParse("a3e0f5c8-1b2d-4c6e-9f8a-7d6c5b4a3921")

// DefaultReplicationBatch caps one replication run; the worker loops until the
// batch is not full so a large backlog drains without one unbounded scan.
const DefaultReplicationBatch = 500

const defaultProjectionReplicationLease = 2 * time.Minute
const defaultReplicationMaxTerminalAttempts = 3

// ProjectionReplicationService publishes approved local projections to remote
// regions as canonical events (P6-03). Cross-region transport carries only
// approved projection data — never raw source content.
type ProjectionReplicationService interface {
	// ReplicateOnce publishes up to one batch and returns the count. Safe to
	// call concurrently with Start (the durable cursor lease fences workers).
	ReplicateOnce(ctx context.Context) (int, error)
	// Start runs the replication loop on interval until the stop func is called.
	Start(ctx context.Context, interval time.Duration) func()
}

type projectionReplicationService struct {
	region              string
	snapshots           repository.SnapshotRepository
	cursors             repository.ReplicationCursorRepository
	publisher           event.EventPublisher
	audit               AuditLedgerService // nil => audit inactive
	batch               int
	metrics             ControlPlaneMetrics
	worker              string
	lease               time.Duration
	terminalFailures    repository.ReplicationTerminalFailureRepository
	maxTerminalAttempts int
	leaseGeneration     int64
	runMu               sync.Mutex
}

func NewProjectionReplicationService(region string, snapshots repository.SnapshotRepository,
	cursors repository.ReplicationCursorRepository, publisher event.EventPublisher,
	options ...func(*projectionReplicationService)) ProjectionReplicationService {
	svc := &projectionReplicationService{region: region, snapshots: snapshots, cursors: cursors,
		publisher: publisher, batch: DefaultReplicationBatch, metrics: noopControlPlaneMetrics{},
		worker: idgen.NewID().String(), lease: defaultProjectionReplicationLease,
		maxTerminalAttempts: defaultReplicationMaxTerminalAttempts}
	for _, opt := range options {
		opt(svc)
	}
	return svc
}

// WithProjectionReplicationLease configures the durable cursor lease. A zero
// value keeps the service default so old callers remain safe.
func WithProjectionReplicationLease(lease time.Duration) func(*projectionReplicationService) {
	return func(s *projectionReplicationService) {
		if lease > 0 {
			s.lease = lease
		}
	}
}

// WithProjectionReplicationMetrics attaches the observability sink.
func WithProjectionReplicationMetrics(metrics ControlPlaneMetrics) func(*projectionReplicationService) {
	return func(s *projectionReplicationService) {
		if metrics != nil {
			s.metrics = metrics
		}
	}
}

// WithProjectionReplicationAudit wires the ledger service into the replication
// worker; when wired, every published snapshot records a per-item
// projection.replication.publish fact (deterministic envelope EventID).
func WithProjectionReplicationAudit(a AuditLedgerService) func(*projectionReplicationService) {
	return func(s *projectionReplicationService) { s.audit = a }
}

func WithProjectionReplicationTerminalFailures(r repository.ReplicationTerminalFailureRepository) func(*projectionReplicationService) {
	return func(s *projectionReplicationService) { s.terminalFailures = r }
}

func WithProjectionReplicationMaxTerminalAttempts(max int) func(*projectionReplicationService) {
	return func(s *projectionReplicationService) {
		if max > 0 {
			s.maxTerminalAttempts = max
		}
	}
}

func (s *projectionReplicationService) ReplicateOnce(ctx context.Context) (int, error) {
	if s.publisher == nil {
		return 0, nil
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.replicateLocked(ctx)
}

func (s *projectionReplicationService) replicateLocked(ctx context.Context) (int, error) {
	target, release, acquired, err := s.acquireReplicationLease(ctx)
	if err != nil {
		return 0, err
	}
	if !acquired {
		return 0, nil
	}
	defer release()

	// New workers consume the immutable commit-ordered log. This avoids
	// timestamp ties/rollback and keeps tombstones visible after deletion.
	if changes, ok := s.snapshots.(repository.ReplicationChangeRepository); ok {
		if cursor, sequenceOK := s.cursors.(repository.ReplicationCursorSequenceRepository); sequenceOK {
			sequence, published, err := cursor.GetCursorSequence(ctx, s.region, target)
			if err != nil {
				return 0, fmt.Errorf("load replication cursor: %w", err)
			}
			items, err := changes.ListReplicationChangesAfter(ctx, s.region, sequence, s.batch)
			if err != nil {
				return 0, fmt.Errorf("scan replication changes: %w", err)
			}
			return s.publishReplicationBatch(ctx, target, published, items)
		}
	}
	lastID, lastUpdatedAt, published, err := s.replicationCheckpoint(ctx, target)
	if err != nil {
		return 0, fmt.Errorf("load replication cursor: %w", err)
	}
	items, err := s.listReplicableSnapshots(ctx, lastUpdatedAt, lastID)
	if err != nil {
		return 0, fmt.Errorf("scan replicable snapshots: %w", err)
	}
	return s.publishReplicationBatch(ctx, target, published, items)
}

func (s *projectionReplicationService) replicationCheckpoint(ctx context.Context, target string) (domain.ID, time.Time, int64, error) {
	if checkpointRepo, ok := s.cursors.(repository.ReplicationCursorCheckpointRepository); ok {
		return checkpointRepo.GetCursorCheckpoint(ctx, s.region, target)
	}
	lastID, published, err := s.cursors.GetCursor(ctx, s.region, target)
	return lastID, time.Time{}, published, err
}

func (s *projectionReplicationService) listReplicableSnapshots(ctx context.Context, afterUpdatedAt time.Time,
	afterID domain.ID) ([]repository.ReplicableSnapshot, error) {
	if versioned, ok := s.snapshots.(repository.ReplicationSnapshotRepository); ok {
		return versioned.ListReplicableSnapshotsAfter(ctx, s.region, afterUpdatedAt, afterID, s.batch)
	}
	return s.snapshots.ListReplicableSnapshots(ctx, s.region, afterID, s.batch)
}

func (s *projectionReplicationService) acquireReplicationLease(ctx context.Context) (string, func(), bool, error) {
	target := s.publisher.Target()
	leaseRepo, fenced := s.cursors.(repository.ReplicationCursorLeaseRepository)
	if !fenced {
		s.leaseGeneration = 0
		return target, func() {}, true, nil
	}
	generationRepo, hasGeneration := s.cursors.(repository.ReplicationCursorGenerationLeaseRepository)
	var acquired bool
	var generation int64
	var err error
	if hasGeneration {
		acquired, generation, err = generationRepo.ClaimCursorGeneration(ctx, s.region, target, s.worker, s.lease)
	} else {
		acquired, err = leaseRepo.ClaimCursor(ctx, s.region, target, s.worker, s.lease)
	}
	if err != nil {
		return "", nil, false, fmt.Errorf("claim replication cursor: %w", err)
	}
	if !acquired {
		return target, func() {}, false, nil
	}
	s.leaseGeneration = generation
	release := func() { _ = leaseRepo.ReleaseCursor(context.Background(), s.region, target, s.worker) }
	return target, release, true, nil
}

func (s *projectionReplicationService) publishReplicationBatch(ctx context.Context, target string,
	published int64, items []repository.ReplicableSnapshot) (int, error) {
	count := 0
	for _, item := range items {
		var err error
		count, err = s.publishReplicationItem(ctx, target, published, count, item)
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *projectionReplicationService) publishReplicationItem(ctx context.Context, target string,
	published int64, count int, item repository.ReplicableSnapshot) (int, error) {
	envelope := replicationEnvelopeAtRevision(s.region, item.TenantID, item.Snapshot, item.Revision)
	if item.Deleted {
		envelope.Payload["deleted"] = true
	}
	if s.metrics != nil {
		s.metrics.ObserveProjectionLag(item.Snapshot.Source, string(item.Snapshot.Dataset), time.Since(item.Snapshot.UpdatedAt.UTC()))
	}
	failure, err := s.terminalFailure(ctx, target, envelope.EventID)
	if err != nil {
		return count, fmt.Errorf("load replication terminal failure: %w", err)
	}
	if failure != nil && failure.State == "parked" {
		return s.finishParkedItem(ctx, target, published, count, item, envelope, *failure)
	}
	if err := s.publisher.Publish(ctx, envelope); err != nil {
		return s.handleReplicationPublishError(ctx, target, published, count, item, envelope, err)
	}
	count++
	if err := s.recordReplicationAudit(ctx, target, envelope, item); err != nil {
		return count, err
	}
	if err := s.saveReplicationItemCursor(ctx, target, item, published+int64(count)); err != nil {
		return count, err
	}
	return count, nil
}

func (s *projectionReplicationService) recordReplicationAudit(ctx context.Context, target string,
	envelope *event.Envelope, item repository.ReplicableSnapshot) error {
	if s.audit == nil {
		return nil
	}
	// audit-site: standalone-fact — per-published-projection fact; the
	// deterministic envelope EventID makes a re-publish dedup at the ledger.
	if err := s.audit.Record(ctx, AuditFactInput{Source: "aero-id", SourceRegion: s.region,
		EventID: envelope.EventID, AccountID: item.Snapshot.AccountID, TenantID: item.TenantID,
		ActorUID: "system", Action: domain.AuditProjectionReplicationPublish,
		ResourceType: "projection", ResourceID: item.Snapshot.AccountID.String() + ":" + string(item.Snapshot.Dataset),
		Payload: map[string]any{"target": target, "dataset": string(item.Snapshot.Dataset),
			"version": item.Snapshot.Version}}); err != nil {
		return fmt.Errorf("record replication audit: %w", err)
	}
	return nil
}

func (s *projectionReplicationService) saveReplicationItemCursor(ctx context.Context, target string,
	item repository.ReplicableSnapshot, published int64) error {
	if item.Revision > 0 {
		if cursor, ok := s.cursors.(repository.ReplicationCursorSequenceRepository); ok {
			if fenced, generationOK := s.cursors.(repository.ReplicationCursorSequenceGenerationRepository); generationOK && s.leaseGeneration > 0 {
				return fenced.SaveCursorSequenceOwnedGeneration(ctx, s.region, target, s.worker,
					s.leaseGeneration, s.lease, item.Revision, item.Snapshot.ID, published)
			}
			if owned, ownedOK := s.cursors.(repository.ReplicationCursorSequenceLeaseRepository); ownedOK && s.leaseGeneration > 0 {
				return owned.SaveCursorSequenceOwned(ctx, s.region, target, s.worker, s.leaseGeneration, s.lease, item.Revision, item.Snapshot.ID, published)
			}
			if err := cursor.SaveCursorSequence(ctx, s.region, target, item.Revision, item.Snapshot.ID, published); err != nil {
				return fmt.Errorf("save replication cursor: %w", err)
			}
			return nil
		}
	}
	return s.saveReplicationCursor(ctx, target, item.Snapshot.UpdatedAt, published, item.Snapshot.ID)
}

//nolint:cyclop // compatibility cursor variants are intentionally selected at runtime.
func (s *projectionReplicationService) saveReplicationCursor(ctx context.Context, target string, updatedAt time.Time,
	published int64, snapshotID domain.ID) error {
	leaseRepo, fenced := s.cursors.(repository.ReplicationCursorLeaseRepository)
	if fenced {
		if generationWriter, ok := s.cursors.(repository.ReplicationCursorCheckpointGenerationRepository); ok && s.leaseGeneration > 0 {
			if err := generationWriter.SaveCursorCheckpointOwnedGeneration(ctx, s.region, target, s.worker, s.leaseGeneration, s.lease,
				updatedAt, snapshotID, published); err != nil {
				return fmt.Errorf("save replication cursor: %w", err)
			}
			return nil
		}
		if generationWriter, ok := s.cursors.(repository.ReplicationCursorGenerationSaveRepository); ok && s.leaseGeneration > 0 {
			if err := generationWriter.SaveCursorOwnedGeneration(ctx, s.region, target, s.worker,
				s.leaseGeneration, s.lease, snapshotID, published); err != nil {
				return fmt.Errorf("save replication cursor: %w", err)
			}
			return nil
		}
		if checkpointLease, ok := s.cursors.(repository.ReplicationCursorCheckpointLeaseRepository); ok {
			if err := checkpointLease.SaveCursorCheckpointOwned(ctx, s.region, target, s.worker, s.lease,
				updatedAt, snapshotID, published); err != nil {
				return fmt.Errorf("save replication cursor: %w", err)
			}
			return nil
		}
		if err := leaseRepo.SaveCursorOwned(ctx, s.region, target, s.worker, s.lease, snapshotID, published); err != nil {
			return fmt.Errorf("save replication cursor: %w", err)
		}
		return nil
	}
	if checkpointRepo, ok := s.cursors.(repository.ReplicationCursorCheckpointRepository); ok {
		if err := checkpointRepo.SaveCursorCheckpoint(ctx, s.region, target, updatedAt, snapshotID, published); err != nil {
			return fmt.Errorf("save replication cursor: %w", err)
		}
		return nil
	}
	if err := s.cursors.SaveCursor(ctx, s.region, target, snapshotID, published); err != nil {
		return fmt.Errorf("save replication cursor: %w", err)
	}
	return nil
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
					observeReplicationFailure(s.metrics, replicationFailureClass(err))
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
// event id is deterministic per tenant/account/dataset projection revision so
// identical re-publishes are receipt-deduped while same-version erasure or
// in-place data changes remain observable.
func replicationEnvelope(sourceRegion, tenantID string, snapshot *domain.AccountSnapshot) *event.Envelope {
	return replicationEnvelopeAtRevision(sourceRegion, tenantID, snapshot, 0)
}

func replicationEnvelopeAtRevision(sourceRegion, tenantID string, snapshot *domain.AccountSnapshot, revision int64) *event.Envelope {
	payload := map[string]any{
		"account_id": snapshot.AccountID.String(),
		"dataset":    string(snapshot.Dataset),
		"status":     string(snapshot.Status),
		"version":    float64(snapshot.Version),
		"data":       event.RedactPayload(snapshot.Data),
	}
	if !snapshot.GeneratedAt.IsZero() {
		payload["generated_at"] = canonicalSnapshotTime(snapshot.GeneratedAt)
	}
	if revision > 0 {
		payload["revision"] = float64(revision)
	}
	revisionPart := snapshotRevision(snapshot)
	if revision > 0 {
		revisionPart = fmt.Sprintf("log:%d", revision)
	}
	stable := uuid.NewSHA1(projectionReplicationNamespace, []byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s",
		tenantID, "aero-id", snapshot.AccountID.String(), snapshot.Dataset, revisionPart)))
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

func snapshotRevision(snapshot *domain.AccountSnapshot) string {
	if snapshot == nil {
		return "nil"
	}
	encoded, err := json.Marshal(map[string]any{
		"version": snapshot.Version, "status": snapshot.Status,
		"generated_at": canonicalSnapshotTime(snapshot.GeneratedAt),
		"updated_at":   canonicalSnapshotTime(snapshot.UpdatedAt),
		"data":         event.RedactPayload(snapshot.Data),
	})
	if err != nil {
		encoded = []byte(fmt.Sprintf("%d:%s:%s:%s", snapshot.Version, snapshot.Status,
			canonicalSnapshotTime(snapshot.GeneratedAt), canonicalSnapshotTime(snapshot.UpdatedAt)))
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// canonicalSnapshotTime matches PostgreSQL timestamptz precision. Event IDs
// must be stable when a caller's nanosecond timestamp is read back from the
// database, which stores timestamps at microsecond precision.
func canonicalSnapshotTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}
