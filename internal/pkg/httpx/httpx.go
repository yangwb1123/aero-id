// Package httpx holds small HTTP rendering helpers shared by middleware and
// handlers: a uniform JSON envelope and AppError-aware error rendering.
package httpx

import (
	stderrors "errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteError renders err as JSON. An *AppError carries its own HTTP status and
// stable code; any other error becomes a 500 with a generic message so internal
// details are never leaked to clients.
func WriteError(c *gin.Context, err error) {
	rid := c.GetString("request_id")
	var ae *apperr.AppError
	if stderrors.As(err, &ae) {
		c.JSON(ae.HTTP, errorBody{Error: errorDetail{Code: ae.Code, Message: ae.Message, RequestID: rid}})
		return
	}
	c.JSON(http.StatusInternalServerError, errorBody{Error: errorDetail{
		Code: "internal.unexpected", Message: "internal server error", RequestID: rid,
	}})
}

// OK / Created / NoContent wrap a successful payload in a {"data": ...} envelope.
func OK(c *gin.Context, data any)      { c.JSON(http.StatusOK, gin.H{"data": data}) }
func Created(c *gin.Context, data any) { c.JSON(http.StatusCreated, gin.H{"data": data}) }
func NoContent(c *gin.Context)         { c.Status(http.StatusNoContent) }

// PageFromQuery reads pagination and optional filter query parameters into an
// (unnormalized) domain.Page; the repository clamps Limit via Page.Normalize.
// Supported optional filters: category (file category filter).
func PageFromQuery(c *gin.Context) domain.Page {
	p := domain.Page{Cursor: c.Query("cursor")}
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			p.Limit = n
		}
	}
	if cat := c.Query("category"); cat != "" {
		p.Category = domain.FileCategory(cat)
	}
	return p
}

// ParseIDFromParam extracts a path parameter named name from c, parses it as a
// UUID, and returns it as a domain.ID. Returns an AppError so the caller can
// render it as a JSON/gRPC error without further wrapping.
func ParseIDFromParam(c *gin.Context, name string) (domain.ID, error) {
	raw := c.Param(name)
	if raw == "" {
		return domain.ID{}, apperr.BadRequest("request.invalid_id", "missing "+name+" param")
	}
	id, err := idgen.Parse(raw)
	if err != nil {
		return domain.ID{}, apperr.BadRequest("request.invalid_id", "invalid "+name+" param")
	}
	return id, nil
}
