package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// regionEpochWriter decorates the commit points of a Gin response writer. Gin
// keeps status selection separate from header commitment, so WriteHeader alone
// must not be treated as a commit.
type regionEpochWriter struct {
	gin.ResponseWriter
	localRegion string
	epoch       int64
	eligible    bool
}

var _ gin.ResponseWriter = (*regionEpochWriter)(nil)

func newRegionEpochWriter(base gin.ResponseWriter, localRegion string, epoch int64, eligible bool) *regionEpochWriter {
	return &regionEpochWriter{
		ResponseWriter: base,
		localRegion:    localRegion,
		epoch:          epoch,
		eligible:       eligible,
	}
}

// prepare adds the epoch without committing the response. It is used after a
// handler that only selected a status, before Gin performs its final flush.
func (w *regionEpochWriter) prepare() {
	if !w.eligible || w.Written() || w.Status() < http.StatusOK || w.Status() >= http.StatusMultipleChoices {
		return
	}
	if w.Header().Get("X-Aero-Write-Epoch") == "" {
		w.Header().Set("X-Aero-Write-Epoch", writeEpochHeader(w.localRegion, w.epoch))
	}
}

// commit prepares metadata and delegates the actual commitment to the wrapped
// writer. Written is checked both here and by Gin's writer, making repeated
// commit calls harmless and preserving the wrapped status/body semantics.
func (w *regionEpochWriter) commit() {
	if w.Written() {
		return
	}
	w.prepare()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *regionEpochWriter) Write(p []byte) (int, error) {
	w.commit()
	return w.ResponseWriter.Write(p)
}

func (w *regionEpochWriter) WriteString(s string) (int, error) {
	w.commit()
	return w.ResponseWriter.WriteString(s)
}

func (w *regionEpochWriter) WriteHeaderNow() {
	w.commit()
}

func (w *regionEpochWriter) Flush() {
	w.commit()
	w.ResponseWriter.Flush()
}

// Unwrap keeps ResponseController and other net/http wrapper-aware callers
// connected to the original writer (including idempotency's capture writer).
func (w *regionEpochWriter) Unwrap() http.ResponseWriter {
	if unwrapper, ok := w.ResponseWriter.(interface{ Unwrap() http.ResponseWriter }); ok {
		return unwrapper.Unwrap()
	}
	return w.ResponseWriter
}
