package comment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
)

type commentRow struct {
	ID         uuid.UUID      `gorm:"column:id;primaryKey"`
	OwnerUID   string         `gorm:"column:owner_uid"`
	TargetType string         `gorm:"column:target_type"`
	TargetID   uuid.UUID      `gorm:"column:target_id"`
	ParentID   *uuid.UUID     `gorm:"column:parent_id"`
	RootID     uuid.UUID      `gorm:"column:root_id"`
	Content    string         `gorm:"column:content"`
	LikeCount  int            `gorm:"column:like_count"`
	ReplyCount int            `gorm:"column:reply_count"`
	Status     int16          `gorm:"column:status"`
	CreatedAt  time.Time      `gorm:"column:created_at"`
	UpdatedAt  time.Time      `gorm:"column:updated_at"`
	EditedAt   *time.Time     `gorm:"column:edited_at"`
	DeletedAt  gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (commentRow) TableName() string { return "comments" }

func commentRowToDomain(r *commentRow) *Comment {
	return &Comment{
		ID:         r.ID,
		OwnerUID:   r.OwnerUID,
		TargetType: domain.TargetType(r.TargetType),
		TargetID:   r.TargetID,
		ParentID:   r.ParentID,
		RootID:     r.RootID,
		Content:    r.Content,
		LikeCount:  r.LikeCount,
		ReplyCount: r.ReplyCount,
		Status:     domain.CommentStatus(r.Status),
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
		EditedAt:   r.EditedAt,
	}
}

func commentDomainToRow(c *Comment) *commentRow {
	return &commentRow{
		ID:         c.ID,
		OwnerUID:   c.OwnerUID,
		TargetType: string(c.TargetType),
		TargetID:   c.TargetID,
		ParentID:   c.ParentID,
		RootID:     c.RootID,
		Content:    c.Content,
		LikeCount:  c.LikeCount,
		ReplyCount: c.ReplyCount,
		Status:     int16(c.Status),
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
		EditedAt:   c.EditedAt,
	}
}

type commentLikeRow struct {
	CommentID uuid.UUID `gorm:"column:comment_id;primaryKey"`
	UID       string    `gorm:"column:uid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (commentLikeRow) TableName() string { return "comment_likes" }

type commentReportRow struct {
	ID          uuid.UUID  `gorm:"column:id;primaryKey"`
	CommentID   uuid.UUID  `gorm:"column:comment_id"`
	ReporterUID string     `gorm:"column:reporter_uid"`
	Reason      string     `gorm:"column:reason"`
	Status      int16      `gorm:"column:status"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	ResolvedAt  *time.Time `gorm:"column:resolved_at"`
	ResolvedBy  string     `gorm:"column:resolved_by"`
}

func (commentReportRow) TableName() string { return "comment_reports" }

func commentReportRowToDomain(r *commentReportRow) *CommentReport {
	return &CommentReport{
		ID:          r.ID,
		CommentID:   r.CommentID,
		ReporterUID: r.ReporterUID,
		Reason:      r.Reason,
		Status:      domain.ReportStatus(r.Status),
		CreatedAt:   r.CreatedAt,
		ResolvedAt:  r.ResolvedAt,
		ResolvedBy:  r.ResolvedBy,
	}
}

type repo struct {
	db     *gorm.DB
	outbox outbox.Repository
}

func NewRepository(db *gorm.DB, ob outbox.Repository) Repository {
	return &repo{db: db, outbox: ob}
}

func (r *repo) Create(ctx context.Context, c *Comment) error {
	if err := r.db.WithContext(ctx).Create(commentDomainToRow(c)).Error; err != nil {
		return fmt.Errorf("create comment: %w", err)
	}
	return nil
}

func (r *repo) CreateWithReplyBump(ctx context.Context, c *Comment, evt *outbox.Event) error {
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

func (r *repo) Get(ctx context.Context, id domain.ID) (*Comment, error) {
	var row commentRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get comment: %w", err)
	}
	return commentRowToDomain(&row), nil
}

func (r *repo) ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*Comment, string, error) {
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
	out := make([]*Comment, len(rows))
	for i := range rows {
		out[i] = commentRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *repo) ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*Comment, string, error) {
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
	out := make([]*Comment, len(rows))
	for i := range rows {
		out[i] = commentRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *repo) IncReplyCount(ctx context.Context, parentID domain.ID) error {
	res := r.db.WithContext(ctx).Model(&commentRow{}).
		Where("id = ?", parentID).
		UpdateColumn("reply_count", gorm.Expr("reply_count + 1"))
	if res.Error != nil {
		return fmt.Errorf("inc reply count: %w", res.Error)
	}
	return nil
}

func (r *repo) SoftDelete(ctx context.Context, id domain.ID) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&commentRow{})
	if res.Error != nil {
		return fmt.Errorf("soft delete: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *repo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
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
		return domain.ErrNotFound
	}
	return nil
}

func (r *repo) Like(ctx context.Context, commentID domain.ID, uid string) (int, error) {
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

func (r *repo) Unlike(ctx context.Context, commentID domain.ID, uid string) (int, error) {
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

func (r *repo) CreateReport(ctx context.Context, rep *CommentReport) error {
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
		return domain.ErrDuplicate
	}
	return nil
}

func (r *repo) SetStatus(ctx context.Context, commentID domain.ID, status domain.CommentStatus) error {
	res := r.db.WithContext(ctx).Model(&commentRow{}).
		Where("id = ?", commentID).
		UpdateColumn("status", int16(status))
	if res.Error != nil {
		return fmt.Errorf("set comment status: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *repo) ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) ([]*CommentReport, string, error) {
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
	out := make([]*CommentReport, len(rows))
	for i := range rows {
		out[i] = commentReportRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *repo) GetReport(ctx context.Context, reportID domain.ID) (*CommentReport, error) {
	var row commentReportRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", reportID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get comment report: %w", err)
	}
	return commentReportRowToDomain(&row), nil
}

func (r *repo) ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time) error {
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
		return domain.ErrNotFound
	}
	return nil
}

func (r *repo) SearchComments(ctx context.Context, ownerUID, query string, limit int) ([]*Comment, error) {
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
	out := make([]*Comment, len(rows))
	for i := range rows {
		out[i] = commentRowToDomain(&rows[i])
	}
	return out, nil
}
