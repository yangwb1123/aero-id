package http

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type AuditLedgerHandler struct {
	audit      service.AuditLedgerService
	holds      service.AuditHoldService  // optional; nil disables legal-hold routes (P4-07)
	anchor     service.AuditAnchorService // optional; nil disables anchor routes (P4-03)
	jobs       service.SyncJobService     // optional; nil disables the audit export route (P4-08)
	permission middleware.PermissionChecker
}

func NewAuditLedgerHandler(audit service.AuditLedgerService, permission middleware.PermissionChecker) *AuditLedgerHandler {
	return &AuditLedgerHandler{audit: audit, permission: permission}
}

// NewAuditLedgerHandlerWithExtensions wires the audit export (P4-08),
// legal-hold administration (P4-07) and WORM anchoring (P4-03).
func NewAuditLedgerHandlerWithExtensions(audit service.AuditLedgerService,
	holds service.AuditHoldService, anchor service.AuditAnchorService, jobs service.SyncJobService,
	permission middleware.PermissionChecker) *AuditLedgerHandler {
	return &AuditLedgerHandler{audit: audit, holds: holds, anchor: anchor, jobs: jobs, permission: permission}
}

func (h *AuditLedgerHandler) RegisterRoutes(r Routes) {
	group := r.Auth.Group("/audit")
	if h.permission != nil {
		group.Use(middleware.RequirePermission(domain.PermAuditRead, h.permission))
	}
	group.GET("/events", h.List)
	group.GET("/operations/:id", h.OperationTimeline)
	group.GET("/verify", h.Verify)
	if h.jobs != nil {
		// P4-08: async audit export with signed-style manifest, TTL download.
		export := r.Auth.Group("/audit/export")
		if h.permission != nil {
			export.Use(middleware.RequirePermission(domain.PermAuditExport, h.permission))
		}
		export.POST("", h.CreateAuditExport)
	}
	if h.anchor != nil {
		// P4-03: tamper-evidence anchoring.
		anchor := r.Auth.Group("/admin/audit/anchor")
		if h.permission != nil {
			anchor.Use(middleware.RequirePermission(domain.PermAuditRead, h.permission))
		}
		anchor.POST("", h.AnchorChains)
		anchor.GET("/manifest/:source/:partition", h.AnchorManifest)
	}
	if h.holds != nil {
		// P4-07: legal-hold administration.
		holds := r.Auth.Group("/admin/audit/holds")
		if h.permission != nil {
			holds.Use(middleware.RequirePermission(domain.PermAuditRead, h.permission))
		}
		holds.GET("", h.ListHolds)
		holds.POST("", h.PutHold)
		holds.DELETE("/:scope_type/:scope_id", h.RemoveHold)
	}
}

func (h *AuditLedgerHandler) OperationTimeline(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items, next, err := h.audit.List(c.Request.Context(), domain.AuditFactFilter{OperationID: id, Page: httpx.PageFromQuery(c)})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	result := make([]auditFactResponse, len(items))
	for i, item := range items {
		result[i] = auditFactResponseOf(item)
	}
	httpx.OK(c, gin.H{"operation_id": id.String(), "events": result, "next_cursor": next})
}

func (h *AuditLedgerHandler) Verify(c *gin.Context) {
	source, partition := c.Query("source"), c.Query("partition")
	if source == "" || partition == "" {
		httpx.WriteError(c, apperr.BadRequest("audit.invalid_chain", "source and partition are required"))
		return
	}
	if err := h.audit.VerifyChain(c.Request.Context(), source, partition); err != nil {
		httpx.WriteError(c, apperr.Conflict("audit.chain_invalid", "audit chain verification failed").Wrap(err))
		return
	}
	httpx.OK(c, gin.H{"source": source, "partition": partition, "verified": true})
}

func (h *AuditLedgerHandler) List(c *gin.Context) {
	filter, err := auditFilter(c)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items, next, err := h.audit.List(c.Request.Context(), filter)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	result := make([]auditFactResponse, len(items))
	for i, item := range items {
		result[i] = auditFactResponseOf(item)
	}
	httpx.OK(c, gin.H{"events": result, "next_cursor": next})
}

