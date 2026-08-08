package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/google/uuid"
)

// replayDefaultLimit bounds one requeue batch (publication and delivery legs)
// when no explicit limit is supplied; -id overrides it (REQ-1.2).
const replayDefaultLimit = 100

// purgeDefaultLimit bounds one purge scan batch per tick.
const purgeDefaultLimit = 50

// ReplayIntent is the operator intent for one requeue call (per kind). The
// repository mints one audit fact per DISTINCT affected tenant, in the same
// transaction as the reset; every fact of one CLI run shares OperationID. The
// fact tenant always derives from the rows (ledger/subscription join), never
// from a flag — mis-attribution is structurally impossible.
type ReplayIntent struct {
	// OperationID is one fresh v7 per CLI run, shared by every leg and tenant.
	OperationID domain.ID
	// ActorUID is the operator identity recorded on the facts (required,
	// trimmed, <= 256 bytes; CLI-validated).
	ActorUID string
	// TenantFilter narrows the selection to rows whose tenant equals this
	// value ('' = all tenants). The fact tenant still derives from the rows.
	TenantFilter string
	// AllowedTenants is the binding allow-list (keys of
	// cfg.AuditGovernance.Bindings when governance is enabled); nil means no
	// constraint (governance disabled). 'legacy' is always allowed, mirroring
	// the boot coverage gate's exclusions (internal/app/audit.go).
	AllowedTenants []string
	// Envelope is the audit-fact template built through the service's shared
	// buildAuditFact path: Action/ResourceType fixed per kind, EventID and ID
	// zero, TenantID empty, PartitionKey the bare (tenant-less) base. The
	// repository clones it once per affected tenant, mints a fresh v7 EventID
	// per (run, leg, tenant), and fills TenantID/PartitionKey/ResourceID/
	// CausationID plus the allow-listed payload.
	Envelope domain.AuditFact
}

// Replay sentinels (package-level, alongside ErrAuditDuplicate).
var (
	// ErrAuditFactRequired rejects a zero/incomplete intent: a mutating
	// requeue must carry a complete, attributable operator intent (fail-
	// closed — the landed un-audited default is deleted).
	ErrAuditFactRequired = errors.New("audit replay: requeue intent is required")
	// ErrAuditReplayUnattributableTenant rejects rows whose ledger/subscription
	// tenant is empty: no fact can be attributed, so the whole batch rolls
	// back (fail-closed; the message lists the offending row ids).
	ErrAuditReplayUnattributableTenant = errors.New("audit replay: dead-letter rows without a tenant cannot be attributed")
	// ErrAuditReplayTenantNotBound rejects affected tenants outside the
	// binding allow-list (and not 'legacy').
	ErrAuditReplayTenantNotBound = errors.New("audit replay: affected tenant is not bound")
)

// requeueReset maps a dead-letter row back to pending with a full fresh retry
// budget. attempt is reset to 0 (B2-5 A7), next_attempt_at to the app clock
// (scheduling only — fencing decisions stay DB-clock), the lease is cleared
// and error_code marks the operator recovery so drains and dashboards can
// distinguish manual replay from original failures.
func requeueReset() map[string]any {
	return map[string]any{
		"status": "pending", "attempt": 0, "next_attempt_at": time.Now().UTC(),
		"lease_owner": "", "lease_until": nil, "error_code": "manual_replay",
		"updated_at": time.Now().UTC(),
	}
}

// requeueRow is one locked selection row decorated with its tenant and prior
// values — the construction inputs for the fact payload.
type requeueRow struct {
	ID        domain.ID
	FactID    domain.ID // ledger fact (publications) or subscription (deliveries)
	TenantID  string
	ErrorCode string
	Attempt   int
}

