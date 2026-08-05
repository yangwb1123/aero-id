package repository

import (
	"context"
	"strings"
)

// LegacyBackfillRepository exposes the legacy compatibility tables (files,
// messages, comments and satellites) for the one-shot shadow identity backfill
// (P1-07 / ADR-011). It is read-only: the backfill never mutates legacy rows.
type LegacyBackfillRepository interface {
	// ListLegacyUIDs returns distinct uids referenced by legacy tables that do
	// NOT yet have an account identity for (tenantID, issuer). Bounded by limit
	// so a single run performs a bounded scan; callers loop until empty.
	ListLegacyUIDs(ctx context.Context, tenantID, issuer string, limit int) ([]string, error)

	// CountLegacyUIDs returns the total number of distinct legacy uids, for
	// progress reporting. Requires no identity filter (cheap approximation).
	CountLegacyUIDs(ctx context.Context) (int64, error)
}

// legacyUIDUnion is the distinct-uid union over every legacy table that
// references human accounts. Messages/comments/files are the primary surfaces;
// shares/likes/reports/blocks add secondary references (P1-07 shadow mapping).
const legacyUIDUnion = `
	SELECT owner_uid AS uid FROM files WHERE deleted_at IS NULL
	UNION SELECT from_uid FROM messages WHERE deleted_at IS NULL
	UNION SELECT to_uid FROM messages WHERE deleted_at IS NULL
	UNION SELECT owner_uid FROM comments WHERE deleted_at IS NULL
	UNION SELECT owner_uid FROM file_shares
	UNION SELECT grantee_uid FROM file_shares
	UNION SELECT uid FROM comment_likes
	UNION SELECT reporter_uid FROM comment_reports
	UNION SELECT blocker_uid FROM user_blocks
	UNION SELECT blocked_uid FROM user_blocks`

func (r *accountRepo) ListLegacyUIDs(ctx context.Context, tenantID, issuer string, limit int) ([]string, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	query := `
		SELECT u.uid FROM (` + legacyUIDUnion + `) u
		WHERE u.uid <> ''
		  AND NOT EXISTS (
			SELECT 1 FROM account_identities i
			WHERE i.tenant_id = ? AND i.issuer = ? AND i.subject = u.uid
		  )
		ORDER BY u.uid
		LIMIT ?`
	var uids []string
	if err := r.db.WithContext(ctx).Raw(query, tenantID, issuer, limit).Scan(&uids).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(uids))
	for _, uid := range uids {
		if strings.TrimSpace(uid) != "" {
			out = append(out, uid)
		}
	}
	return out, nil
}

func (r *accountRepo) CountLegacyUIDs(ctx context.Context) (int64, error) {
	var count int64
	query := `SELECT COUNT(*) FROM (` + legacyUIDUnion + `) u WHERE u.uid <> ''`
	if err := r.db.WithContext(ctx).Raw(query).Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