func auditFilter(c *gin.Context) (domain.AuditFactFilter, error) {
	from, err := parseAuditTime(c.Query("from"), "from")
	if err != nil {
		return domain.AuditFactFilter{}, err
	}
	to, err := parseAuditTime(c.Query("to"), "to")
	if err != nil {
		return domain.AuditFactFilter{}, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return domain.AuditFactFilter{}, apperr.BadRequest("audit.invalid_time_range", "audit from must be before to")
	}
	filter := domain.AuditFactFilter{Source: c.Query("source"), ActorUID: c.Query("actor_uid"),
		RequestID: c.Query("request_id"), TenantID: c.Query("tenant_id"), Action: c.Query("action"),
		ResourceType: c.Query("resource_type"), From: from, To: to, Page: httpx.PageFromQuery(c)}
	if err := setAuditID(&filter.AccountID, c.Query("account_id")); err != nil {
		return domain.AuditFactFilter{}, err
	}
	if err := setAuditID(&filter.OperationID, c.Query("operation_id")); err != nil {
		return domain.AuditFactFilter{}, err
	}
	return filter, nil
}

func setAuditID(target *domain.ID, raw string) error {
	if raw == "" {
		return nil
	}
	id, err := domainID(raw)
	if err != nil {
		return err
	}
	*target = id
	return nil
}

func parseAuditTime(value, field string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, apperr.BadRequest("audit.invalid_time", "invalid "+field)
	}
	return &parsed, nil
}

