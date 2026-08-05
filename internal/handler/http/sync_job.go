package http

import (
	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

type SyncJobHandler struct {
	jobs       service.SyncJobService
	permission middleware.PermissionChecker
}

func NewSyncJobHandler(jobs service.SyncJobService, permission middleware.PermissionChecker) *SyncJobHandler {
	return &SyncJobHandler{jobs: jobs, permission: permission}
}

func (h *SyncJobHandler) RegisterRoutes(r Routes) {
	r.Auth.POST("/me/sync", h.SyncMe)
	r.Auth.POST("/me/export", h.ExportMe)
	r.Auth.POST("/me/erase", h.EraseMe)
	r.Auth.GET("/sync/jobs", h.List)
	r.Auth.GET("/sync/jobs/:id", h.Get)
	r.Auth.POST("/sync/jobs/:id/reconcile", h.Reconcile)
	r.Auth.GET("/sync/jobs/:id/export/download", h.DownloadExport)
	admin := r.Auth.Group("/admin/accounts")
	adminSync := admin.Group("", middleware.RequirePermissionIfConfigured(domain.PermSourceSync, h.permission))
	adminSync.POST("/:id/sync", h.SyncAccount)
	adminExport := admin.Group("", middleware.RequirePermissionIfConfigured(domain.PermAccountExport, h.permission))
	adminExport.POST("/:id/export", h.ExportAccount)
	adminErase := admin.Group("", middleware.RequirePermissionIfConfigured(domain.PermAccountErase, h.permission))
	adminErase.POST("/:id/erase", h.EraseAccount)
}

type syncJobRequest struct {
	Datasets       []domain.AccountDataset `json:"datasets"`
	IdempotencyKey string                  `json:"idempotency_key"`
}

func (h *SyncJobHandler) SyncMe(c *gin.Context) {
	h.create(c, domain.SyncJobSync, domain.ID{})
}

func (h *SyncJobHandler) ExportMe(c *gin.Context) {
	h.create(c, domain.SyncJobExport, domain.ID{})
}

func (h *SyncJobHandler) EraseMe(c *gin.Context) {
	h.create(c, domain.SyncJobErase, domain.ID{})
}

func (h *SyncJobHandler) SyncAccount(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	h.create(c, domain.SyncJobSync, id)
}

func (h *SyncJobHandler) ExportAccount(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	h.create(c, domain.SyncJobExport, id)
}

func (h *SyncJobHandler) EraseAccount(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	h.create(c, domain.SyncJobErase, id)
}

func (h *SyncJobHandler) create(c *gin.Context, jobType domain.SyncJobType, accountID domain.ID) {
	var body syncJobRequest
	if err := c.ShouldBindJSON(&body); err != nil && c.Request.ContentLength != 0 {
		httpx.WriteError(c, err)
		return
	}
	key := body.IdempotencyKey
	if key == "" {
		key = c.GetHeader("Idempotency-Key")
	}
	job, err := h.jobs.Create(c.Request.Context(), service.SyncJobInput{AccountID: accountID, JobType: jobType,
		Datasets: body.Datasets, IdempotencyKey: key})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, syncJobResponseOf(job))
}

func (h *SyncJobHandler) Get(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	job, err := h.jobs.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, syncJobResponseOf(job))
}

func (h *SyncJobHandler) List(c *gin.Context) {
	items, next, err := h.jobs.List(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	result := make([]syncJobResponse, len(items))
	for i, item := range items {
		result[i] = syncJobResponseOf(item)
	}
	httpx.OK(c, gin.H{"jobs": result, "next_cursor": next})
}

func (h *SyncJobHandler) Reconcile(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	job, err := h.jobs.Reconcile(c.Request.Context(), id, c.GetHeader("X-Worker-ID"))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, syncJobResponseOf(job))
}

func (h *SyncJobHandler) DownloadExport(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	data, err := h.jobs.DownloadExport(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", `attachment; filename="aero-id-export.json"`)
	c.Data(200, "application/json", data)
}

type syncJobResponse struct {
	ID             string         `json:"job_id"`
	AccountID      string         `json:"account_id"`
	ActorUID       string         `json:"actor_uid"`
	TenantID       string         `json:"tenant_id,omitempty"`
	RequestID      string         `json:"request_id,omitempty"`
	JobType        string         `json:"job_type"`
	Status         string         `json:"status"`
	IdempotencyKey string         `json:"idempotency_key"`
	Request        map[string]any `json:"request"`
	Result         map[string]any `json:"result"`
	ErrorCode      string         `json:"error_code,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	Attempt        int            `json:"attempt"`
	CreatedAt      any            `json:"created_at"`
	UpdatedAt      any            `json:"updated_at"`
	CompletedAt    any            `json:"completed_at,omitempty"`
}

func syncJobResponseOf(job *domain.SyncJob) syncJobResponse {
	return syncJobResponse{ID: job.ID.String(), AccountID: job.AccountID.String(), ActorUID: job.ActorUID, TenantID: job.TenantID, RequestID: job.RequestID,
		JobType: string(job.JobType), Status: string(job.Status), IdempotencyKey: job.IdempotencyKey, Request: job.Request,
		Result: publicSyncResult(job.Result), ErrorCode: job.ErrorCode, ErrorMessage: job.ErrorMessage, Attempt: job.Attempt,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt, CompletedAt: job.CompletedAt}
}

func publicSyncResult(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		if len(key) > 0 && key[0] == '_' {
			continue
		}
		result[key] = item
	}
	return result
}
