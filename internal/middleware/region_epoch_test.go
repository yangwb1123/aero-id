package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
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
	router.PATCH("/v1/me/profile/write", func(c *gin.Context) {
		_, _ = c.Writer.Write([]byte("ok"))
	})
	router.PATCH("/v1/me/profile/write-string", func(c *gin.Context) {
		_, _ = c.Writer.WriteString("ok")
	})
	router.PATCH("/v1/me/profile/flush", func(c *gin.Context) {
		c.Writer.Flush()
	})
	router.PATCH("/v1/me/profile/explicit", func(c *gin.Context) {
		c.Status(http.StatusOK)
		c.Writer.WriteHeaderNow()
	})
	router.PATCH("/v1/me/profile/status-change", func(c *gin.Context) {
		c.Status(http.StatusCreated)
		c.Status(http.StatusBadRequest)
		c.Writer.WriteHeaderNow()
	})
	router.POST("/v1/me/erase", func(c *gin.Context) { c.Status(http.StatusBadRequest) })
	router.POST("/v1/me/server-error", func(c *gin.Context) {
		c.Status(http.StatusInternalServerError)
		c.Writer.WriteHeaderNow()
	})
	router.POST("/v1/admin/regions/promote", func(c *gin.Context) {
		SetWriteEpoch(c, "eu-west", 9)
		c.Status(http.StatusOK)
		c.Writer.WriteHeaderNow()
	})
	req := httptest.NewRequest(method, path, nil)
	if requestEpoch != "" {
		req.Header.Set("X-Aero-Write-Epoch", requestEpoch)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestRegionEpochHeader_PreservesPromotedWriter(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 3}, "us-east")
	rec := runEpochRequest(t, header, http.MethodPost, "/v1/admin/regions/promote", "")
	if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "eu-west:9" {
		t.Fatalf("promoted write epoch = %q, want eu-west:9", got)
	}
}

// TestRegionEpochHeader_WritesCarryEpoch: successful writes are tagged with
// the writer generation; failed writes are not.
func TestRegionEpochHeader_WritesCarryEpoch(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 3}, "us-east")
	if rec := runEpochRequest(t, header, http.MethodPatch, "/v1/me/profile", ""); rec.Result().Header.Get("X-Aero-Write-Epoch") != "us-east:3" {
		t.Errorf("PATCH write epoch = %q, want us-east:3", rec.Result().Header.Get("X-Aero-Write-Epoch"))
	}
	// 4xx responses are not tagged as successful writes.
	if rec := runEpochRequest(t, header, http.MethodPost, "/v1/me/erase", ""); rec.Result().Header.Get("X-Aero-Write-Epoch") != "" {
		t.Errorf("failed write tagged: %q", rec.Result().Header.Get("X-Aero-Write-Epoch"))
	}
}

func TestRegionEpochHeader_CommitMethodsTagSuccessfulWrites(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 3}, "us-east")
	for _, tc := range []struct {
		path string
		body string
	}{
		{path: "/v1/me/profile/explicit"},
		{path: "/v1/me/profile/write", body: "ok"},
		{path: "/v1/me/profile/write-string", body: "ok"},
		{path: "/v1/me/profile/flush"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := runEpochRequest(t, header, http.MethodPatch, tc.path, "")
			if rec.Code != http.StatusOK || rec.Body.String() != tc.body {
				t.Fatalf("response = %d/%q, want 200/%q", rec.Code, rec.Body.String(), tc.body)
			}
			if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "us-east:3" {
				t.Fatalf("committed epoch = %q, want us-east:3", got)
			}
		})
	}
}

func TestRegionEpochHeader_StatusChangesBeforeCommitUseFinalStatus(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 3}, "us-east")
	rec := runEpochRequest(t, header, http.MethodPatch, "/v1/me/profile/status-change", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "" {
		t.Fatalf("4xx status was tagged: %q", got)
	}
}

func TestRegionEpochHeader_ServerErrorIsNotTagged(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 3}, "us-east")
	rec := runEpochRequest(t, header, http.MethodPost, "/v1/me/server-error", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "" {
		t.Fatalf("5xx response was tagged: %q", got)
	}
}

// TestRegionEpochHeader_StaleReadDetected: a read pinned to an older generation
// is flagged; a read pinned to the current generation is not.
func TestRegionEpochHeader_StaleReadDetected(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 5}, "us-east")
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", "us-east:4"); rec.Result().Header.Get("X-Aero-Stale-Read") != "true" {
		t.Error("stale generation read must be flagged")
	}
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", "us-east:5"); rec.Result().Header.Get("X-Aero-Stale-Read") != "" {
		t.Error("current generation read must not be flagged")
	}
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", "eu-west:5"); rec.Result().Header.Get("X-Aero-Stale-Read") != "true" {
		t.Error("other-region generation read must be flagged")
	}
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", ""); rec.Result().Header.Get("X-Aero-Stale-Read") != "" {
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
	if rec.Result().Header.Get("X-Aero-Stale-Read") != "" || rec.Result().Header.Get("X-Aero-Write-Epoch") != "" {
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
	if rec := runEpochRequest(t, header, http.MethodGet, "/v1/me", "us-east:1"); rec.Result().Header.Get("X-Aero-Stale-Read") != "true" {
		t.Error("cache must observe the promotion after TTL")
	}
}

