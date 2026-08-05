package search

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httphandler "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

type SearchHandler struct {
	svc Service
}

func NewSearchHandler(svc Service) *SearchHandler { return &SearchHandler{svc: svc} }

func (h *SearchHandler) RegisterRoutes(r httphandler.Routes) {
	r.Auth.GET("/search", h.Search)
}

func (h *SearchHandler) Search(c *gin.Context) {
	query := c.Query("q")
	kindStr := c.Query("kinds")
	var kinds []domain.SearchKind
	if kindStr != "" {
		for _, k := range strings.Split(kindStr, ",") {
			kinds = append(kinds, domain.SearchKind(strings.TrimSpace(k)))
		}
	}
	hits, err := h.svc.Search(c.Request.Context(), query, kinds, defaultLimit)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items := make([]gin.H, 0, len(hits))
	for _, hit := range hits {
		items = append(items, gin.H{"kind": string(hit.Kind), "result": hit})
	}
	httpx.OK(c, gin.H{"results": items})
}
