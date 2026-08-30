package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

var ErrReplicationTerminalFailureNotFound = errors.New("replication terminal failure not found")

// ReplicationTerminalFailure is durable poison-work state. It deliberately
// contains identity and classification metadata only, never an event payload.
type ReplicationTerminalFailure struct {
	ID               domain.ID
	SourceRegion     string
	Target           string
	EnvelopeEventID  domain.ID
	SnapshotID       domain.ID
	TenantID         string
	AccountID        domain.ID
	Source           string
	Dataset          domain.AccountDataset
	SnapshotVersion  int64
	TerminalAttempts int
	RemoteStatus     int
	ReasonCode       string
	FirstFailedAt    time.Time
	LastFailedAt     time.Time
	State            string
	TerminalCode     string
	ParkedAt         *time.Time
	ParkingToken     domain.ID
}

type ReplicationTerminalFailureRepository interface {
	GetTerminalFailure(context.Context, string, string, domain.ID) (*ReplicationTerminalFailure, error)
	RecordTerminalAttempt(context.Context, ReplicationTerminalFailure, int) (ReplicationTerminalFailure, bool, error)
}

// ReplicationTerminalFailureFencedRepository is implemented by the production
// repository. The compatibility interface above remains usable by old fakes,
// while production terminal writes are fenced by the cursor lease generation.
type ReplicationTerminalFailureFencedRepository interface {
	ReplicationTerminalFailureRepository
	RecordTerminalAttemptOwned(context.Context, ReplicationTerminalFailure, int, string, int64) (ReplicationTerminalFailure, bool, error)
}

type replicationTerminalFailureRow struct {
	ID               uuid.UUID  `gorm:"column:id;primaryKey"`
	SourceRegion     string     `gorm:"column:source_region"`
	Target           string     `gorm:"column:target"`
	EnvelopeEventID  uuid.UUID  `gorm:"column:envelope_event_id"`
	SnapshotID       uuid.UUID  `gorm:"column:snapshot_id"`
	TenantID         string     `gorm:"column:tenant_id"`
	AccountID        uuid.UUID  `gorm:"column:account_id"`
	Source           string     `gorm:"column:source"`
	Dataset          string     `gorm:"column:dataset"`
	SnapshotVersion  int64      `gorm:"column:snapshot_version"`
	TerminalAttempts int        `gorm:"column:terminal_attempts"`
	RemoteStatus     int        `gorm:"column:remote_status"`
	ReasonCode       string     `gorm:"column:reason_code"`
	FirstFailedAt    time.Time  `gorm:"column:first_failed_at"`
	LastFailedAt     time.Time  `gorm:"column:last_failed_at"`
	State            string     `gorm:"column:state"`
	TerminalCode     string     `gorm:"column:terminal_code"`
	ParkedAt         *time.Time `gorm:"column:parked_at"`
	ParkingToken     *uuid.UUID `gorm:"column:parking_token"`
}

func (replicationTerminalFailureRow) TableName() string { return "replication_terminal_failures" }

func failureFromRow(row *replicationTerminalFailureRow) ReplicationTerminalFailure {
	failure := ReplicationTerminalFailure{ID: row.ID, SourceRegion: row.SourceRegion, Target: row.Target,
		EnvelopeEventID: row.EnvelopeEventID, SnapshotID: row.SnapshotID, TenantID: row.TenantID,
		AccountID: row.AccountID, Source: row.Source, Dataset: domain.AccountDataset(row.Dataset),
		SnapshotVersion: row.SnapshotVersion, TerminalAttempts: row.TerminalAttempts, RemoteStatus: row.RemoteStatus,
		ReasonCode: row.ReasonCode, FirstFailedAt: row.FirstFailedAt, LastFailedAt: row.LastFailedAt,
		State: row.State, TerminalCode: row.TerminalCode, ParkedAt: row.ParkedAt}
	if row.ParkingToken != nil {
		failure.ParkingToken = *row.ParkingToken
	}
	return failure
}

type replicationTerminalFailureRepo struct{ db *gorm.DB }

func NewReplicationTerminalFailureRepository(db *gorm.DB) ReplicationTerminalFailureRepository {
	return &replicationTerminalFailureRepo{db: db}
}

func (r *replicationTerminalFailureRepo) GetTerminalFailure(ctx context.Context, sourceRegion, target string,
	eventID domain.ID) (*ReplicationTerminalFailure, error) {
	var row replicationTerminalFailureRow
	err := r.db.WithContext(ctx).Where("source_region = ? AND target = ? AND envelope_event_id = ?",
		sourceRegion, target, eventID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrReplicationTerminalFailureNotFound
	}
	if err != nil {
		return nil, err
	}
	failure := failureFromRow(&row)
	return &failure, nil
}

func (r *replicationTerminalFailureRepo) RecordTerminalAttempt(ctx context.Context, failure ReplicationTerminalFailure,
	maxAttempts int) (ReplicationTerminalFailure, bool, error) {
	return r.recordTerminalAttempt(ctx, failure, maxAttempts, "", 0)
}

func (r *replicationTerminalFailureRepo) RecordTerminalAttemptOwned(ctx context.Context, failure ReplicationTerminalFailure,
	maxAttempts int, owner string, generation int64) (ReplicationTerminalFailure, bool, error) {
	return r.recordTerminalAttempt(ctx, failure, maxAttempts, owner, generation)
}

