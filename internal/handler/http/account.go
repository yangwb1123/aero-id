package http

import (
	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

type AccountHandler struct {
	accounts   service.AccountService
	fencing    service.RegionFencingService   // optional; nil disables region administration (P6-02)
	migration  service.LegacyMigrationService // optional; nil disables shadow comparison (P8-02)
	cutover    service.LegacyCutoverService   // optional; nil disables cutover administration (P8-04/05)
	permission middleware.PermissionChecker
}

func NewAccountHandler(accounts service.AccountService, permission middleware.PermissionChecker) *AccountHandler {
	return &AccountHandler{accounts: accounts, permission: permission}
}

// NewAccountHandlerWithExtensions wires the durable home-region directory
// administration (P6-02), shadow migration comparison (P8-02), and legacy
// cutover administration (P8-04/05).
func NewAccountHandlerWithExtensions(accounts service.AccountService,
	fencing service.RegionFencingService, migration service.LegacyMigrationService,
	cutover service.LegacyCutoverService, permission middleware.PermissionChecker) *AccountHandler {
	return &AccountHandler{accounts: accounts, fencing: fencing, migration: migration,
		cutover: cutover, permission: permission}
}

func (h *AccountHandler) RegisterRoutes(r Routes) {
	r.Auth.GET("/me", h.GetMe)
	r.Auth.GET("/me/profile", h.GetProfile)
	r.Auth.PATCH("/me/profile", h.UpdateProfile)
	r.Auth.GET("/me/sources", h.ListSources)
	r.Auth.GET("/me/memberships", h.ListMemberships)

	admin := r.Auth.Group("/admin/accounts")
	if h.permission != nil {
		admin.Use(middleware.RequirePermission(service.AccountReadPermission, h.permission))
	}
	admin.GET("", h.ListAccounts)
	admin.GET("/:id", h.GetAccount)

	// P1-07: one-shot shadow backfill of legacy uids -> account identities.
	// Write-level admin op, guarded by account.manage (not account.read).
	backfill := r.Auth.Group("/admin/accounts/backfill")
	if h.permission != nil {
		backfill.Use(middleware.RequirePermission(service.AccountManagePermission, h.permission))
	}
	backfill.POST("/legacy-identities", h.BackfillLegacyIdentities)

	if h.fencing != nil {
		// P6-02: durable home-region directory administration.
		regions := r.Auth.Group("/admin/regions")
		if h.permission != nil {
			regions.Use(middleware.RequirePermission(service.AccountManagePermission, h.permission))
		}
		regions.GET("/directory/:account_id", h.GetRegionDirectory)
		regions.POST("/promote", h.PromoteRegion)
	}

	if h.migration != nil {
		// P8-02: shadow migration comparison (read-only; no legacy writes).
		migration := r.Auth.Group("/admin/migration")
		if h.permission != nil {
			migration.Use(middleware.RequirePermission(service.AccountManagePermission, h.permission))
		}
		migration.POST("/legacy/compare", h.CompareLegacyMigration)
	}

	if h.cutover != nil {
		// P8-04/05: per-tenant cutover registry + cleanup readiness checklist.
		cutover := r.Auth.Group("/admin/migration/legacy")
		if h.permission != nil {
			cutover.Use(middleware.RequirePermission(service.AccountManagePermission, h.permission))
		}
		cutover.GET("/cutover/:tenant_id", h.GetCutover)
		cutover.POST("/cutover", h.SetCutover)
		cutover.GET("/cleanup-readiness/:tenant_id", h.CleanupReadiness)
	}
}

type backfillRequest struct {
	Limit int `json:"limit"` // max uids per run; 0 => service default
}

type legacyCompareRequest struct {
	OwnerUID string `json:"owner_uid" binding:"required"`
	Source   string `json:"source" binding:"required"`
	Limit    int    `json:"limit"`
}

type cutoverRequest struct {
	TenantID string `json:"tenant_id" binding:"required"`
	Status   string `json:"status" binding:"required"`
	Note     string `json:"note"`
}

func (h *AccountHandler) GetCutover(c *gin.Context) {
	status, err := h.cutover.Get(c.Request.Context(), c.Param("tenant_id"))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"tenant_id": c.Param("tenant_id"), "status": status})
}

