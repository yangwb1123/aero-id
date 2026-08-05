package block

import (
	"github.com/gin-gonic/gin"

	httphandler "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

type BlockHandler struct {
	blocks Service
}

func NewBlockHandler(blocks Service) *BlockHandler { return &BlockHandler{blocks: blocks} }

func (h *BlockHandler) RegisterRoutes(r httphandler.Routes) {
	r.Auth.POST("/blocks", h.Block)
	r.Auth.DELETE("/blocks/:uid", h.Unblock)
	r.Auth.GET("/blocks", h.ListBlocked)
}

func (h *BlockHandler) Block(c *gin.Context) {
	var body struct {
		UID string `json:"uid"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("block.invalid", "invalid request body"))
		return
	}
	if err := h.blocks.Block(c.Request.Context(), body.UID); err != nil {
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

func (h *BlockHandler) ListBlocked(c *gin.Context) {
	list, err := h.blocks.ListBlocked(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"uids": list.UIDs, "next_cursor": list.NextCursor})
}
