package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

// AdminRepository provides role-based access queries and admin operations.
// AdminRepository provides role-based access queries and admin operations.
type AdminRepository interface {
	// HasPermission checks whether uid holds at least one role that includes perm.
	HasPermission(ctx context.Context, uid, perm string) (bool, error)
	// ListAudit returns audit events across all users, newest-first, cursor-paged.
	ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error)
	// GrantRole assigns role_code to uid.
	GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error
	// RevokeRole removes role_code from uid.
	RevokeRole(ctx context.Context, uid, roleCode string) error
	// ListRoles returns all defined roles with their permissions.
	ListRoles(ctx context.Context) ([]*domain.Role, error)
	// ListUserRoles returns all roles assigned to uid.
	ListUserRoles(ctx context.Context, uid string) ([]*domain.UserRole, error)
}

type adminRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

func NewAdminRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) AdminRepository {
	return &adminRepo{db: db, audit: requireAuditLedger("NewAdminRepository", audit)}
}

type userRoleRow struct {
	UID       string    `gorm:"column:uid;primaryKey"`
	RoleCode  string    `gorm:"column:role_code;primaryKey"`
	GrantedBy string    `gorm:"column:granted_by"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (userRoleRow) TableName() string { return "user_roles" }

type roleRow struct {
	Code        string    `gorm:"column:code;primaryKey"`
	Name        string    `gorm:"column:name"`
	Permissions []string  `gorm:"column:permissions;type:text[]"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (roleRow) TableName() string { return "roles" }

func (r *adminRepo) HasPermission(ctx context.Context, uid, perm string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&userRoleRow{}).
		Joins("JOIN roles ON roles.code = user_roles.role_code").
		Where("user_roles.uid = ? AND ? = ANY(roles.permissions)", uid, perm).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("has permission: %w", err)
	}
	return count > 0, nil
}

const (
	defaultAuditAdminLimit = 50
	maxAuditAdminLimit     = 200
)

func (r *adminRepo) ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error) {
	q := r.db.WithContext(ctx).Model(&auditEventRow{}).Order("created_at DESC, id DESC")
	if action != "" {
		q = q.Where("action = ?", action)
	}
	if page.Cursor != "" {
		q = applyLegacyAuditCursor(q, page.Cursor)
	}
	limit := page.Limit
	if limit <= 0 {
		limit = defaultAuditAdminLimit
	}
	if limit > maxAuditAdminLimit {
		limit = maxAuditAdminLimit
	}
	var rows []auditEventRow
	if err := q.Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list admin audit: %w", err)
	}
	cursor := ""
	if len(rows) > limit {
		cursor = encodeAuditCursorAt(rows[limit-1].CreatedAt, rows[limit-1].ID.String())
		rows = rows[:limit]
	}
	out := make([]*domain.AuditEvent, len(rows))
	for i := range rows {
		out[i] = auditEventRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func applyLegacyAuditCursor(q *gorm.DB, value string) *gorm.DB {
	if cursor, err := decodeAuditCursor(value); err == nil {
		return q.Where("(created_at < ? OR (created_at = ? AND id < ?))", cursor.OccurredAt, cursor.OccurredAt, cursor.ID)
	}
	// Accept the pre-compound UUID cursor during the compatibility window.
	return q.Where("id < ?", value)
}

func (r *adminRepo) GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error {
	row := &userRoleRow{
		UID:       uid,
		RoleCode:  roleCode,
		GrantedBy: grantedBy,
		CreatedAt: time.Now().UTC(),
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		return fmt.Errorf("grant role: %w", err)
	}
	return nil
}

func (r *adminRepo) RevokeRole(ctx context.Context, uid, roleCode string) error {
	res := r.db.WithContext(ctx).
		Where("uid = ? AND role_code = ?", uid, roleCode).
		Delete(&userRoleRow{})
	if res.Error != nil {
		return fmt.Errorf("revoke role: %w", res.Error)
	}
	return nil
}

func (r *adminRepo) ListRoles(ctx context.Context) ([]*domain.Role, error) {
	var rows []roleRow
	if err := r.db.WithContext(ctx).Model(&roleRow{}).Order("code ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	out := make([]*domain.Role, len(rows))
	for i := range rows {
		out[i] = &domain.Role{
			Code:        rows[i].Code,
			Name:        rows[i].Name,
			Permissions: rows[i].Permissions,
			CreatedAt:   rows[i].CreatedAt,
		}
	}
	return out, nil
}

func (r *adminRepo) ListUserRoles(ctx context.Context, uid string) ([]*domain.UserRole, error) {
	var rows []userRoleRow
	if err := r.db.WithContext(ctx).
		Model(&userRoleRow{}).
		Where("uid = ?", uid).
		Order("created_at DESC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list user roles: %w", err)
	}
	out := make([]*domain.UserRole, len(rows))
	for i := range rows {
		out[i] = &domain.UserRole{
			UID:       rows[i].UID,
			RoleCode:  rows[i].RoleCode,
			GrantedBy: rows[i].GrantedBy,
			CreatedAt: rows[i].CreatedAt,
		}
	}
	return out, nil
}
