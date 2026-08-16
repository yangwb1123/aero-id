package httpx_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
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
	const want = `{"data":{"key":"value"}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestCreated_returns201(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	httpx.Created(c, "created")
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d", w.Code)
	}
	const want = `{"data":"created"}`
	if got := w.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
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

func TestParseIDFromParam(t *testing.T) {
	const rawID = "01921234-5678-7abc-def0-123456789abc"
	expected, err := idgen.Parse(rawID)
	if err != nil {
		t.Fatalf("parse fixture %q: %v", rawID, err)
	}

	tests := []struct {
		name        string
		params      gin.Params
		wantID      domain.ID
		wantErrCode string
		wantMsg     string
	}{
		{
			name:   "valid uuid",
			params: gin.Params{{Key: "id", Value: rawID}},
			wantID: expected,
		},
		{
			name:        "missing param",
			wantErrCode: "request.invalid_id",
			wantMsg:     "missing id param",
		},
		{
			name:        "invalid uuid",
			params:      gin.Params{{Key: "id", Value: "not-a-uuid"}},
			wantErrCode: "request.invalid_id",
			wantMsg:     "invalid id param",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = tt.params

			got, err := httpx.ParseIDFromParam(c, "id")
			if tt.wantErrCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tt.wantID {
					t.Fatalf("id = %v, want %v", got, tt.wantID)
				}
				return
			}
			var ae *apperr.AppError
			if !errors.As(err, &ae) {
				t.Fatalf("error type = %T, want *AppError", err)
			}
			if ae.Code != tt.wantErrCode {
				t.Fatalf("code = %q, want %q", ae.Code, tt.wantErrCode)
			}
			if !strings.Contains(ae.Message, tt.wantMsg) {
				t.Fatalf("message = %q, want it to contain %q", ae.Message, tt.wantMsg)
			}
			if got != (domain.ID{}) {
				t.Fatalf("id = %v, want zero ID", got)
			}
		})
	}
}

func TestWriteError_nilError(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	httpx.WriteError(c, nil)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
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
	if body.Error.Code != "internal.unexpected" {
		t.Fatalf("code = %q, want %q", body.Error.Code, "internal.unexpected")
	}
	if body.Error.Message != "internal server error" {
		t.Fatalf("message = %q, want %q", body.Error.Message, "internal server error")
	}
}

func TestWriteError_doesNotLeakCause(t *testing.T) {
	const causeText = "secret internal detail: dial tcp 10.0.0.5:5432"
	appErr := apperr.Conflict("audit.chain_invalid", "chain verification failed").Wrap(errors.New(causeText))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	httpx.WriteError(c, appErr)

	if w.Code != appErr.HTTP {
		t.Fatalf("status = %d, want %d", w.Code, appErr.HTTP)
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
	if body.Error.Code != appErr.Code {
		t.Fatalf("code = %q, want %q", body.Error.Code, appErr.Code)
	}
	if body.Error.Message != appErr.Message {
		t.Fatalf("message = %q, want %q", body.Error.Message, appErr.Message)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(causeText)) {
		t.Fatalf("response body leaks cause text: %s", w.Body.String())
	}
}

func TestWriteError_echoesRequestID(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("request_id", "req-test-1234")
	httpx.WriteError(c, apperr.NotFound("test.not_found", "nope"))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	var body struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.RequestID != "req-test-1234" {
		t.Fatalf("request_id = %q, want %q", body.Error.RequestID, "req-test-1234")
	}
}

func TestPageFromQuery_categoryMapping(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?category=docs", nil)
	p := httpx.PageFromQuery(c)
	if p.Category != domain.FileCategory("docs") {
		t.Fatalf("Category = %q, want %q", p.Category, domain.FileCategory("docs"))
	}
}

func TestPageFromQuery_unnormalizedLimitPassthrough(t *testing.T) {
	tests := []struct {
		query string
		want  int
	}{
		{"?limit=-5", -5},
		{"?limit=999", 999},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/"+tt.query, nil)
			p := httpx.PageFromQuery(c)
			if p.Limit != tt.want {
				t.Fatalf("Limit = %d, want %d (unnormalized passthrough)", p.Limit, tt.want)
			}
		})
	}
}