type auditFactResponse struct {
	ID               string         `json:"id"`
	ChainSequence    int64          `json:"chain_sequence"`
	Source           string         `json:"source"`
	SourceRegion     string         `json:"source_region"`
	PartitionKey     string         `json:"partition_key"`
	EventID          string         `json:"event_id,omitempty"`
	OperationID      string         `json:"operation_id,omitempty"`
	CorrelationID    string         `json:"correlation_id,omitempty"`
	CausationID      string         `json:"causation_id,omitempty"`
	TraceID          string         `json:"trace_id,omitempty"`
	RequestID        string         `json:"request_id,omitempty"`
	ActorUID         string         `json:"actor_uid,omitempty"`
	AccountID        string         `json:"account_id,omitempty"`
	TenantID         string         `json:"tenant_id,omitempty"`
	Action           string         `json:"action"`
	ResourceType     string         `json:"resource_type,omitempty"`
	ResourceID       string         `json:"resource_id,omitempty"`
	OccurredAt       time.Time      `json:"occurred_at"`
	Payload          map[string]any `json:"payload"`
	PayloadHash      string         `json:"payload_hash"`
	PreviousHash     string         `json:"previous_hash"`
	RecordHash       string         `json:"record_hash"`
	SignatureVersion int            `json:"signature_version"`
	Signature        string         `json:"signature,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	HashVersion      int            `json:"hash_version"`
}

func auditFactResponseOf(fact *domain.AuditFact) auditFactResponse {
	result := auditFactResponse{ID: fact.ID.String(), ChainSequence: fact.ChainSequence, Source: fact.Source,
		SourceRegion: fact.SourceRegion, PartitionKey: fact.PartitionKey, TraceID: fact.TraceID, RequestID: fact.RequestID, ActorUID: fact.ActorUID,
		TenantID: fact.TenantID, Action: fact.Action, ResourceType: fact.ResourceType, ResourceID: fact.ResourceID,
		OccurredAt: fact.OccurredAt, Payload: fact.Payload, PayloadHash: fact.PayloadHash, PreviousHash: fact.PreviousHash,
		RecordHash: fact.RecordHash, SignatureVersion: fact.SignatureVersion, Signature: fact.Signature, CreatedAt: fact.CreatedAt,
		HashVersion: fact.HashVersion}
	if fact.EventID != (domain.ID{}) {
		result.EventID = fact.EventID.String()
	}
	if fact.OperationID != (domain.ID{}) {
		result.OperationID = fact.OperationID.String()
	}
	if fact.CorrelationID != (domain.ID{}) {
		result.CorrelationID = fact.CorrelationID.String()
	}
	if fact.CausationID != (domain.ID{}) {
		result.CausationID = fact.CausationID.String()
	}
	if fact.AccountID != (domain.ID{}) {
		result.AccountID = fact.AccountID.String()
	}
	return result
}

func domainID(value string) (domain.ID, error) {
	id, err := idgen.Parse(value)
	if err != nil {
		return domain.ID{}, apperr.BadRequest("audit.invalid_id", "audit identifier is invalid")
	}
	return id, nil
}

type auditExportRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

// CreateAuditExport starts an asynchronous audit-export job (P4-08). The job id
// is returned; progress and the TTL-bound download are served by the standard
// sync job endpoints.
func (h *AuditLedgerHandler) CreateAuditExport(c *gin.Context) {
	var body auditExportRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		body = auditExportRequest{}
	}
	key := body.IdempotencyKey
	if key == "" {
		key = c.GetHeader("Idempotency-Key")
	}
	job, err := h.jobs.Create(c.Request.Context(), service.SyncJobInput{
		JobType:        domain.SyncJobAuditExport,
		IdempotencyKey: key,
	})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, gin.H{"job_id": job.ID.String(), "status": job.Status, "download_url": "/v1/sync/jobs/" + job.ID.String() + "/export/download"})
}

type holdRequest struct {
	ScopeType string `json:"scope_type" binding:"required"` // tenant | account | actor
	ScopeID   string `json:"scope_id" binding:"required"`
	Reason    string `json:"reason"`
	ExpiresAt string `json:"expires_at"` // RFC3339; empty = indefinite
}

func (h *AuditLedgerHandler) ListHolds(c *gin.Context) {
	tenantID := service.TenantFrom(c.Request.Context())
	holds, err := h.holds.List(c.Request.Context(), tenantID)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	out := make([]gin.H, 0, len(holds))
	for _, hold := range holds {
		out = append(out, gin.H{"scope_type": hold.ScopeType, "scope_id": hold.ScopeID,
			"reason": hold.Reason, "created_by": hold.CreatedBy, "created_at": hold.CreatedAt, "expires_at": hold.ExpiresAt})
	}
	httpx.OK(c, gin.H{"holds": out})
}

func (h *AuditLedgerHandler) PutHold(c *gin.Context) {
	var body holdRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("audit_hold.invalid_request", "scope_type and scope_id are required"))
		return
	}
	var expiresAt *time.Time
	if strings.TrimSpace(body.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, body.ExpiresAt)
		if err != nil {
			httpx.WriteError(c, apperr.BadRequest("audit_hold.invalid_expiry", "expires_at must be RFC3339"))
			return
		}
		expiresAt = &parsed
	}
	tenantID := service.TenantFrom(c.Request.Context())
	hold, err := h.holds.Put(c.Request.Context(), tenantID, domain.AuditHoldScope(body.ScopeType), body.ScopeID, body.Reason, expiresAt)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, gin.H{"scope_type": hold.ScopeType, "scope_id": hold.ScopeID, "created_at": hold.CreatedAt})
}

func (h *AuditLedgerHandler) RemoveHold(c *gin.Context) {
	tenantID := service.TenantFrom(c.Request.Context())
	if err := h.holds.Remove(c.Request.Context(), tenantID,
		domain.AuditHoldScope(c.Param("scope_type")), c.Param("scope_id")); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"removed": true})
}

// AnchorChains runs one WORM anchoring pass over all audit chains (P4-03).
func (h *AuditLedgerHandler) AnchorChains(c *gin.Context) {
	result, err := h.anchor.AnchorOnce(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, result)
}

// AnchorManifest returns the newest stored anchor for one chain.
func (h *AuditLedgerHandler) AnchorManifest(c *gin.Context) {
	manifest, err := h.anchor.AnchorManifest(c.Request.Context(), c.Param("source"), c.Param("partition"))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, manifest)
}
