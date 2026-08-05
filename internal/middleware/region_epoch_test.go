package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeEpochProvider struct {
	epoch int64
	err   error
}

func (f *fakeEpochProvider) RegionEpoch(ctx context.Context, region string) (int64, error) {
	return f.epoch, f.err
}

func runEpochRequest(t *testing.T, header *RegionEpochHeader, method, path string, requestEpoch string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(header.Middleware())
	router.GET("/v1/me", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.PATCH("/v1/me/profile", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.POST("/v1/me/erase", func(c *gin.Context) { c.Status(http.StatusBadRequest) })
	req := httptest.NewRequest(method, path, nil)
	if requestEpoch != "" {
		req.Header.Set("X-Aero-Write-Epoch", requestEpoch)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestRegionEpochHeader_WritesCarryEpoch: successful writes are tagged with
// the writer generation; failed writes are not.
func TestRegionEpochHeader_WritesCarryEpoch(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 3}, "us-east")
	if rec := runEpochRequest(t, header, http.MethodPatch, "/v1/me/profile", ""); rec.Header().Get("X-Aero-Write-Epoch") != "us-east:3" {
		t.Errorf("PATCH write epoch = %q, want us-east:3", rec.Header().Get("X-Aero-Write-Epoch"))
	}
	// 4xx responses are not tagged as successful writes.
	if rec := runEpochRequest(t, header, http.MethodPost, "/v1/me/erase", ""); rec.Header().Get("X-Aero-Write-Epoch") != "" {
		t.Errorf("failed write tagged: %q", rec.Header().Get("X-Aero-Write-Epoch"))
	}
}

// TestRegionEpochHeader_StaleReadDetected: a read pinned to an older generation
// is flagged; a read pinned to the current generation is not.
func TestRegionEpochHeader_StaleReadDetected(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 5}, "us-east")
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", "us-east:4"); rec.Header().Get("X-Aero-Stale-Read") != "true" {
		t.Error("stale generation read must be flagged")
	}
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", "us-east:5"); rec.Header().Get("X-Aero-Stale-Read") != "" {
		t.Error("current generation read must not be flagged")
	}
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", "eu-west:5"); rec.Header().Get("X-Aero-Stale-Read") != "true" {
		t.Error("other-region generation read must be flagged")
	}
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", ""); rec.Header().Get("X-Aero-Stale-Read") != "" {
		t.Error("unpinned read must not be flagged")
	}
}

// TestRegionEpochHeader_ProviderFailureDegradesToNoHeaders: a Redis/DB outage
// must not break requests; the durable fencing guard still protects writes.
func TestRegionEpochHeader_ProviderFailureDegradesToNoHeaders(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{err: errors.New("db down")}, "us-east")
	rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", "us-east:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Aero-Stale-Read") != "" || rec.Header().Get("X-Aero-Write-Epoch") != "" {
		t.Error("provider failure must not emit epoch headers")
	}
}

// TestRegionEpochHeader_CacheRefreshes: the epoch is cached for the TTL and
// refreshed afterwards (promotions land within ~1s on subsequent requests).
func TestRegionEpochHeader_CacheRefreshes(t *testing.T) {
	provider := &fakeEpochProvider{epoch: 1}
	header := NewRegionEpochHeader(provider, "us-east")
	header.ttl = 20 * time.Millisecond
	runEpochRequest(t, header, http.MethodGet, "/v1/me", "us-east:1")
	provider.epoch = 2 // promotion
	runEpochRequest(t, header, http.MethodGet, "/v1/me", "us-east:1")
	time.Sleep(30 * time.Millisecond) // let the cache TTL expire
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", "us-east:1"); rec.Header().Get("X-Aero-Stale-Read") != "true" {
		t.Error("cache must observe the promotion after TTL")
	}
}

func TestParseWriteEpoch(t *testing.T) {
	if region, epoch, err := parseWriteEpoch("us-east:7"); err != nil || region != "us-east" || epoch != 7 {
		t.Errorf("parse = %q/%d/%v", region, epoch, err)
	}
	if _, _, err := parseWriteEpoch("nope"); err == nil {
		t.Error("malformed header must fail")
	}
	if _, _, err := parseWriteEpoch("us-east:-1"); err == nil {
		t.Error("negative epoch must fail")
	}
}