type atomicEpochProvider struct {
	epoch *atomic.Int64
}

func (a *atomicEpochProvider) RegionEpoch(ctx context.Context, region string) (int64, error) {
	return a.epoch.Load(), nil
}

func TestRegionEpochHeader_PanicRestoresUnderlyingWriter(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 3}, "us-east")
	router := gin.New()
	router.Use(Recovery(zap.NewNop()))
	wrappedAtRecovery := false
	router.Use(func(c *gin.Context) {
		defer func() { _, wrappedAtRecovery = c.Writer.(*regionEpochWriter) }()
		c.Next()
	})
	router.Use(header.Middleware())
	router.POST("/v1/panic", func(c *gin.Context) { panic("boom") })

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic response status = %d, want 500; body = %q", rec.Code, rec.Body.String())
	}
	if wrappedAtRecovery {
		t.Fatal("region epoch writer was not restored during panic unwinding")
	}
	if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "" {
		t.Fatalf("panic response was tagged: %q", got)
	}
}

func TestRegionEpochHeader_DoesNotRepairAlreadyCommittedResponse(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 3}, "us-east")
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Status(http.StatusOK)
		c.Writer.WriteHeaderNow()
		c.Next()
	})
	router.Use(header.Middleware())
	router.PATCH("/v1/write", func(c *gin.Context) {})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/v1/write", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "" {
		t.Fatalf("already committed response was retroactively tagged %q", got)
	}
}

func TestRegionEpochHeader_DisabledConfigurationIsNoOp(t *testing.T) {
	cases := map[string]*RegionEpochHeader{
		"nil header":   nil,
		"nil provider": NewRegionEpochHeader(nil, "us-east"),
		"empty region": NewRegionEpochHeader(&fakeEpochProvider{epoch: 3}, " "),
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.Use(header.Middleware())
			router.PATCH("/v1/write", func(c *gin.Context) {
				_, _ = c.Writer.Write([]byte("ok"))
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/v1/write", nil))
			if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "" {
				t.Fatalf("disabled middleware emitted epoch %q", got)
			}
		})
	}
}

func TestRegionEpochHeader_ConcurrentRequestsRemainConsistent(t *testing.T) {
	var providerEpoch atomic.Int64
	providerEpoch.Store(7)
	header := NewRegionEpochHeader(&atomicEpochProvider{epoch: &providerEpoch}, "us-east")
	router := gin.New()
	router.Use(header.Middleware())
	router.PATCH("/v1/write", func(c *gin.Context) {
		_, _ = c.Writer.WriteString("ok")
	})

	const requests = 32
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/v1/write", nil))
			if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "us-east:7" {
				t.Errorf("concurrent epoch = %q, want us-east:7", got)
			}
		}()
	}
	wg.Wait()
}

// TestRegionEpochHeader_StaleResponseCannotDowngradeCache: a provider response
// carrying an older epoch (an out-of-order read of the durable store) must
// never replace the cached generation or extend its freshness.
func TestRegionEpochHeader_StaleResponseCannotDowngradeCache(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 2}, "us-east")
	if epoch, ok := header.currentEpoch(context.Background()); !ok || epoch != 2 {
		t.Fatalf("seed epoch = %d, %v; want 2", epoch, ok)
	}
	// Force a refresh; the provider answers with a stale, older value.
	header.provider = &fakeEpochProvider{epoch: 1}
	header.cachedAt.Store(0)
	epoch, ok := header.currentEpoch(context.Background())
	if !ok || epoch != 2 {
		t.Fatalf("epoch after stale refresh = %d, %v; want cached 2", epoch, ok)
	}
	if got := header.cachedEpoch.Load(); got != 2 {
		t.Fatalf("cached epoch downgraded to %d", got)
	}
}

// TestRegionEpochHeader_ConcurrentRefreshStaysMonotonic: concurrent refreshes
// after a promotion all converge on the new generation (exercises the CAS
// path under -race).
func TestRegionEpochHeader_ConcurrentRefreshStaysMonotonic(t *testing.T) {
	var providerEpoch atomic.Int64
	providerEpoch.Store(1)
	header := NewRegionEpochHeader(&atomicEpochProvider{epoch: &providerEpoch}, "us-east")
	if _, ok := header.currentEpoch(context.Background()); !ok {
		t.Fatal("seed failed")
	}
	providerEpoch.Store(3) // promotion
	header.cachedAt.Store(0)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if epoch, ok := header.currentEpoch(context.Background()); !ok || epoch != 3 {
				t.Errorf("concurrent refresh saw epoch %d, %v; want 3", epoch, ok)
			}
		}()
	}
	wg.Wait()
	if got := header.cachedEpoch.Load(); got != 3 {
		t.Fatalf("cached epoch = %d, want 3", got)
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
