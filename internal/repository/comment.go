package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
)

type CommentRepository interface {
	Create(ctx context.Context, c *domain.Comment) error
	// CreateWithReplyBump inserts a comment and, when it is a reply (ParentID set),
	// increments the parent's reply_count in the same transaction. When evt is
	// non-nil it is also appended to the outbox within that same transaction.
	CreateWithReplyBump(ctx context.Context, c *domain.Comment, evt *outbox.Event) error
	// AppendEvent appends an outbox event in its own transaction. This is used for
	// side-effect events (e.g. comment likes) that do not need to be atomic with
	// a repository write. The event is best-effort: a failure to append does not
	// fail the caller.
	AppendEvent(ctx context.Context, evt *outbox.Event) error
	Get(ctx context.Context, id domain.ID) (*domain.Comment, error)
	ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	IncReplyCount(ctx context.Context, parentID domain.ID) error
	SoftDelete(ctx context.Context, id domain.ID) error
	// UpdateContent rewrites a comment's content and stamps edited_at (and updated_at)
	// to at. Returns ErrNotFound if no live row matches.
	UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error
	// Like records a like by uid and returns the comment's updated like_count. A repeated
	// like by the same uid is a no-op (idempotent). Unlike is the inverse.
	Like(ctx context.Context, commentID domain.ID, uid string) (likeCount int, err error)
	Unlike(ctx context.Context, commentID domain.ID, uid string) (likeCount int, err error)
	// CreateReport records a report. A repeat report by the same reporter for the same
	// comment violates the (comment_id, reporter_uid) unique constraint and is surfaced
	// as ErrDuplicate.
	CreateReport(ctx context.Context, r *domain.CommentReport) error
	// SetStatus updates a comment's moderation status. Returns ErrNotFound if no row matches.
	SetStatus(ctx context.Context, commentID domain.ID, status domain.CommentStatus) error
	// ListReports returns reports against a single comment, newest first, cursor-paged.
	// When status is non-nil only reports in that triage state are returned.
	ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) ([]*domain.CommentReport, string, error)
	// GetReport loads a single report by id. Returns ErrNotFound if absent.
	GetReport(ctx context.Context, reportID domain.ID) (*domain.CommentReport, error)
	// ResolveReport transitions a report to a terminal status, stamping resolved_at/by.
	// Returns ErrNotFound if no row matches.
	ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time) error
	// SearchComments returns comments owned by ownerUID whose content matches the
	// full-text query, ranked by relevance. Requires migration 000016.
	SearchComments(ctx context.Context, ownerUID, query string, limit int) ([]*domain.Comment, error)
	// SearchCommentsWithSnippet is like SearchComments but also returns a
	// highlighted content snippet (via PostgreSQL ts_headline containing
	// <mark>...</mark> tags) for each hit.
	SearchCommentsWithSnippet(ctx context.Context, ownerUID, query string, limit int) ([]CommentSearchResult, error)
	// SearchCommentsLike is a LIKE-based fallback for FTS-unsupported queries (e.g. CJK).
	SearchCommentsLike(ctx context.Context, ownerUID, query string, limit int) ([]*domain.Comment, error)
}

type commentRepo struct {
	db     *gorm.DB
	outbox outbox.Repository
	audit  AuditLedgerTransactionalRepository
}

// NewCommentRepository builds a comment repository. ob is the outbox repository
// used to append domain events transactionally with comment writes; it may be nil
// (events are then simply not emitted), keeping existing wiring/tests working.
func NewCommentRepository(db *gorm.DB, ob outbox.Repository, audit AuditLedgerTransactionalRepository) CommentRepository {
	return &commentRepo{db: db, outbox: ob, audit: requireAuditLedger("NewCommentRepository", audit)}
}

func (r *commentRepo) Create(ctx context.Context, c *domain.Comment) error {
	if err := r.db.WithContext(ctx).Create(commentDomainToRow(c)).Error; err != nil {
		return fmt.Errorf("create comment: %w", err)
	}
	return nil
}

// CreateWithReplyBump inserts a comment and, when it is a reply (ParentID set),
// increments the parent's reply_count in the same transaction so the comment and
// the denormalized counter commit together (or not at all). When evt is non-nil
// and an outbox repository is configured, evt is appended in that same tx too.
func (r *commentRepo) CreateWithReplyBump(ctx context.Context, c *domain.Comment, evt *outbox.Event) error {
	row := commentDomainToRow(c)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		if c.ParentID != nil {
			if err := tx.Model(&commentRow{}).
				Where("id = ?", *c.ParentID).
				UpdateColumn("reply_count", gorm.Expr("reply_count + 1")).Error; err != nil {
				return err
			}
		}
		if evt != nil && r.outbox != nil {
			if err := r.outbox.Append(ctx, tx, evt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("create comment with reply bump: %w", err)
	}
	return nil
}

// AppendEvent appends an outbox event in its own transaction. When no outbox
// repository is configured this is a no-op (best-effort).
func (r *commentRepo) AppendEvent(ctx context.Context, evt *outbox.Event) error {
	if r.outbox == nil || evt == nil {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.outbox.Append(ctx, tx, evt)
	})
}