func (h *AccountHandler) SetCutover(c *gin.Context) {
	var body cutoverRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("cutover.invalid_request", "tenant_id and status are required"))
		return
	}
	entry, err := h.cutover.Set(c.Request.Context(), body.TenantID, repository.LegacyCutoverStatus(body.Status), body.Note)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"tenant_id": entry.TenantID, "status": entry.Status, "decided_by": entry.DecidedBy, "note": entry.Note})
}

func (h *AccountHandler) CleanupReadiness(c *gin.Context) {
	items, ready, err := h.cutover.CleanupReadiness(c.Request.Context(), c.Param("tenant_id"))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"tenant_id": c.Param("tenant_id"), "ready": ready, "checks": items})
}

func (h *AccountHandler) CompareLegacyMigration(c *gin.Context) {
	var body legacyCompareRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("migration.invalid_request", "owner_uid and source are required"))
		return
	}
	result, err := h.migration.Compare(c.Request.Context(), service.LegacyCompareInput{
		OwnerUID: body.OwnerUID, Source: connector.Source(body.Source), Limit: body.Limit,
	})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, result)
}

type promoteRegionRequest struct {
	AccountID     string `json:"account_id" binding:"required"`
	TargetRegion  string `json:"target_region" binding:"required"`
	ExpectedEpoch int64  `json:"expected_epoch"`
}

func (h *AccountHandler) GetRegionDirectory(c *gin.Context) {
	accountID, err := parseIDString(c.Param("account_id"), "account_id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	tenantID := service.TenantFrom(c.Request.Context())
	entry, err := h.fencing.Directory(c.Request.Context(), tenantID, accountID)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"account_id": entry.AccountID.String(), "home_region": entry.HomeRegion,
		"resolver_version": entry.ResolverVersion, "fenced_epoch": entry.FencedEpoch,
		"promoted_at": entry.PromotedAt})
}

func (h *AccountHandler) PromoteRegion(c *gin.Context) {
	var body promoteRegionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("region.invalid_request", "account_id and target_region are required"))
		return
	}
	accountID, err := parseIDString(body.AccountID, "account_id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	tenantID := service.TenantFrom(c.Request.Context())
	newEpoch, err := h.fencing.Promote(c.Request.Context(), tenantID, accountID, body.TargetRegion, body.ExpectedEpoch)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"account_id": accountID.String(), "target_region": body.TargetRegion, "fenced_epoch": newEpoch})
}

func (h *AccountHandler) BackfillLegacyIdentities(c *gin.Context) {
	var body backfillRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		body = backfillRequest{}
	}
	result, err := h.accounts.BackfillLegacyIdentities(c.Request.Context(), body.Limit)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, result)
}

func (h *AccountHandler) GetMe(c *gin.Context) {
	view, err := h.accounts.GetMe(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, accountViewResponse(view))
}

func (h *AccountHandler) GetProfile(c *gin.Context) {
	profile, err := h.accounts.GetProfile(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, profileResponse(profile))
}

type updateProfileRequest struct {
	DisplayName *string        `json:"display_name"`
	AvatarURL   *string        `json:"avatar_url"`
	Locale      *string        `json:"locale"`
	Timezone    *string        `json:"timezone"`
	Preferences map[string]any `json:"preferences"`
}

func (h *AccountHandler) UpdateProfile(c *gin.Context) {
	var body updateProfileRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("account.invalid_profile", "invalid profile request"))
		return
	}
	profile, err := h.accounts.UpdateProfile(c.Request.Context(), service.UpdateAccountProfileInput{
		DisplayName: body.DisplayName,
		AvatarURL:   body.AvatarURL,
		Locale:      body.Locale,
		Timezone:    body.Timezone,
		Preferences: body.Preferences,
	})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, profileResponse(profile))
}

