package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type EventIngestService interface {
	Ingest(context.Context, *event.Envelope) (*EventIngestResult, error)
}

// EventItemIngestService is the item-oriented contract used by gRPC. It keeps
// transport item failures in the response while Ingest remains error-oriented
// for HTTP and source adapters.
type EventItemIngestService interface {
	EventIngestService
	IngestItem(context.Context, EventIngestItem) *EventIngestResult
}

type EventIngestItem struct {
	Envelope              *event.Envelope
	CanonicalizationStage string
	CanonicalizationCode  string
	OriginalWireEventID   string
	WireFingerprint       string
}

// QuarantineDetails is retained as the service-level name for callers that
// need to describe canonicalization provenance.
type QuarantineDetails = repository.EventQuarantineDetails

type EventIngestResult struct {
	EventID   string `json:"event_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

const (
	quarantineCanonicalization = "canonicalization"
	quarantineEnvelope         = "envelope_validation"
	quarantineResidency        = "residency"
	quarantineProjection       = "projection"
	quarantineLifecycle        = "lifecycle"
)

type eventIngestService struct {
	receipts       repository.EventReceiptRepository
	audit          AuditLedgerService
	allowedRegions map[string]bool
	metrics        ControlPlaneMetrics
}

type EventIngestOption func(*eventIngestService)

func WithEventAllowedRegions(regions []string) EventIngestOption {
	return func(s *eventIngestService) {
		s.allowedRegions = make(map[string]bool, len(regions))
		for _, region := range regions {
			if strings.TrimSpace(region) != "" {
				s.allowedRegions[strings.TrimSpace(region)] = true
			}
		}
	}
}

func WithEventIngestAudit(audit AuditLedgerService) EventIngestOption {
	return func(s *eventIngestService) { s.audit = audit }
}

func WithEventIngestMetrics(metrics ControlPlaneMetrics) EventIngestOption {
	return func(s *eventIngestService) {
		if metrics != nil {
			s.metrics = metrics
		}
	}
}

func NewEventIngestService(receipts repository.EventReceiptRepository, opts ...EventIngestOption) EventIngestService {
	service := &eventIngestService{receipts: receipts, metrics: noopControlPlaneMetrics{}}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// Ingest preserves the original error-oriented contract. A successful
// quarantine is returned as the deterministic source error to HTTP/NATS.
func (s *eventIngestService) Ingest(ctx context.Context, envelope *event.Envelope) (*EventIngestResult, error) {
	result, err := s.ingestEnvelope(ctx, envelope, repository.EventQuarantineDetails{})
	if err != nil {
		return nil, err
	}
	if result.Status == string(repository.EventQuarantined) {
		return nil, deterministicQuarantineError(result.ErrorCode)
	}
	return result, nil
}

func (s *eventIngestService) IngestItem(ctx context.Context, item EventIngestItem) *EventIngestResult {
	result := &EventIngestResult{EventID: ingestResultID(item)}
	if item.Envelope == nil {
		return rejectedEventResult(result, "event.invalid")
	}
	if item.Envelope.EventID == (domain.ID{}) {
		if item.WireFingerprint == "" {
			return rejectedEventResult(result, "event.invalid")
		}
		item.Envelope.EventID = idgen.Anchor("event.quarantine.v1", item.WireFingerprint)
		result.EventID = item.Envelope.EventID.String()
	}
	if item.CanonicalizationCode != "" {
		if strings.TrimSpace(item.Envelope.TenantID) == "" {
			return rejectedEventResult(result, "event.invalid")
		}
		details := quarantineDetails(item)
		if err := s.quarantineEvent(ctx, item.Envelope, item.CanonicalizationCode, details); err != nil {
			return failedEventResult(result, err)
		}
		s.observeQuarantine(item.Envelope, quarantineStage(item.CanonicalizationStage), item.CanonicalizationCode)
		result.Status, result.ErrorCode = string(repository.EventQuarantined), item.CanonicalizationCode
		return result
	}
	processed, err := s.ingestEnvelope(ctx, item.Envelope, quarantineDetails(item))
	if err != nil {
		return failedEventResult(result, err)
	}
	return processed
}

func (s *eventIngestService) ingestEnvelope(ctx context.Context, envelope *event.Envelope,
	details repository.EventQuarantineDetails) (*EventIngestResult, error) {
	if err := validateIngestEnvelope(envelope); err != nil {
		if !trustedQuarantineContext(envelope, details) {
			return nil, err
		}
		code := appErrorCode(err, "event.invalid")
		if qerr := s.quarantineEvent(ctx, envelope, code, detailsForStage(details, quarantineEnvelope)); qerr != nil {
			return nil, qerr
		}
		s.observeQuarantine(envelope, quarantineStage(quarantineEnvelope), code)
		return quarantinedEventResult(envelope, code), nil
	}
	if !s.regionAllowed(envelope.SourceRegion) {
		code := "event.region_not_allowed"
		if err := s.quarantineEvent(ctx, envelope, code, detailsForStage(details, quarantineResidency)); err != nil {
			return nil, err
		}
		s.observeQuarantine(envelope, quarantineResidency, code)
		return quarantinedEventResult(envelope, code), nil
	}
	mutation, err := envelope.Snapshot()
	if err != nil {
		code := "event.invalid_projection"
		if qerr := s.quarantineEvent(ctx, envelope, code, detailsForStage(details, quarantineProjection)); qerr != nil {
			return nil, qerr
		}
		s.observeQuarantine(envelope, quarantineProjection, code)
		return quarantinedEventResult(envelope, code), nil
	}
	return s.apply(ctx, envelope, mutation)
}

func (s *eventIngestService) apply(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation) (*EventIngestResult, error) {
	result, atomicAudit, err := s.applySnapshot(ctx, envelope, mutation)
	if err != nil {
		return nil, mapEventApplyError(err)
	}
	s.observeEventOutcome(envelope, mutation, result)
	if result.Status == "" {
		return nil, apperr.Internal("event.apply_failed", "event apply returned empty status")
	}
	if !atomicAudit && s.audit != nil {
		if err := s.recordEventOutcomeAudit(ctx, envelope, mutation, result); err != nil {
			return nil, apperr.Internal("event.audit_failed", "failed to record event audit").Wrap(err)
		}
	}
	return &EventIngestResult{EventID: envelope.EventID.String(), Status: string(result.Status), ErrorCode: result.ErrorCode}, nil
}

func (s *eventIngestService) regionAllowed(sourceRegion string) bool {
	if len(s.allowedRegions) == 0 {
		return true
	}
	return s.allowedRegions[sourceRegion]
}

func trustedQuarantineContext(envelope *event.Envelope, details repository.EventQuarantineDetails) bool {
	return envelope != nil && strings.TrimSpace(envelope.TenantID) != "" &&
		(envelope.EventID != (domain.ID{}) || details.WireFingerprint != "")
}

func validateIngestEnvelope(envelope *event.Envelope) error {
	if envelope == nil || strings.TrimSpace(envelope.TenantID) == "" {
		return apperr.Forbidden("event.tenant_required", "verified event tenant is required")
	}
	if err := envelope.Validate(); err != nil {
		return apperr.BadRequest("event.invalid", "event envelope is invalid").Wrap(err)
	}
	return nil
}

func (s *eventIngestService) quarantineEvent(ctx context.Context, envelope *event.Envelope,
	code string, details repository.EventQuarantineDetails) error {
	if envelope == nil || strings.TrimSpace(envelope.TenantID) == "" {
		return apperr.Internal("event.audit_failed", "quarantine context is not configured")
	}
	if s.receipts == nil {
		return apperr.Internal("event.quarantine_failed", "event receipt repository is not configured")
	}
	// The receipt is always attempted first. In particular, a missing audit
	// sink must not turn a receipt failure into an audit-looking failure.
	if err := s.persistQuarantineReceipt(ctx, envelope, code, details); err != nil {
		return err
	}
	if s.audit == nil {
		return apperr.Internal("event.audit_failed", "quarantine audit is not configured")
	}
	if err := s.recordQuarantineAudit(ctx, envelope, code, details); err != nil {
		return apperr.Internal("event.audit_failed", "failed to record event audit").Wrap(err)
	}
	return nil
}

func (s *eventIngestService) persistQuarantineReceipt(ctx context.Context, envelope *event.Envelope,
	code string, details repository.EventQuarantineDetails) error {
	var err error
	if detailed, ok := s.receipts.(repository.EventQuarantineRepository); ok {
		err = detailed.QuarantineEventWithDetails(ctx, envelope, code, details)
	} else if detailed, ok := s.receipts.(interface {
		QuarantineEventDetailed(context.Context, *event.Envelope, string, repository.EventQuarantineDetails) error
	}); ok {
		err = detailed.QuarantineEventDetailed(ctx, envelope, code, details)
	} else {
		err = s.receipts.QuarantineEvent(ctx, envelope, code)
	}
	if err != nil {
		return s.mapQuarantineReceiptError(err, details.Stage)
	}
	return nil
}

func (s *eventIngestService) mapQuarantineReceiptError(err error, stage string) error {
	failureCode := "event.quarantine_failed"
	if errors.Is(err, repository.ErrEventIntegrityConflict) {
		failureCode = "event.integrity_conflict"
	} else if errors.Is(err, repository.ErrEventReceiptBusy) {
		failureCode = "event.in_flight"
	}
	if observer, ok := s.metrics.(EventQuarantineMetrics); ok {
		observer.ObserveEventQuarantineFailure(quarantineStage(stage), failureCode)
	}
	if failureCode == "event.integrity_conflict" {
		return apperr.Conflict(failureCode, "event was rejected").Wrap(err)
	}
	if failureCode == "event.in_flight" {
		return apperr.Conflict(failureCode, "event is already being processed").Wrap(err)
	}
	return apperr.Internal(failureCode, "failed to quarantine event").Wrap(err)
}

func (s *eventIngestService) recordQuarantineAudit(ctx context.Context, envelope *event.Envelope,
	code string, details repository.EventQuarantineDetails) error {
	details.Stage = quarantineStage(details.Stage)
	input := eventAuditInputWithDetails(event.BoundedEnvelope(envelope), nil, "quarantined", code, details.Stage, details)
	return s.recordAuditInput(ctx, input)
}

func (s *eventIngestService) observeQuarantine(envelope *event.Envelope, stage, _ string) {
	stored := event.BoundedEnvelope(envelope)
	dataset := stored.Source + "." + stored.EventType
	if len(dataset) > event.MaxAggregateTypeBytes+event.MaxEventTypeBytes+1 {
		dataset = "unknown"
	}
	if observer, ok := s.metrics.(EventQuarantineMetrics); ok {
		observer.ObserveEventQuarantinedAtStage(dataset, stage)
		return
	}
	s.metrics.ObserveEventQuarantined(dataset)
}

func (s *eventIngestService) applySnapshot(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation) (repository.EventApplyResult, bool, error) {
	if s.audit != nil {
		if applier, ok := s.receipts.(repository.EventSnapshotAuditRepository); ok {
			result, err := applier.ApplySnapshotEventWithAudit(ctx, envelope, mutation,
				buildAuditFact(ctx, eventAuditInput(envelope, mutation, "applied")))
			return result, true, err
		}
	}
	result, err := s.receipts.ApplySnapshotEvent(ctx, envelope, mutation)
	return result, false, err
}

func (s *eventIngestService) recordAudit(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation, status string) error {
	return s.recordAuditInput(ctx, eventAuditInput(envelope, mutation, status))
}

// audit-site: standalone-fact — source event outcome/quarantine facts are
// appended after the receipt commit and deduplicated by the ledger.
func (s *eventIngestService) recordAuditInput(ctx context.Context, input AuditFactInput) error {
	if s.audit == nil {
		return apperr.Internal("event.audit_failed", "audit ledger is not configured")
	}
	return s.audit.Record(ctx, input)
}

func eventAuditInput(envelope *event.Envelope, mutation *event.SnapshotMutation, status string) AuditFactInput {
	return eventAuditInputWithDetails(envelope, mutation, status, "", "", repository.EventQuarantineDetails{})
}

func eventAuditInputWithDetails(envelope *event.Envelope, mutation *event.SnapshotMutation,
	status, code, stage string, details repository.EventQuarantineDetails) AuditFactInput {
	input := AuditFactInput{Source: envelope.Source, SourceRegion: envelope.SourceRegion, EventID: envelope.EventID,
		OperationID: envelope.OperationID, CorrelationID: envelope.CorrelationID, CausationID: envelope.CausationID,
		TraceID: envelope.TraceID, ActorUID: envelope.ActorUID, TenantID: envelope.TenantID, Action: "source.event",
		ResourceType: envelope.AggregateType, ResourceID: envelope.AggregateID, OccurredAt: envelope.OccurredAt,
		Payload: map[string]any{"event_type": envelope.EventType, "schema_version": envelope.SchemaVersion,
			"aggregate_version": envelope.AggregateVersion, "status": status}}
	if code != "" {
		input.Payload["error_code"] = code
	}
	if stage != "" {
		input.Payload["stage"] = stage
	}
	addQuarantineAuditMetadata(input.Payload, envelope, details, stage)
	if mutation != nil {
		input.AccountID = mutation.AccountID
	}
	return input
}

func addQuarantineAuditMetadata(payload map[string]any, envelope *event.Envelope,
	details repository.EventQuarantineDetails, stage string) {
	if details.Stage == "" && details.OriginalWireID == "" && details.WireFingerprint == "" {
		return
	}
	identityKind := repository.QuarantineIdentityKind(envelope, details)
	quarantine := map[string]any{"identity_kind": identityKind, "stage": quarantineStage(stage)}
	if details.OriginalWireID != "" {
		quarantine["wire_event_id"] = boundedWireID(details.OriginalWireID)
	}
	if details.WireFingerprint != "" {
		quarantine["wire_fingerprint"] = details.WireFingerprint
	}
	payload["_quarantine"] = quarantine
}

func quarantineDetails(item EventIngestItem) repository.EventQuarantineDetails {
	return repository.EventQuarantineDetails{Stage: item.CanonicalizationStage, OriginalWireID: boundedWireID(item.OriginalWireEventID),
		WireFingerprint: item.WireFingerprint}
}

func boundedWireID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, value)
	if len(value) <= 256 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return value[:239] + "#" + hex.EncodeToString(sum[:])[:16]
}

func detailsForStage(details repository.EventQuarantineDetails, stage string) repository.EventQuarantineDetails {
	details.Stage = stage
	return details
}

func quarantineStage(stage string) string {
	switch stage {
	case quarantineCanonicalization, quarantineEnvelope, quarantineResidency, quarantineProjection, quarantineLifecycle:
		return stage
	default:
		return quarantineEnvelope
	}
}

func quarantinedEventResult(envelope *event.Envelope, code string) *EventIngestResult {
	return &EventIngestResult{EventID: envelope.EventID.String(), Status: string(repository.EventQuarantined), ErrorCode: code}
}

func deterministicQuarantineError(code string) error {
	switch code {
	case "event.region_not_allowed":
		return apperr.Forbidden(code, "event source region violates residency policy")
	case "event.account_erased":
		return apperr.Forbidden(code, "event target data is erased")
	case "event.invalid_projection":
		return apperr.BadRequest(code, "event cannot update an approved projection")
	default:
		return apperr.BadRequest(code, "event was quarantined")
	}
}

func appErrorCode(err error, fallback string) string {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) && appErr.Code != "" {
		if appErr.Code == "event.invalid_id" {
			return appErr.Code
		}
		if appErr.Code == "event.invalid" {
			return appErr.Code
		}
	}
	return fallback
}

func ingestResultID(item EventIngestItem) string {
	if item.Envelope != nil && item.Envelope.EventID != (domain.ID{}) {
		return item.Envelope.EventID.String()
	}
	// Never echo an attacker-controlled malformed wire ID. The gRPC handler
	// derives a surrogate before calling IngestItem; direct callers without a
	// surrogate get an intentionally empty result ID.
	return ""
}

func rejectedEventResult(result *EventIngestResult, code string) *EventIngestResult {
	result.Status, result.ErrorCode = "rejected", code
	return result
}

func failedEventResult(result *EventIngestResult, err error) *EventIngestResult {
	result.Status, result.Retryable = "failed", true
	result.ErrorCode = "event.ingest_failed"
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code == "" {
		return result
	}
	// Item responses are an information boundary: only this fixed vocabulary
	// is exposed, and event/account existence conflicts are intentionally
	// collapsed so they cannot be used as oracles.
	switch appErr.Code {
	case "event.integrity_conflict", "event.tenant_mismatch":
		result.Status, result.ErrorCode, result.Retryable = "rejected", "event.rejected", false
	case "event.in_flight":
		result.ErrorCode = appErr.Code
	case "event.quarantine_failed", "event.audit_failed", "event.apply_failed", "event.ingest_failed":
		result.ErrorCode = appErr.Code
	default:
		if appErr.HTTPStatus() < 500 {
			result.Status, result.ErrorCode, result.Retryable = "rejected", "event.rejected", false
		}
	}
	return result
}

func mapEventApplyError(err error) error {
	if errors.Is(err, repository.ErrEventIntegrityConflict) {
		return apperr.Conflict("event.integrity_conflict", "event id is already bound to different content").Wrap(err)
	}
	if errors.Is(err, repository.ErrEventReceiptBusy) {
		return apperr.Conflict("event.in_flight", "event is already being processed").Wrap(err)
	}
	if errors.Is(err, repository.ErrEventTenantMismatch) {
		return apperr.Forbidden("event.tenant_mismatch", "event target is not in the authenticated tenant")
	}
	return apperr.Internal("event.apply_failed", "failed to apply event").Wrap(err)
}