// validateReplayIntent fails closed when the operator intent is absent: a
// mutating requeue without an operation id, actor, or per-kind envelope is a
// programming error, never a silently un-audited commit.
func validateReplayIntent(intent ReplayIntent) error {
	if intent.OperationID == (domain.ID{}) || strings.TrimSpace(intent.ActorUID) == "" || intent.Envelope.Action == "" {
		return ErrAuditFactRequired
	}
	return nil
}

// RequeuePublication recovers operator-selected dead-lettered audit
// publications (B2-5 REQ-1). Selection is status-scoped ('dead_letter' only)
// and bounded: a non-empty ids set selects exactly those rows (limit ignored),
// otherwise the oldest limit rows by (next_attempt_at, id); a non-empty
// TenantFilter narrows the selection to rows whose ledger fact carries that
// tenant. The reset UPDATE and one audit fact per DISTINCT affected tenant
// commit in one transaction; a zero/incomplete intent fails closed with
// ErrAuditFactRequired before any DB access, rows without a tenant roll the
// whole batch back with ErrAuditReplayUnattributableTenant, and tenants
// outside AllowedTenants (non-'legacy') roll back with
// ErrAuditReplayTenantNotBound.
//
// The selection is locked with FOR UPDATE (two-statement shape: the locked
// statement joins nothing, so ledger rows are never locked), which makes the
// fact payload (count/publication_ids/prior values) accurate by lock-hold: the
// repo asserts RowsAffected == len(selection) in-tx and aborts rather than
// mint a fact whose ids disagree with the rows they claim. Idempotent by the
// dead_letter status predicate: a second run affects 0 rows and mints no
// facts.
func (r *auditLedgerRepo) RequeuePublication(ctx context.Context, ids []domain.ID, limit int, intent ReplayIntent) (int, []*domain.AuditFact, error) {
	if err := validateReplayIntent(intent); err != nil {
		return 0, nil, err
	}
	if limit <= 0 {
		limit = replayDefaultLimit
	}
	requeued := 0
	facts := []*domain.AuditFact{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		requeued, facts, err = requeuePublicationRows(ctx, tx, r.AppendInTx, ids, limit, intent)
		return err
	})
	if err != nil {
		return 0, nil, err
	}
	return requeued, facts, nil
}

// requeuePublicationRows is the publication-leg transaction body: locked
// selection → tenant decorator → binding check → reset UPDATE → in-tx
// per-tenant facts.
func requeuePublicationRows(ctx context.Context, tx *gorm.DB, appendInTx func(context.Context, *gorm.DB, *domain.AuditFact) error,
	ids []domain.ID, limit int, intent ReplayIntent) (int, []*domain.AuditFact, error) {
	locked, err := selectDeadLetterPublicationIDs(tx, ids, limit, intent.TenantFilter, true)
	if err != nil {
		return 0, nil, err
	}
	rows, err := publicationRequeueRows(tx, locked)
	if err != nil {
		return 0, nil, err
	}
	if err := checkReplayTenants(rows, intent.AllowedTenants); err != nil {
		return 0, nil, err
	}
	requeued, err := updateRequeuedRows(tx, &auditPublicationRow{}, locked)
	if err != nil {
		return 0, nil, err
	}
	if requeued != len(locked) {
		return 0, nil, fmt.Errorf("audit replay: requeue affected %d rows but locked selection is %d (programming error)",
			requeued, len(locked))
	}
	if requeued == 0 {
		return 0, []*domain.AuditFact{}, nil
	}
	facts, err := appendRequeueFacts(ctx, appendInTx, tx, intent, rows, replayScope(ids, limit), "publication_ids",
		func(row requeueRow) domain.ID { return row.FactID })
	if err != nil {
		return 0, nil, err
	}
	return requeued, facts, nil
}

