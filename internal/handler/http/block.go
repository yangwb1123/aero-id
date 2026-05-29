package http

import (
	"github.com/gin-gonic/gin"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

type BlockHandler struct {
	blocks service.BlockService
}

func NewBlockHandler(blocks service.BlockService) *BlockHandler {
	return &BlockHandler{blocks: blocks}
}

func (h *BlockHandler) RegisterRoutes(r Routes) {
	r.Auth.POST("/blocks", h.Block)
	r.Auth.GET("/blocks", h.List)
	r.Auth.DELETE("/blocks/:uid", h.Unblock)
}

type blockRequest struct {
	UID string `json:"uid"`
}

func (h *BlockHandler) Block(c *gin.Context) {
	var req blockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("block.invalid", "invalid request body"))
		return
	}
	if err := h.blocks.Block(c.Request.Context(), req.UID); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *BlockHandler) Unblock(c *gin.Context) {
	if err := h.blocks.Unblock(c.Request.Context(), c.Param("uid")); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *BlockHandler) List(c *gin.Context) {
	list, err := h.blocks.ListBlocked(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	uids := list.UIDs
	if uids == nil {
		uids = []string{}
	}
	httpx.OK(c, gin.H{
		"blocked":     uids,
		"next_cursor": list.NextCursor,
	})
}