func (r *commentRepo) Get(ctx context.Context, id domain.ID) (*domain.Comment, error) {
	var row commentRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get comment: %w", err)
	}
	return commentRowToDomain(&row), nil
}

func (r *commentRepo) ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Where("target_type = ? AND target_id = ? AND parent_id IS NULL AND status = ?", string(t), target, int16(domain.CommentStatusNormal)).
		Order("created_at DESC, id DESC").
		Limit(page.Limit + 1)
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []commentRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list by target: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.Comment, len(rows))
	for i := range rows {
		out[i] = commentRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *commentRepo) ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Where("root_id = ? AND id <> root_id AND status = ?", rootID, int16(domain.CommentStatusNormal)).
		Order("created_at ASC, id ASC").
		Limit(page.Limit + 1)
	if page.Cursor != "" {
		q = q.Where("id > ?", page.Cursor)
	}
	var rows []commentRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list replies: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.Comment, len(rows))
	for i := range rows {
		out[i] = commentRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *commentRepo) IncReplyCount(ctx context.Context, parentID domain.ID) error {
	res := r.db.WithContext(ctx).Model(&commentRow{}).
		Where("id = ?", parentID).
		UpdateColumn("reply_count", gorm.Expr("reply_count + 1"))
	if res.Error != nil {
		return fmt.Errorf("inc reply count: %w", res.Error)
	}
	return nil
}

