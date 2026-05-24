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

// PageFromQuery reads ?limit= and ?cursor= into an (unnormalized) domain.Page;
// the repository clamps Limit via Page.Normalize.
func PageFromQuery(c *gin.Context) domain.Page {
	p := domain.Page{Cursor: c.Query("cursor")}
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			p.Limit = n
		}
	}
	return p
}
