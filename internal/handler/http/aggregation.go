package http

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

type AggregationHandler struct {
	aggregation service.AggregationService
	permission  middleware.PermissionChecker
}

func NewAggregationHandler(aggregation service.AggregationService, permission middleware.PermissionChecker) *AggregationHandler {
	return &AggregationHandler{aggregation: aggregation, permission: permission}
}

func (h *AggregationHandler) RegisterRoutes(r Routes) {
	r.Auth.GET("/me/overview", h.GetMeOverview)
	r.Auth.GET("/me/resources", h.GetMeOverview)
	r.Auth.GET("/me/data", h.GetMeOverview)
	r.Auth.GET("/me/activity", h.GetMeActivity)
	r.Auth.GET("/account-datasets", h.ListDatasets)
	admin := r.Auth.Group("/admin/accounts")
	if h.permission != nil {
		admin.Use(middleware.RequirePermission(service.AccountReadPermission, h.permission))
	}
	admin.GET("/:id/overview", h.GetAccountOverview)
	admin.GET("/:id/data", h.GetAccountOverview)
}

func (h *AggregationHandler) GetMeOverview(c *gin.Context) {
	view, err := h.aggregation.GetMeOverview(c.Request.Context(), aggregationInput(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, aggregateOverviewResponse(view))
}

func (h *AggregationHandler) GetMeActivity(c *gin.Context) {
	view, err := h.aggregation.ListMeActivity(c.Request.Context(), aggregationInput(c), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"events": view.Events, "next_cursor": view.NextCursor, "partial": view.Partial,
		"generated_at": view.GeneratedAt, "source_errors": view.SourceErrors, "stale_datasets": view.StaleDatasets})
}

func (h *AggregationHandler) ListDatasets(c *gin.Context) {
	items := domain.AccountDatasetRegistry()
	result := make([]gin.H, 0, len(items))
	for _, item := range items {
		result = append(result, gin.H{"name": item.Name, "source": item.Source, "pii": item.PII})
	}
	httpx.OK(c, gin.H{"datasets": result})
}

func (h *AggregationHandler) GetAccountOverview(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	view, err := h.aggregation.GetAccountOverview(c.Request.Context(), id, aggregationInput(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, aggregateOverviewResponse(view))
}

func aggregationInput(c *gin.Context) service.AggregationInput {
	values := c.QueryArray("dataset")
	if len(values) == 0 && c.Query("datasets") != "" {
		values = strings.Split(c.Query("datasets"), ",")
	}
	datasets := make([]domain.AccountDataset, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			datasets = append(datasets, domain.AccountDataset(value))
		}
	}
	maxAge := time.Duration(0)
	if value := c.Query("max_age"); value != "" {
		maxAge, _ = time.ParseDuration(value)
	}
	return service.AggregationInput{
		Consistency: domain.ConsistencyMode(c.DefaultQuery("consistency", string(domain.ConsistencyEventual))),
		Datasets:    datasets,
		MaxAge:      maxAge,
	}
}

type aggregateSnapshotResponse struct {
	ID           string         `json:"id"`
	Source       string         `json:"source"`
	SourceRegion string         `json:"source_region"`
	Dataset      string         `json:"dataset"`
	Status       string         `json:"status"`
	Version      int64          `json:"version"`
	GeneratedAt  time.Time      `json:"generated_at"`
	LastSyncedAt *time.Time     `json:"last_synced_at,omitempty"`
	ExpiresAt    *time.Time     `json:"expires_at,omitempty"`
	ErrorCode    string         `json:"error_code,omitempty"`
	Data         map[string]any `json:"data"`
}

func aggregateOverviewResponse(view *domain.AggregateOverview) gin.H {
	snapshots := make([]aggregateSnapshotResponse, 0, len(view.Snapshots))
	for _, item := range view.Snapshots {
		if item == nil {
			continue
		}
		snapshots = append(snapshots, aggregateSnapshotResponse{
			ID: item.ID.String(), Source: item.Source, SourceRegion: item.SourceRegion,
			Dataset: string(item.Dataset), Status: string(item.Status), Version: item.Version,
			GeneratedAt: item.GeneratedAt, LastSyncedAt: item.LastSyncedAt, ExpiresAt: item.ExpiresAt,
			ErrorCode: item.ErrorCode, Data: item.Data,
		})
	}
	return gin.H{
		"account":        accountResponseOf(view.Account),
		"profile":        profileResponse(view.Profile),
		"snapshots":      snapshots,
		"consistency":    string(view.Consistency),
		"partial":        view.Partial,
		"source_errors":  view.SourceErrors,
		"stale_datasets": view.StaleDatasets,
		"generated_at":   view.GeneratedAt,
	}
}
