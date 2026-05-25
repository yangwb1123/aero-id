package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

type CommentRepository interface {
	Create(ctx context.Context, c *domain.Comment) error
	// CreateWithReplyBump inserts a comment and, when it is a reply (ParentID set),
	// increments the parent's reply_count in the same transaction.
	CreateWithReplyBump(ctx context.Context, c *domain.Comment) error
	Get(ctx context.Context, id domain.ID) (*domain.Comment, error)
	ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	IncReplyCount(ctx context.Context, parentID domain.ID) error
	SoftDelete(ctx context.Context, id domain.ID) error
	// Like records a like by uid and returns the comment's updated like_count. A repeated
	// like by the same uid is a no-op (idempotent). Unlike is the inverse.
	Like(ctx context.Context, commentID domain.ID, uid string) (likeCount int, err error)
	Unlike(ctx context.Context, commentID domain.ID, uid string) (likeCount int, err error)
}

type commentRepo struct{ db *gorm.DB }

func NewCommentRepository(db *gorm.DB) CommentRepository { return &commentRepo{db: db} }

func (r *commentRepo) Create(ctx context.Context, c *domain.Comment) error {
	if err := r.db.WithContext(ctx).Create(commentDomainToRow(c)).Error; err != nil {
		return fmt.Errorf("create comment: %w", err)
	}
	return nil
}

// CreateWithReplyBump inserts a comment and, when it is a reply (ParentID set),
// increments the parent's reply_count in the same transaction so the comment and
// the denormalized counter commit together (or not at all).
func (r *commentRepo) CreateWithReplyBump(ctx context.Context, c *domain.Comment) error {
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
		return nil
	})
	if err != nil {
		return fmt.Errorf("create comment with reply bump: %w", err)
	}
	return nil
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
		Where("target_type = ? AND target_id = ? AND parent_id IS NULL", string(t), target).
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
		Where("root_id = ? AND id <> root_id", rootID).
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
