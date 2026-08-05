package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

// AdminHandler exposes admin-only endpoints under /v1/admin. Every route
// requires the caller to have the appropriate permission, enforced via
// RequirePermission middleware so the check is uniform and auditable.
// HubDisconnector forces the SSE hub to drop all connections for a uid.
// Implemented by *hub.Hub.
type HubDisconnector interface {
	Disconnect(uid string)
}

type AdminHandler struct {
	admin       service.AdminService
	bans        service.BanService
	permChecker middleware.PermissionChecker
	hub         HubDisconnector // nil => no SSE disconnect on ban
}

// NewAdminHandler builds an AdminHandler. Optional dependencies:
//   - bans[0]: BanService (nil => ban endpoints are not wired)
//   - hub[0]: HubDisconnector for SSE disconnect on ban
func NewAdminHandler(admin service.AdminService, bans []service.BanService, hub []HubDisconnector, permissions ...middleware.PermissionChecker) *AdminHandler {
	h := &AdminHandler{
		admin:       admin,
		permChecker: admin, // AdminService satisfies PermissionChecker
	}
	if len(permissions) > 0 && permissions[0] != nil {
		h.permChecker = permissions[0]
	}
	if len(bans) > 0 {
		h.bans = bans[0]
	}
	if len(hub) > 0 {
		h.hub = hub[0]
	}
	return h
}

func (h *AdminHandler) RegisterRoutes(r Routes) {
	// Audit & roles group requires PermAuditRead.
	admin := r.Auth.Group("/admin")
	admin.Use(middleware.RequirePermission(domain.PermAuditRead, h.permChecker))
	{
		admin.GET("/audit-log", h.ListAudit)
		admin.GET("/roles", h.ListRoles)
		admin.GET("/roles/:uid", h.ListUserRoles)
	}

	// System config group requires system.config permission.
	syscfg := r.Auth.Group("/admin")
	syscfg.Use(middleware.RequirePermission(domain.PermSystemConfig, h.permChecker))
	{
		syscfg.POST("/roles/grant", h.GrantRole)
		syscfg.POST("/roles/revoke", h.RevokeRole)
	}

	// Ban endpoints require user.ban permission.
	if h.bans != nil {
		bans := r.Auth.Group("/admin/bans")
		bans.Use(middleware.RequirePermission(domain.PermUserBan, h.permChecker))
		{
			bans.POST("", h.BanUser)
			bans.GET("", h.ListBanned)
			bans.DELETE("/:uid", h.UnbanUser)
		}
	}
}

func (h *AdminHandler) ListAudit(c *gin.Context) {
	page := httpx.PageFromQuery(c)
	action := c.Query("action")
	events, next, err := h.admin.ListAudit(c.Request.Context(), action, page)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{
		"events":      events,
		"next_cursor": next,
	})
}

func (h *AdminHandler) GrantRole(c *gin.Context) {
	var req struct {
		UID      string `json:"uid" binding:"required"`
		RoleCode string `json:"role_code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("admin.invalid_request", "uid and role_code are required"))
		return
	}
	if err := h.admin.GrantRole(c.Request.Context(), req.UID, req.RoleCode); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"status": "granted"})
}

func (h *AdminHandler) RevokeRole(c *gin.Context) {
	var req struct {
		UID      string `json:"uid" binding:"required"`
		RoleCode string `json:"role_code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("admin.invalid_request", "uid and role_code are required"))
		return
	}
	if err := h.admin.RevokeRole(c.Request.Context(), req.UID, req.RoleCode); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"status": "revoked"})
}

func (h *AdminHandler) ListRoles(c *gin.Context) {
	roles, err := h.admin.ListRoles(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"roles": roles})
}

func (h *AdminHandler) ListUserRoles(c *gin.Context) {
	uid := c.Param("uid")
	if uid == "" {
		httpx.WriteError(c, apperr.BadRequest("admin.invalid_uid", "uid is required"))
		return
	}
	roles, err := h.admin.ListUserRoles(c.Request.Context(), uid)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"roles": roles})
}

// ---- Ban endpoints ----

type banUserRequest struct {
	UID      string `json:"uid" binding:"required"`
	Reason   string `json:"reason" binding:"required"`
	Duration string `json:"duration"` // Go duration string e.g. "24h", "7d"; empty = permanent
}

func (h *AdminHandler) BanUser(c *gin.Context) {
	var req banUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("ban.invalid_request", "uid and reason are required"))
		return
	}
	var d time.Duration
	if req.Duration != "" {
		var err error
		d, err = time.ParseDuration(req.Duration)
		if err != nil {
			httpx.WriteError(c, apperr.BadRequest("ban.invalid_duration", "invalid duration format"))
			return
		}
	}
	if err := h.bans.Ban(c.Request.Context(), req.UID, req.Reason, d); err != nil {
		httpx.WriteError(c, err)
		return
	}
	// Force-disconnect any active SSE connections for the banned user.
	if h.hub != nil {
		h.hub.Disconnect(req.UID)
	}
	httpx.OK(c, gin.H{"status": "banned"})
}

func (h *AdminHandler) UnbanUser(c *gin.Context) {
	uid := c.Param("uid")
	if uid == "" {
		httpx.WriteError(c, apperr.BadRequest("ban.invalid_uid", "uid is required"))
		return
	}
	if err := h.bans.Unban(c.Request.Context(), uid); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"status": "unbanned"})
}

func (h *AdminHandler) ListBanned(c *gin.Context) {
	bans, next, err := h.bans.ListBanned(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	type banItem struct {
		UID       string     `json:"uid"`
		ActorUID  string     `json:"actor_uid"`
		Reason    string     `json:"reason"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
		CreatedAt time.Time  `json:"created_at"`
	}
	items := make([]banItem, len(bans))
	for i, b := range bans {
		items[i] = banItem{
			UID:       b.UID,
			ActorUID:  b.ActorUID,
			Reason:    b.Reason,
			ExpiresAt: b.ExpiresAt,
			CreatedAt: b.CreatedAt,
		}
	}
	httpx.OK(c, gin.H{
		"bans":        items,
		"next_cursor": next,
	})
}
