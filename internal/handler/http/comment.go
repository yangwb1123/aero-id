package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type CommentHandler struct {
	comments service.CommentService
}

func NewCommentHandler(comments service.CommentService) *CommentHandler {
	return &CommentHandler{comments: comments}
}

func (h *CommentHandler) RegisterRoutes(r Routes) {
	r.Auth.POST("/comments", h.Create)
	r.Auth.GET("/comments", h.List)
	r.Auth.GET("/comments/:id/replies", h.Replies)
	r.Auth.DELETE("/comments/:id", h.Delete)
	r.Auth.POST("/comments/:id/like", h.Like)
	r.Auth.DELETE("/comments/:id/like", h.Unlike)
}

type createCommentRequest struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	ParentID   string `json:"parent_id"`
	Content    string `json:"content"`
}

func (h *CommentHandler) Create(c *gin.Context) {
	var req createCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("comment.invalid", "invalid request body"))
		return
	}
	targetID, err := idgen.Parse(req.TargetID)
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("comment.invalid", "invalid target_id"))
		return
	}
	in := service.CreateCommentInput{
		TargetType: domain.TargetType(req.TargetType),
		TargetID:   targetID,
		Content:    req.Content,
	}
	if req.ParentID != "" {
		pid, err := idgen.Parse(req.ParentID)
		if err != nil {
			httpx.WriteError(c, apperr.BadRequest("comment.invalid", "invalid parent_id"))
			return
		}
		in.ParentID = &pid
	}
	created, err := h.comments.Create(c.Request.Context(), in)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, toCommentResponse(created))
}

func (h *CommentHandler) List(c *gin.Context) {
	targetID, err := idgen.Parse(c.Query("target_id"))
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("comment.invalid", "invalid or missing target_id"))
		return
	}
	list, err := h.comments.List(c.Request.Context(), domain.TargetType(c.Query("target_type")), targetID, httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderCommentList(c, list)
}

func (h *CommentHandler) Replies(c *gin.Context) {
	rootID, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	list, err := h.comments.Replies(c.Request.Context(), rootID, httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderCommentList(c, list)
}

func (h *CommentHandler) Delete(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.comments.Delete(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *CommentHandler) Like(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	res, err := h.comments.Like(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"liked": res.Liked, "like_count": res.LikeCount})
}

func (h *CommentHandler) Unlike(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	res, err := h.comments.Unlike(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"liked": res.Liked, "like_count": res.LikeCount})
}

func renderCommentList(c *gin.Context, list *service.CommentList) {
	items := make([]commentResponse, 0, len(list.Comments))
	for _, cm := range list.Comments {
		items = append(items, toCommentResponse(cm))
	}
	httpx.OK(c, gin.H{
		"comments":    items,
		"users":       toUserMap(list.Users),
		"next_cursor": list.NextCursor,
	})
}

type commentResponse struct {
	ID         string    `json:"id"`
	OwnerUID   string    `json:"owner_uid"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	ParentID   *string   `json:"parent_id,omitempty"`
	RootID     string    `json:"root_id"`
	Content    string    `json:"content"`
	LikeCount  int       `json:"like_count"`
	ReplyCount int       `json:"reply_count"`
	Status     int16     `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

func toCommentResponse(c *domain.Comment) commentResponse {
	resp := commentResponse{
		ID:         c.ID.String(),
		OwnerUID:   c.OwnerUID,
		TargetType: string(c.TargetType),
		TargetID:   c.TargetID.String(),
		RootID:     c.RootID.String(),
		Content:    c.Content,
		LikeCount:  c.LikeCount,
		ReplyCount: c.ReplyCount,
		Status:     int16(c.Status),
		CreatedAt:  c.CreatedAt,
	}
	if c.ParentID != nil {
		s := c.ParentID.String()
		resp.ParentID = &s
	}
	return resp
}
