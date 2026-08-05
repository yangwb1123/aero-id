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
		// Read-your-writes check: a client that pinned a writer generation and
		// is now reading from a different generation must be told it is stale.
		if raw := c.GetHeader("X-Aero-Write-Epoch"); raw != "" && ok {
			region, clientEpoch, err := parseWriteEpoch(raw)
			if err == nil && (region != h.localRegion || clientEpoch != epoch) {
				c.Header("X-Aero-Stale-Read", "true")
			}
		}
		c.Next()
		// Tag successful writes with the writer generation.
		if ok && isWriteMethod(c.Request.Method) && c.Writer.Status() >= 200 && c.Writer.Status() < 300 {
			c.Header("X-Aero-Write-Epoch", writeEpochHeader(h.localRegion, epoch))
		}
	}
}

// currentEpoch returns the cached region epoch, refreshing the cache after TTL.
// A provider failure degrades to no headers (writes are still durably fenced).
func (h *RegionEpochHeader) currentEpoch(ctx context.Context) (int64, bool) {
	now := time.Now().UnixNano()
	if h.cachedAt.Load() != 0 && now-h.cachedAt.Load() < int64(h.ttl) {
		return h.cachedEpoch.Load(), true
	}
	epoch, err := h.provider.RegionEpoch(ctx, h.localRegion)
	if err != nil {
		return 0, false
	}
	h.cachedEpoch.Store(epoch)
	h.cachedAt.Store(now)
	return epoch, true
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
