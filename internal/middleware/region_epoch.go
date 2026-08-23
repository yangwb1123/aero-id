package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// RegionEpochProvider reads the durable fencing epoch of a region (P6-02).
type RegionEpochProvider interface {
	RegionEpoch(ctx context.Context, region string) (int64, error)
}

// RegionEpochHeader implements the read-your-writes token (P6-05):
//
//   - account-affecting writes (POST/PATCH/DELETE under /v1) respond with
//     `X-Aero-Write-Epoch: <region>:<epoch>`, pinning the writer generation;
//   - reads carrying that header back get `X-Aero-Stale-Read: true` when the
//     local cell is not the generation the client last wrote to, so the client
//     can retry against the writer region instead of silently reading stale
//     projections.
//
// The epoch is cached briefly (1s) because promotions are rare, admin-driven
// events. Header staleness cannot grant writes: the fencing guard re-checks
// the durable epoch on every mutation (region.write_fenced fail-closed).
type RegionEpochHeader struct {
	provider    RegionEpochProvider
	localRegion string
	cachedEpoch atomic.Int64
	cachedAt    atomic.Int64 // unix nanos of the last successful read
	ttl         time.Duration
}

const regionEpochCacheTTL = time.Second

// NewRegionEpochHeader builds the read-your-writes middleware. A nil provider
// disables the middleware (single-cell deployments without fencing).
func NewRegionEpochHeader(provider RegionEpochProvider, localRegion string) *RegionEpochHeader {
	return &RegionEpochHeader{provider: provider, localRegion: strings.TrimSpace(localRegion), ttl: regionEpochCacheTTL}
}

// Middleware returns the gin handler.
func (h *RegionEpochHeader) Middleware() gin.HandlerFunc {
	if h == nil || h.provider == nil || h.localRegion == "" {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		epoch, ok := h.currentEpoch(c.Request.Context())
		h.markStaleReads(c, epoch, ok)
		c.Next()
		h.tagWrites(c, epoch, ok)
	}
}

// markStaleReads implements the read-your-writes check: a client that pinned a
// writer generation and is now reading from a different generation must be
// told it is stale, so it can retry against the writer region.
func (h *RegionEpochHeader) markStaleReads(c *gin.Context, epoch int64, ok bool) {
	raw := c.GetHeader("X-Aero-Write-Epoch")
	if raw == "" || !ok {
		return
	}
	region, clientEpoch, err := parseWriteEpoch(raw)
	if err == nil && (region != h.localRegion || clientEpoch != epoch) {
		c.Header("X-Aero-Stale-Read", "true")
	}
}

// tagWrites stamps successful account-affecting writes with the writer
// generation (P6-05 read-your-writes token).
func (h *RegionEpochHeader) tagWrites(c *gin.Context, epoch int64, ok bool) {
	if !ok || !isWriteMethod(c.Request.Method) {
		return
	}
	if c.Writer.Status() >= 200 && c.Writer.Status() < 300 {
		if c.Writer.Header().Get("X-Aero-Write-Epoch") != "" {
			return // the handler selected a different writer generation
		}
		c.Header("X-Aero-Write-Epoch", writeEpochHeader(h.localRegion, epoch))
	}
}

// SetWriteEpoch pins a successful response to a writer selected by the
// handler. Region promotion uses it because the new writer differs from the
// API cell that executed the administrative CAS.
func SetWriteEpoch(c *gin.Context, region string, epoch int64) {
	c.Header("X-Aero-Write-Epoch", writeEpochHeader(region, epoch))
}

// currentEpoch returns the cached region epoch, refreshing the cache after TTL.
// A provider failure degrades to no headers (writes are still durably fenced).
// Durable region epochs are strictly increasing (promotions bump them), so a
// stale provider response that raced a fresher one must never downgrade the
// cached generation: the freshest observed epoch wins, and only that response
// extends the cache freshness.
func (h *RegionEpochHeader) currentEpoch(ctx context.Context) (int64, bool) {
	now := time.Now().UnixNano()
	if h.cachedAt.Load() != 0 && now-h.cachedAt.Load() < int64(h.ttl) {
		return h.cachedEpoch.Load(), true
	}
	epoch, err := h.provider.RegionEpoch(ctx, h.localRegion)
	if err != nil {
		return 0, false
	}
	for {
		current := h.cachedEpoch.Load()
		if epoch < current {
			// A stale response lost the race to a fresher one; keep the
			// newer generation and its original freshness.
			return current, true
		}
		if h.cachedEpoch.CompareAndSwap(current, epoch) {
			h.cachedAt.Store(now)
			return epoch, true
		}
	}
}

func isWriteMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPatch || method == http.MethodDelete || method == http.MethodPut
}

func writeEpochHeader(region string, epoch int64) string {
	return region + ":" + strconv.FormatInt(epoch, 10)
}

func parseWriteEpoch(raw string) (string, int64, error) {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return "", 0, errInvalidWriteEpoch
	}
	epoch, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || epoch < 0 {
		return "", 0, errInvalidWriteEpoch
	}
	return strings.TrimSpace(parts[0]), epoch, nil
}

var errInvalidWriteEpoch = &writeEpochError{}

type writeEpochError struct{}

func (*writeEpochError) Error() string { return "invalid X-Aero-Write-Epoch header" }