// deadLetterPublicationSelection builds the status-scoped selection core
// shared by the requeue, bounded-count, and tenant-report paths: exact ids
// when non-empty (limit ignored), otherwise the oldest limit rows by
// (next_attempt_at, id). A non-empty tenantFilter narrows via the ledger
// fact's tenant using EXISTS — the locked statement joins nothing, so
// FOR UPDATE (forUpdate) never locks ledger rows.
func deadLetterPublicationSelection(tx *gorm.DB, ids []domain.ID, limit int, tenantFilter string, forUpdate bool) *gorm.DB {
	query := tx.Model(&auditPublicationRow{}).Where("status = 'dead_letter'")
	if tenantFilter != "" {
		query = query.Where("EXISTS (SELECT 1 FROM audit_ledger l WHERE l.id = audit_publications.fact_id AND l.tenant_id = ?)", tenantFilter)
	}
	if len(ids) > 0 {
		query = query.Where("id IN ?", ids)
	} else {
		query = query.Order("next_attempt_at ASC, id ASC").Limit(limit)
	}
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	return query.Select("id")
}

func selectDeadLetterPublicationIDs(tx *gorm.DB, ids []domain.ID, limit int, tenantFilter string, forUpdate bool) ([]domain.ID, error) {
	var rows []auditPublicationRow
	if err := deadLetterPublicationSelection(tx, ids, limit, tenantFilter, forUpdate).Find(&rows).Error; err != nil {
		return nil, err
	}
	rowIDs := make([]domain.ID, 0, len(rows))
	for _, row := range rows {
		rowIDs = append(rowIDs, domain.ID(row.ID))
	}
	return rowIDs, nil
}

// publicationRequeueRows decorates the locked selection with the ledger
// tenant and the prior values the fact payload is built from. The rows are
// locked by the caller, so the re-read is stable; the join locks nothing
// (fact_id is immutable).
func publicationRequeueRows(tx *gorm.DB, rowIDs []domain.ID) ([]requeueRow, error) {
	if len(rowIDs) == 0 {
		return nil, nil
	}
	var rows []struct {
		ID        uuid.UUID `gorm:"column:id"`
		FactID    uuid.UUID `gorm:"column:fact_id"`
		TenantID  string    `gorm:"column:tenant_id"`
		ErrorCode string    `gorm:"column:error_code"`
		Attempt   int       `gorm:"column:attempt"`
	}
	if err := tx.Table("audit_publications p").
		Select("p.id, p.error_code, p.attempt, l.id AS fact_id, l.tenant_id").
		Joins("JOIN audit_ledger l ON l.id = p.fact_id").
		Where("p.id IN ?", rowIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]requeueRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, requeueRow{ID: domain.ID(row.ID), FactID: domain.ID(row.FactID),
			TenantID: row.TenantID, ErrorCode: row.ErrorCode, Attempt: row.Attempt})
	}
	return out, nil
}

