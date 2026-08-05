package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

func TestWriteError_withAppError(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	appErr := apperr.NotFound("test.not_found", "resource not found")
	httpx.WriteError(c, appErr)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "test.not_found" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestWriteError_withGenericError(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	httpx.WriteError(c, http.ErrAbortHandler)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "internal.unexpected" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestOK_returnsData(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	httpx.OK(c, gin.H{"key": "value"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !json.Valid(w.Body.Bytes()) {
		t.Fatal("invalid JSON")
	}
}

func TestCreated_returns201(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	httpx.Created(c, "created")
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestNoContent_returns204(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	httpx.NoContent(c)
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d", c.Writer.Status())
	}
}

func TestPageFromQuery_withLimitAndCursor(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?limit=10&cursor=abc", nil)
	p := httpx.PageFromQuery(c)
	if p.Limit != 10 || p.Cursor != "abc" {
		t.Fatalf("Page{Limit=%d, Cursor=%q}", p.Limit, p.Cursor)
	}
}

func TestPageFromQuery_defaults(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	p := httpx.PageFromQuery(c)
	if p.Limit != 0 || p.Cursor != "" {
		t.Fatalf("Page{Limit=%d, Cursor=%q}", p.Limit, p.Cursor)
	}
}

func TestPageFromQuery_invalidLimitIgnored(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?limit=abc", nil)
	p := httpx.PageFromQuery(c)
	if p.Limit != 0 {
		t.Fatalf("invalid limit should be ignored, got %d", p.Limit)
	}
}
