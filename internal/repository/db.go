package repository

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// ErrNotFound is returned by repository methods when no row matches.
var ErrNotFound = errors.New("repository: not found")

// ErrDuplicate is returned when an insert violates a uniqueness constraint that the
// caller is expected to treat as a benign conflict (e.g. a repeated report).
var ErrDuplicate = errors.New("repository: duplicate")

// fileRow is the GORM-mapped representation of the files table.
type fileRow struct {
	ID          uuid.UUID         `gorm:"column:id;primaryKey"`
	OwnerUID    string            `gorm:"column:owner_uid"`
	Name        string            `gorm:"column:name"`
	Size        int64             `gorm:"column:size"`
	MimeType    string            `gorm:"column:mime_type"`
	Category    string            `gorm:"column:category;default:other"`
	Status      int16             `gorm:"column:status;default:1"`
	StorageKey  string            `gorm:"column:storage_key"`
	StorageType string            `gorm:"column:storage_type"`
	Checksum    *string           `gorm:"column:checksum"`
	Visibility  int16             `gorm:"column:visibility"`
	Meta        datatypes.JSONMap `gorm:"column:meta"`
	CreatedAt   time.Time         `gorm:"column:created_at"`
	UpdatedAt   time.Time         `gorm:"column:updated_at"`
	DeletedAt   gorm.DeletedAt    `gorm:"column:deleted_at;index"`
}

func (fileRow) TableName() string { return "files" }