func (r *commentRepo) SoftDelete(ctx context.Context, id domain.ID) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&commentRow{})
	if res.Error != nil {
		return fmt.Errorf("soft delete: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateContent rewrites a comment's content and stamps edited_at and updated_at to
// at. The default GORM scope excludes soft-deleted rows, so a removed comment yields
// zero affected rows and ErrNotFound.
func (r *commentRepo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&commentRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"content":    content,
			"edited_at":  at,
			"updated_at": at,
		})
	if res.Error != nil {
		return fmt.Errorf("update comment content: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Like inserts a (comment, uid) like and bumps like_count in one transaction. A
// duplicate like by the same uid is ignored (ON CONFLICT DO NOTHING), keeping the
// count consistent. It returns the resulting like_count.
func (r *commentRepo) Like(ctx context.Context, commentID domain.ID, uid string) (int, error) {
	var count int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&commentLikeRow{CommentID: commentID, UID: uid, CreatedAt: time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 1 {
			if err := tx.Model(&commentRow{}).Where("id = ?", commentID).
				UpdateColumn("like_count", gorm.Expr("like_count + 1")).Error; err != nil {
				return err
			}
		}
		var row commentRow
		if err := tx.Select("like_count").First(&row, "id = ?", commentID).Error; err != nil {
			return err
		}
		count = row.LikeCount
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("like comment: %w", err)
	}
	return count, nil
}

// Unlike removes a (comment, uid) like and decrements like_count (never below zero)
// in one transaction, returning the resulting like_count. Unliking a comment that
// wasn't liked is a no-op.
func (r *commentRepo) Unlike(ctx context.Context, commentID domain.ID, uid string) (int, error) {
	var count int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("comment_id = ? AND uid = ?", commentID, uid).Delete(&commentLikeRow{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 1 {
			if err := tx.Model(&commentRow{}).Where("id = ?", commentID).
				UpdateColumn("like_count", gorm.Expr("GREATEST(like_count - 1, 0)")).Error; err != nil {
				return err
			}
		}
		var row commentRow
		if err := tx.Select("like_count").First(&row, "id = ?", commentID).Error; err != nil {
			return err
		}
		count = row.LikeCount
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("unlike comment: %w", err)
	}
	return count, nil
}

// CreateReport inserts a report. A repeat report by the same reporter for the same
// comment hits the (comment_id, reporter_uid) unique constraint; rather than relying
// on a driver-specific error code we insert with ON CONFLICT DO NOTHING and report a
// zero-row insert as ErrDuplicate.
func (r *commentRepo) CreateReport(ctx context.Context, rep *domain.CommentReport) error {
	row := &commentReportRow{
		ID:          rep.ID,
		CommentID:   rep.CommentID,
		ReporterUID: rep.ReporterUID,
		Reason:      rep.Reason,
		Status:      int16(domain.ReportStatusOpen),
		CreatedAt:   rep.CreatedAt,
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		return fmt.Errorf("create comment report: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrDuplicate
	}
	return nil
}

// SetStatus updates a comment's moderation status, returning ErrNotFound when no
// (non-deleted) row matches.
func (r *commentRepo) SetStatus(ctx context.Context, commentID domain.ID, status domain.CommentStatus) error {
	res := r.db.WithContext(ctx).Model(&commentRow{}).
		Where("id = ?", commentID).
		UpdateColumn("status", int16(status))
	if res.Error != nil {
		return fmt.Errorf("set comment status: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListReports returns reports against commentID newest-first, using the standard
// id-cursor pagination. An optional status filter narrows to one triage state.
func (r *commentRepo) ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus,
	page domain.Page) ([]*domain.CommentReport, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).Model(&commentReportRow{}).
		Where("comment_id = ?", commentID).
		Order("created_at DESC, id DESC").
		Limit(page.Limit + 1)
	if status != nil {
		q = q.Where("status = ?", int16(*status))
	}
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []commentReportRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list comment reports: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.CommentReport, len(rows))
	for i := range rows {
		out[i] = commentReportRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

// GetReport loads a single report by id, returning ErrNotFound when absent.
func (r *commentRepo) GetReport(ctx context.Context, reportID domain.ID) (*domain.CommentReport, error) {
	var row commentReportRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", reportID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get comment report: %w", err)
	}
	return commentReportRowToDomain(&row), nil
}

// ResolveReport transitions a report to a terminal status and stamps resolved_at/by,
// returning ErrNotFound when no row matches.
func (r *commentRepo) ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&commentReportRow{}).
		Where("id = ?", reportID).
		Updates(map[string]any{
			"status":      int16(status),
			"resolved_at": at,
			"resolved_by": resolverUID,
		})
	if res.Error != nil {
		return fmt.Errorf("resolve comment report: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchComments returns comments owned by ownerUID whose content matches the
// full-text query, ranked by relevance. Requires migration 000016.
func (r *commentRepo) SearchComments(ctx context.Context, ownerUID, query string, limit int) ([]*domain.Comment, error) {
	var rows []commentRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, owner_uid, target_type, target_id, parent_id, root_id, content,
		        like_count, reply_count, status, created_at, updated_at, edited_at
		 FROM comments
		 WHERE deleted_at IS NULL
		   AND owner_uid = ?
		   AND search_vector @@ plainto_tsquery('simple', ?)
		 ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
		 LIMIT ?`,
		ownerUID, query, query, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search comments: %w", err)
	}
	out := make([]*domain.Comment, len(rows))
	for i := range rows {
		out[i] = commentRowToDomain(&rows[i])
	}
	return out, nil
}

// SearchCommentsWithSnippet is like SearchComments but also returns a
// highlighted content snippet (via PostgreSQL ts_headline containing
// <mark>...</mark> tags) for each hit.
func (r *commentRepo) SearchCommentsWithSnippet(ctx context.Context, ownerUID, query string, limit int) ([]CommentSearchResult, error) {
	type cSnippetRow struct {
		commentRow
		Snippet string `gorm:"column:snippet"`
	}
	var rows []cSnippetRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, owner_uid, target_type, target_id, parent_id, root_id, content,
		        like_count, reply_count, status, created_at, updated_at, edited_at,
		        ts_headline('simple', content, plainto_tsquery('simple', ?),
		            'StartSel=<mark>, StopSel=</mark>, MaxWords=50, MinWords=20, ShortWord=3') AS snippet
		 FROM comments
		 WHERE deleted_at IS NULL
		   AND owner_uid = ?
		   AND search_vector @@ plainto_tsquery('simple', ?)
		 ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
		 LIMIT ?`,
		query, ownerUID, query, query, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search comments with snippet: %w", err)
	}
	out := make([]CommentSearchResult, len(rows))
	for i := range rows {
		out[i] = CommentSearchResult{
			Comment: commentRowToDomain(&rows[i].commentRow),
			Snippet: rows[i].Snippet,
		}
	}
	return out, nil
}

// SearchCommentsLike is a LIKE-based fallback for FTS-unsupported queries. It does
// ILIKE on the content column, scoped to the caller's own comments.
func (r *commentRepo) SearchCommentsLike(ctx context.Context, ownerUID, query string, limit int) ([]*domain.Comment, error) {
	var rows []commentRow
	pattern := "%" + query + "%"
	if err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL AND owner_uid = ? AND content ILIKE ?", ownerUID, pattern).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("search comments like: %w", err)
	}
	out := make([]*domain.Comment, len(rows))
	for i := range rows {
		out[i] = commentRowToDomain(&rows[i])
	}
	return out, nil
}
