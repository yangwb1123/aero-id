package http

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

// SearchHandler serves GET /v1/search.
type SearchHandler struct {
	svc service.SearchService
}

func NewSearchHandler(svc service.SearchService) *SearchHandler {
	return &SearchHandler{svc: svc}
}

func (h *SearchHandler) RegisterRoutes(r Routes) {
	r.Auth.GET("/search", h.Search)
}

func (h *SearchHandler) Search(c *gin.Context) {
	q := c.Query("q")
	if q == "" {
		httpx.WriteError(c, apperr.BadRequest("search.invalid", "q is required"))
		return
	}

	// Parse optional comma-separated kinds: messages,comments,files
	var kinds []domain.SearchKind
	if raw := c.Query("kind"); raw != "" {
		for _, k := range strings.Split(raw, ",") {
			switch strings.TrimSpace(k) {
			case string(domain.SearchKindMessages):
				kinds = append(kinds, domain.SearchKindMessages)
			case string(domain.SearchKindComments):
				kinds = append(kinds, domain.SearchKindComments)
			case string(domain.SearchKindFiles):
				kinds = append(kinds, domain.SearchKindFiles)
			}
		}
	}

	limit := 20
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}

	hits, err := h.svc.Search(c.Request.Context(), q, kinds, limit)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}

	httpx.OK(c, gin.H{"hits": toSearchHitResponses(hits)})
}

// ---- presentation DTO ----
// commentResponse, fileResponse, messageResponse and their converters are
// defined in comment.go, file.go, and message.go respectively (same package).

type searchHitResponse struct {
	Kind    string           `json:"kind"`
	Message *messageResponse `json:"message,omitempty"`
	Comment *commentResponse `json:"comment,omitempty"`
	File    *fileResponse    `json:"file,omitempty"`
}

func toSearchHitResponses(hits []domain.SearchHit) []searchHitResponse {
	out := make([]searchHitResponse, 0, len(hits))
	for _, h := range hits {
		resp := searchHitResponse{Kind: string(h.Kind)}
		switch h.Kind {
		case domain.SearchKindMessages:
			mr := toMessageResponse(h.Message)
			resp.Message = &mr
		case domain.SearchKindComments:
			cr := toCommentResponse(h.Comment)
			resp.Comment = &cr
		case domain.SearchKindFiles:
			fr := toFileResponse(h.File)
			resp.File = &fr
		}
		out = append(out, resp)
	}
	return out
}