func fileRowToDomain(r *fileRow) *domain.File {
	f := &domain.File{
		ID:          r.ID,
		OwnerUID:    r.OwnerUID,
		Name:        r.Name,
		Size:        r.Size,
		MimeType:    r.MimeType,
		Category:    domain.FileCategory(r.Category),
		Status:      domain.FileStatus(r.Status),
		StorageKey:  r.StorageKey,
		StorageType: r.StorageType,
		Visibility:  domain.Visibility(r.Visibility),
		Meta:        map[string]any(r.Meta),
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
	if r.Checksum != nil {
		f.Checksum = *r.Checksum
	}
	return f
}

func fileDomainToRow(f *domain.File) *fileRow {
	r := &fileRow{
		ID:          f.ID,
		OwnerUID:    f.OwnerUID,
		Name:        f.Name,
		Size:        f.Size,
		MimeType:    f.MimeType,
		Category:    string(f.Category),
		Status:      int16(f.Status),
		StorageKey:  f.StorageKey,
		StorageType: f.StorageType,
		Visibility:  int16(f.Visibility),
		Meta:        datatypes.JSONMap(f.Meta),
		CreatedAt:   f.CreatedAt,
		UpdatedAt:   f.UpdatedAt,
	}
	if f.Checksum != "" {
		c := f.Checksum
		r.Checksum = &c
	}
	return r
}

type messageRow struct {
	ID        uuid.UUID         `gorm:"column:id;primaryKey"`
	FromUID   string            `gorm:"column:from_uid"`
	ToUID     string            `gorm:"column:to_uid"`
	Kind      int16             `gorm:"column:kind"`
	Content   string            `gorm:"column:content"`
	FileID    *uuid.UUID        `gorm:"column:file_id"`
	ReadAt    *time.Time        `gorm:"column:read_at"`
	EditedAt  *time.Time        `gorm:"column:edited_at"`
	Meta      datatypes.JSONMap `gorm:"column:meta"`
	CreatedAt time.Time         `gorm:"column:created_at"`
	DeletedAt gorm.DeletedAt    `gorm:"column:deleted_at;index"`
}

func (messageRow) TableName() string { return "messages" }

func msgRowToDomain(r *messageRow) *domain.Message {
	m := &domain.Message{
		ID:        r.ID,
		FromUID:   r.FromUID,
		ToUID:     r.ToUID,
		Kind:      domain.MessageKind(r.Kind),
		Content:   r.Content,
		FileID:    r.FileID,
		ReadAt:    r.ReadAt,
		EditedAt:  r.EditedAt,
		Meta:      map[string]any(r.Meta),
		CreatedAt: r.CreatedAt,
	}
	return m
}

func msgDomainToRow(m *domain.Message) *messageRow {
	return &messageRow{
		ID:        m.ID,
		FromUID:   m.FromUID,
		ToUID:     m.ToUID,
		Kind:      int16(m.Kind),
		Content:   m.Content,
		FileID:    m.FileID,
		ReadAt:    m.ReadAt,
		EditedAt:  m.EditedAt,
		Meta:      datatypes.JSONMap(m.Meta),
		CreatedAt: m.CreatedAt,
	}
}

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

func commentRowToDomain(r *commentRow) *domain.Comment {
	return &domain.Comment{
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

func commentDomainToRow(c *domain.Comment) *commentRow {
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

// commentLikeRow maps the comment_likes join table: one row per (comment, user) like.
type commentLikeRow struct {
	CommentID uuid.UUID `gorm:"column:comment_id;primaryKey"`
	UID       string    `gorm:"column:uid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (commentLikeRow) TableName() string { return "comment_likes" }

// fileShareRow maps the file_shares join table: one row per (file, grantee) grant.
type fileShareRow struct {
	FileID     uuid.UUID `gorm:"column:file_id;primaryKey"`
	GranteeUID string    `gorm:"column:grantee_uid;primaryKey"`
	OwnerUID   string    `gorm:"column:owner_uid"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (fileShareRow) TableName() string { return "file_shares" }

// commentReportRow maps the comment_reports table: one row per (comment, reporter).
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

func commentReportRowToDomain(r *commentReportRow) *domain.CommentReport {
	return &domain.CommentReport{
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

// MessageSearchResult is returned by SearchMessages: the matched message plus
// a highlighted snippet (ts_headline) of the content that matched the query.
type MessageSearchResult struct {
	Message *domain.Message
	Snippet string
}

// CommentSearchResult is returned by SearchComments: the matched comment plus
// a highlighted snippet (ts_headline) of the content that matched the query.
type CommentSearchResult struct {
	Comment *domain.Comment
	Snippet string
}

// messageReactionRow maps the message_reactions join table: one row per (message, user) reaction.
type messageReactionRow struct {
	MessageID uuid.UUID `gorm:"column:message_id;primaryKey"`
	UID       string    `gorm:"column:uid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (messageReactionRow) TableName() string { return "message_reactions" }

// auditEventRow maps the audit_events table.
type auditEventRow struct {
	ID          uuid.UUID         `gorm:"column:id;primaryKey"`
	ActorUID    string            `gorm:"column:actor_uid"`
	Action      string            `gorm:"column:action"`
	SubjectType string            `gorm:"column:subject_type"`
	SubjectID   string            `gorm:"column:subject_id"`
	TargetUID   string            `gorm:"column:target_uid"`
	Meta        datatypes.JSONMap `gorm:"column:meta"`
	CreatedAt   time.Time         `gorm:"column:created_at"`
}

func (auditEventRow) TableName() string { return "audit_events" }

func auditEventDomainToRow(e *domain.AuditEvent) *auditEventRow {
	return &auditEventRow{
		ID:          e.ID,
		ActorUID:    e.ActorUID,
		Action:      e.Action,
		SubjectType: e.SubjectType,
		SubjectID:   e.SubjectID,
		TargetUID:   e.TargetUID,
		Meta:        datatypes.JSONMap(e.Meta),
		CreatedAt:   e.CreatedAt,
	}
}

func auditEventRowToDomain(r *auditEventRow) *domain.AuditEvent {
	return &domain.AuditEvent{
		ID:          r.ID,
		ActorUID:    r.ActorUID,
		Action:      r.Action,
		SubjectType: r.SubjectType,
		SubjectID:   r.SubjectID,
		TargetUID:   r.TargetUID,
		Meta:        map[string]any(r.Meta),
		CreatedAt:   r.CreatedAt,
	}
}

// notificationRow maps the notifications table: one row per recipient per event.
type notificationRow struct {
	ID             uuid.UUID  `gorm:"column:id;primaryKey"`
	RecipientUID   string     `gorm:"column:recipient_uid"`
	Type           string     `gorm:"column:type"`
	ActorUID       string     `gorm:"column:actor_uid"`
	SubjectType    string     `gorm:"column:subject_type"`
	SubjectID      string     `gorm:"column:subject_id"`
	ReadAt         *time.Time `gorm:"column:read_at"`
	CollapsedCount int        `gorm:"column:collapsed_count"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
}

func (notificationRow) TableName() string { return "notifications" }

func notificationRowToDomain(r *notificationRow) *domain.Notification {
	return &domain.Notification{
		ID:             r.ID,
		RecipientUID:   r.RecipientUID,
		Type:           r.Type,
		ActorUID:       r.ActorUID,
		SubjectType:    r.SubjectType,
		SubjectID:      r.SubjectID,
		ReadAt:         r.ReadAt,
		CollapsedCount: r.CollapsedCount,
		CreatedAt:      r.CreatedAt,
	}
}

func notificationDomainToRow(n *domain.Notification) *notificationRow {
	return &notificationRow{
		ID:             n.ID,
		RecipientUID:   n.RecipientUID,
		Type:           n.Type,
		ActorUID:       n.ActorUID,
		SubjectType:    n.SubjectType,
		SubjectID:      n.SubjectID,
		ReadAt:         n.ReadAt,
		CollapsedCount: n.CollapsedCount,
		CreatedAt:      n.CreatedAt,
	}
}