// checkReplayTenants enforces the two attribution invariants on the ACTUAL
// locked selection (stronger than any pre-flight): empty tenants are
// unattributable (fail-closed), and every non-'legacy' tenant must be in the
// binding allow-list when one is supplied.
func checkReplayTenants(rows []requeueRow, allowed []string) error {
	var unattributable []string
	for _, row := range rows {
		if strings.TrimSpace(row.TenantID) == "" {
			unattributable = append(unattributable, row.ID.String())
		}
	}
	if len(unattributable) > 0 {
		return fmt.Errorf("%w: row ids %s", ErrAuditReplayUnattributableTenant, strings.Join(unattributable, ","))
	}
	if allowed == nil {
		return nil
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, tenant := range allowed {
		allowedSet[tenant] = true
	}
	var unbound []string
	for _, row := range rows {
		if row.TenantID != "legacy" && !allowedSet[row.TenantID] {
			unbound = append(unbound, row.TenantID)
		}
	}
	if len(unbound) > 0 {
		return fmt.Errorf("%w: %s", ErrAuditReplayTenantNotBound, strings.Join(unbound, ","))
	}
	return nil
}

// replayScope records how the selection was bounded for the fact payload.
func replayScope(ids []domain.ID, limit int) string {
	if len(ids) > 0 {
		return "ids"
	}
	return fmt.Sprintf("limit:%d", limit)
}

// updateRequeuedRows applies the requeue reset to the selected rows and
// returns how many were updated (idempotent: only dead_letter rows match).
func updateRequeuedRows(tx *gorm.DB, model any, rowIDs []domain.ID) (int, error) {
	if len(rowIDs) == 0 {
		return 0, nil
	}
	result := tx.Model(model).Where("status = 'dead_letter' AND id IN ?", rowIDs).Updates(requeueReset())
	if result.Error != nil {
		return 0, result.Error
	}
	return int(result.RowsAffected), nil
}

// appendRequeueFacts mints one audit fact per DISTINCT affected tenant and
// appends them all in the caller's transaction (tenants processed in sorted
// order for a deterministic advisory-lock order). Each fact gets a fresh v7
// EventID per (run, leg, tenant) — the C2 doctrine: the dedup key
// (tenant_id, source, event_id) can never collide across re-runs. The payload
// is construction-only from the locked selection's allow-listed columns.
func appendRequeueFacts(ctx context.Context, appendInTx func(context.Context, *gorm.DB, *domain.AuditFact) error,
	tx *gorm.DB, intent ReplayIntent, rows []requeueRow, scope, idsKey string, causer func(requeueRow) domain.ID) ([]*domain.AuditFact, error) {
	groups := groupRequeueRows(rows)
	facts := make([]*domain.AuditFact, 0, len(groups))
	for _, tenant := range sortedTenantKeys(groups) {
		group := groups[tenant]
		fact := cloneReplayEnvelope(intent, tenant)
		fact.EventID = idgen.NewID() //nolint:gosec // fresh v7 per (run, leg, tenant) — C2 allowlist
		fact.CorrelationID = intent.OperationID
		if len(group) == 1 {
			// Single-row tenant group: the causation link is the original
			// ledger fact (publications) / subscription (deliveries). Batch
			// linkage for multi-row groups lives in the payload's id list.
			fact.CausationID = causer(group[0])
			fact.ResourceID = causer(group[0]).String()
		}
		fact.Payload = replayPayload(idsKey, group, scope)
		// ErrAuditDuplicate folds in-tx only: a duplicate append implies the
		// requeue already committed with identical anchor inputs; the swallow
		// runs after the UPDATE inside the same tx, so it can never mask a
		// failed requeue. Fresh v7 EventIDs make it practically unreachable.
		if err := appendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return nil, fmt.Errorf("append requeue audit: %w", err)
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// cloneReplayEnvelope copies the service-built envelope template and fills the
// per-tenant fields; ID is zeroed so AppendInTx mints a fresh ledger row PK
// per append (the template's ID would otherwise collide across tenants).
func cloneReplayEnvelope(intent ReplayIntent, tenant string) *domain.AuditFact {
	fact := intent.Envelope
	fact.ID = domain.ID{}
	fact.EventID = domain.ID{}
	fact.OperationID = intent.OperationID
	fact.TenantID = tenant
	fact.PartitionKey = tenant + ":" + fact.PartitionKey
	fact.Payload = map[string]any{}
	return &fact
}

// replayPayload is the allow-listed fact payload: exactly these five keys,
// constructed from the locked selection's columns, nothing else. Bounded at
// limit ids (~3.6KB at the default 100) — far below the 64KB ledger guard, so
// no truncation is possible by construction. Never: row payloads, lease
// owners, endpoints, credentials, stack traces.
func replayPayload(idsKey string, rows []requeueRow, scope string) map[string]any {
	ids := make([]string, 0, len(rows))
	codeSet := map[string]struct{}{}
	priorAttempt := 0
	for _, row := range rows {
		ids = append(ids, row.ID.String())
		codeSet[row.ErrorCode] = struct{}{}
		if row.Attempt > priorAttempt {
			priorAttempt = row.Attempt
		}
	}
	codes := make([]string, 0, len(codeSet))
	for code := range codeSet {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return map[string]any{
		"count":            len(rows),
		idsKey:             ids,
		"scope":            scope,
		"prior_error_code": strings.Join(codes, ","),
		"prior_attempt":    priorAttempt,
	}
}

func groupRequeueRows(rows []requeueRow) map[string][]requeueRow {
	groups := map[string][]requeueRow{}
	for _, row := range rows {
		groups[row.TenantID] = append(groups[row.TenantID], row)
	}
	return groups
}

func sortedTenantKeys(groups map[string][]requeueRow) []string {
	keys := make([]string, 0, len(groups))
	for tenant := range groups {
		keys = append(keys, tenant)
	}
	sort.Strings(keys)
	return keys
}

// CountDeadLetterPublications returns exactly the number of rows the next
// RequeuePublication call would affect (min(backlog, limit), ids- or
// tenant-filtered): the count is bounded by the same selection core, so the
// dry-run estimate can never overstate the mutating run by an order of
// magnitude (GORM's Count ignores LIMIT on count(*); the bounded form counts
// the derived id-selection table).
func (r *auditLedgerRepo) CountDeadLetterPublications(ctx context.Context, ids []domain.ID, limit int, tenantFilter string) (int, error) {
	if limit <= 0 {
		limit = replayDefaultLimit
	}
	sub := deadLetterPublicationSelection(r.db.WithContext(ctx), ids, limit, tenantFilter, false)
	var count int64
	if err := r.db.WithContext(ctx).Table("(?) AS s", sub).Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// DeadLetterTenants returns the distinct tenants of the rows the next
// RequeuePublication call would affect (dry-run tenant report and friendly
// binding pre-flight; the authoritative check stays in-tx).
func (r *auditLedgerRepo) DeadLetterTenants(ctx context.Context, ids []domain.ID, limit int, tenantFilter string) ([]string, error) {
	if limit <= 0 {
		limit = replayDefaultLimit
	}
	sub := deadLetterPublicationSelection(r.db.WithContext(ctx), ids, limit, tenantFilter, false)
	var tenants []string
	err := r.db.WithContext(ctx).Table("audit_publications p").
		Select("DISTINCT l.tenant_id").
		Joins("JOIN audit_ledger l ON l.id = p.fact_id").
		Where("p.id IN (?)", sub).
		Order("l.tenant_id ASC").
		Scan(&tenants).Error
	if err != nil {
		return nil, err
	}
	return tenants, nil
}

// PurgeDeadLetterPublications deletes dead_letter publications past the
// retention cutoff (B2-5 REQ-4.2). The predicate excludes leased rows
// (lease_owner != ” or an unexpired lease_until), is bounded per call and
// ordered by (updated_at, id) so the cutoff is an index range bound on
// idx_audit_publications_purge. The outer status qual is re-checked by the
// EPQ machinery against the NEW row version, so a row concurrently requeued
// to pending survives the DELETE; FOR UPDATE SKIP LOCKED in the subquery
// makes the purge skip rows locked by an in-flight requeue instead of
// blocking on them.
func (r *auditLedgerRepo) PurgeDeadLetterPublications(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = purgeDefaultLimit
	}
	sub := r.db.WithContext(ctx).Model(&auditPublicationRow{}).
		Where("status = 'dead_letter' AND lease_owner = '' AND (lease_until IS NULL OR lease_until < now()) AND updated_at < ?", cutoff).
		Order("updated_at ASC, id ASC").
		Select("id").
		Limit(limit).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked})
	result := r.db.WithContext(ctx).
		Where("id IN (?) AND status = 'dead_letter'", sub).
		Delete(&auditPublicationRow{})
	if result.Error != nil {
		return 0, fmt.Errorf("purge dead-letter publications: %w", result.Error)
	}
	return result.RowsAffected, nil
}