func (h *AccountHandler) ListSources(c *gin.Context) {
	result, err := h.accounts.ListSources(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items := make([]sourceResponse, len(result.Sources))
	for i, item := range result.Sources {
		items[i] = sourceResponseOf(item)
	}
	httpx.OK(c, gin.H{"sources": items, "next_cursor": result.NextCursor})
}

func (h *AccountHandler) ListMemberships(c *gin.Context) {
	result, err := h.accounts.ListMemberships(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items := make([]membershipResponse, len(result.Memberships))
	for i, item := range result.Memberships {
		items[i] = membershipResponseOf(item)
	}
	httpx.OK(c, gin.H{"memberships": items, "next_cursor": result.NextCursor})
}

func (h *AccountHandler) ListAccounts(c *gin.Context) {
	result, err := h.accounts.ListAccounts(c.Request.Context(), service.ListAccountsInput{
		Query: c.Query("q"), Status: c.Query("status"), HomeRegion: c.Query("home_region"), Page: httpx.PageFromQuery(c),
	})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items := make([]accountResponse, len(result.Accounts))
	for i, item := range result.Accounts {
		items[i] = accountResponseOf(item)
	}
	httpx.OK(c, gin.H{"accounts": items, "next_cursor": result.NextCursor})
}

func (h *AccountHandler) GetAccount(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	view, err := h.accounts.GetAccount(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, accountViewResponse(view))
}

type accountResponse struct {
	ID           string `json:"account_id"`
	CanonicalUID string `json:"canonical_uid"`
	HomeRegion   string `json:"home_region"`
	Status       string `json:"status"`
}

type profileResponseBody struct {
	AccountID   string         `json:"account_id"`
	DisplayName string         `json:"display_name"`
	AvatarURL   string         `json:"avatar_url"`
	Locale      string         `json:"locale"`
	Timezone    string         `json:"timezone"`
	Preferences map[string]any `json:"preferences"`
}

type sourceResponse struct {
	ID              string         `json:"id"`
	Source          string         `json:"source"`
	SourceRegion    string         `json:"source_region"`
	SourceAccountID string         `json:"source_account_id"`
	ScopeType       string         `json:"scope_type,omitempty"`
	ScopeID         string         `json:"scope_id,omitempty"`
	Status          string         `json:"status"`
	Version         int64          `json:"version"`
	GeneratedAt     any            `json:"generated_at,omitempty"`
	LastSyncedAt    any            `json:"last_synced_at,omitempty"`
	ErrorCode       string         `json:"error_code,omitempty"`
	Data            map[string]any `json:"data,omitempty"`
}

type membershipResponse struct {
	ID             string         `json:"id"`
	Source         string         `json:"source"`
	SourceRegion   string         `json:"source_region"`
	ScopeType      string         `json:"scope_type"`
	ScopeID        string         `json:"scope_id"`
	SourceMemberID string         `json:"source_member_id"`
	Role           string         `json:"role"`
	Status         string         `json:"status"`
	Version        int64          `json:"version"`
	LastSyncedAt   any            `json:"last_synced_at,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
}

func accountResponseOf(account *domain.Account) accountResponse {
	return accountResponse{ID: account.ID.String(), CanonicalUID: account.CanonicalUID, HomeRegion: account.HomeRegion, Status: string(account.Status)}
}

func profileResponse(profile *domain.AccountProfile) profileResponseBody {
	return profileResponseBody{AccountID: profile.AccountID.String(), DisplayName: profile.DisplayName,
		AvatarURL: profile.AvatarURL, Locale: profile.Locale, Timezone: profile.Timezone, Preferences: profile.Preferences}
}

func accountViewResponse(view *service.AccountView) gin.H {
	sources := make([]sourceResponse, len(view.Sources))
	for i, item := range view.Sources {
		sources[i] = sourceResponseOf(item)
	}
	memberships := make([]membershipResponse, len(view.Memberships))
	for i, item := range view.Memberships {
		memberships[i] = membershipResponseOf(item)
	}
	return gin.H{"account": accountResponseOf(view.Account), "profile": profileResponse(view.Profile), "sources": sources, "memberships": memberships}
}

func sourceResponseOf(source *domain.AccountSource) sourceResponse {
	return sourceResponse{ID: source.ID.String(), Source: source.Source, SourceRegion: source.SourceRegion,
		SourceAccountID: source.SourceAccountID, ScopeType: source.ScopeType, ScopeID: source.ScopeID,
		Status: source.Status, Version: source.Version, GeneratedAt: source.GeneratedAt,
		LastSyncedAt: source.LastSyncedAt, ErrorCode: source.ErrorCode, Data: source.Data}
}

func membershipResponseOf(membership *domain.AccountMembership) membershipResponse {
	return membershipResponse{ID: membership.ID.String(), Source: membership.Source, SourceRegion: membership.SourceRegion,
		ScopeType: membership.ScopeType, ScopeID: membership.ScopeID, SourceMemberID: membership.SourceMemberID,
		Role: membership.Role, Status: membership.Status, Version: membership.Version, LastSyncedAt: membership.LastSyncedAt,
		Data: membership.Data}
}