//nolint:cyclop,funlen // one SQL statement owns the atomic insert/update transition.
func (r *replicationTerminalFailureRepo) recordTerminalAttempt(ctx context.Context, failure ReplicationTerminalFailure,
	maxAttempts int, owner string, generation int64) (ReplicationTerminalFailure, bool, error) {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if failure.ID == (domain.ID{}) {
		failure.ID = idgen.NewID()
	}
	if failure.FirstFailedAt.IsZero() {
		failure.FirstFailedAt = time.Now().UTC()
	}
	if failure.LastFailedAt.IsZero() {
		failure.LastFailedAt = failure.FirstFailedAt
	}
	if failure.ParkingToken == (domain.ID{}) {
		failure.ParkingToken = idgen.NewID()
	}
	parkedAt := failure.LastFailedAt
	state := "retrying"
	var parkedAtArg any
	var parkingTokenArg any
	// Keep the per-attempt token in EXCLUDED even while retrying. It is used
	// to identify this call's retry-to-park transition in RETURNING; it is not
	// an operator credential or event payload.
	parkingTokenArg = failure.ParkingToken
	if maxAttempts <= 1 {
		state, parkedAtArg = "parked", parkedAt
	}
	var row replicationTerminalFailureRow
	// The conflict update is one atomic statement. parking_token is a
	// per-attempt marker: it lets us distinguish this call's transition from
	// a concurrent worker observing an already parked row.
	sql := `WITH guard AS (
 SELECT 1 WHERE (? = '' OR EXISTS (SELECT 1 FROM replication_cursors c WHERE c.source_region = ? AND c.target = ?
   AND c.lease_owner = ? AND c.lease_generation = ? AND c.lease_until > now()))
)
INSERT INTO replication_terminal_failures
 (id, source_region, target, envelope_event_id, snapshot_id, tenant_id, account_id, source, dataset,
  snapshot_version, terminal_attempts, remote_status, reason_code, first_failed_at, last_failed_at,
  state, terminal_code, parked_at, parking_token, created_at, updated_at)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?::timestamptz, ?::uuid, now(), now()
FROM guard
 ON CONFLICT (source_region, target, envelope_event_id) DO UPDATE SET
  terminal_attempts = CASE WHEN replication_terminal_failures.state = 'retrying'
    THEN LEAST(replication_terminal_failures.terminal_attempts + 1, ?) ELSE replication_terminal_failures.terminal_attempts END,
  remote_status = CASE WHEN replication_terminal_failures.state = 'retrying' THEN EXCLUDED.remote_status ELSE replication_terminal_failures.remote_status END,
  reason_code = CASE WHEN replication_terminal_failures.state = 'retrying' THEN EXCLUDED.reason_code ELSE replication_terminal_failures.reason_code END,
  last_failed_at = CASE WHEN replication_terminal_failures.state = 'retrying'
    THEN EXCLUDED.last_failed_at ELSE replication_terminal_failures.last_failed_at END,
  state = CASE WHEN replication_terminal_failures.state = 'retrying'
    AND replication_terminal_failures.terminal_attempts + 1 >= ? THEN 'parked' ELSE replication_terminal_failures.state END,
  terminal_code = CASE WHEN replication_terminal_failures.state = 'retrying'
    AND replication_terminal_failures.terminal_attempts + 1 >= ? THEN EXCLUDED.terminal_code ELSE replication_terminal_failures.terminal_code END,
  parked_at = CASE WHEN replication_terminal_failures.state = 'retrying'
    AND replication_terminal_failures.terminal_attempts + 1 >= ? THEN COALESCE(EXCLUDED.parked_at, now()) ELSE replication_terminal_failures.parked_at END,
  parking_token = CASE WHEN replication_terminal_failures.state = 'retrying'
    AND replication_terminal_failures.terminal_attempts + 1 >= ?
    THEN COALESCE(EXCLUDED.parking_token, gen_random_uuid())
    ELSE replication_terminal_failures.parking_token END,
  updated_at = CASE WHEN replication_terminal_failures.state = 'retrying' THEN now() ELSE replication_terminal_failures.updated_at END
 WHERE replication_terminal_failures.state = 'retrying'
   AND (? = '' OR EXISTS (SELECT 1 FROM replication_cursors c WHERE c.source_region = ? AND c.target = ?
   AND c.lease_owner = ? AND c.lease_generation = ? AND c.lease_until > now()))
 RETURNING *`
	args := []any{owner, failure.SourceRegion, failure.Target, owner, generation,
		failure.ID, failure.SourceRegion, failure.Target, failure.EnvelopeEventID, failure.SnapshotID,
		failure.TenantID, failure.AccountID, failure.Source, string(failure.Dataset), failure.SnapshotVersion,
		failure.RemoteStatus, failure.ReasonCode, failure.FirstFailedAt, failure.LastFailedAt,
		state, failure.TerminalCode, parkedAtArg, parkingTokenArg,
		maxAttempts, maxAttempts, maxAttempts, maxAttempts, maxAttempts,
		owner, failure.SourceRegion, failure.Target, owner, generation}
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&row).Error; err != nil {
		return ReplicationTerminalFailure{}, false, err
	}
	if row.ID == (uuid.UUID{}) {
		if owner != "" {
			return ReplicationTerminalFailure{}, false, ErrReplicationCursorLeaseLost
		}
		parked, lookupErr := r.GetTerminalFailure(ctx, failure.SourceRegion, failure.Target, failure.EnvelopeEventID)
		if lookupErr == nil && parked != nil {
			return *parked, false, nil
		}
		if lookupErr != nil && !errors.Is(lookupErr, ErrReplicationTerminalFailureNotFound) {
			return ReplicationTerminalFailure{}, false, lookupErr
		}
		return ReplicationTerminalFailure{}, false, ErrReplicationTerminalFailureNotFound
	}
	result := failureFromRow(&row)
	return result, result.State == "parked" && result.ParkingToken == failure.ParkingToken, nil
}

var _ ReplicationTerminalFailureFencedRepository = (*replicationTerminalFailureRepo)(nil)
