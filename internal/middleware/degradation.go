// Package middleware provides gin middleware for aero-id — auth, recovery,
// access log, CORS, request ID, rate limiting, ban checking, idempotency,
// sampling, and degradation detection.
package middleware

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

// DegradedDep identifies a dependency whose health the degradation manager tracks.
type DegradedDep string

const (
	DepDB      DegradedDep = "db"
	DepSSO     DegradedDep = "sso"
	DepStorage DegradedDep = "storage"
)

// DegradationProber checks whether a dependency is healthy. It should use a
// short, independent context timeout (typically 3s) and return nil when healthy.
type DegradationProber func(ctx context.Context) error

// DegradationState aggregates the health of upstream dependencies and drives the
// X-Aero-Degraded response header. It is safe for concurrent use.
//
// Usage:
//
//	state := NewDegradationState()
//	state.SetProber(DepDB, dbProber)
//	state.SetProber(DepSSO, ssoProber)
//	stop := state.StartProbing(ctx, 30*time.Second, 3, 2)
//	defer stop()
//	…
//	router.Use(middleware.DegradationHeader(state))
type DegradationState struct {
	// degraded is an atomic bitmask: bit 0 = DB, bit 1 = SSO, bit 2 = storage.
	degraded atomic.Uint32

	mu      sync.Mutex
	probers map[DegradedDep]DegradationProber
}

const (
	bitDB      uint32 = 1 << 0
	bitSSO     uint32 = 1 << 1
	bitStorage uint32 = 1 << 2
)

// NewDegradationState returns an initialized DegradationState with all
// dependencies marked healthy. No probers are registered by default.
func NewDegradationState() *DegradationState {
	return &DegradationState{probers: make(map[DegradedDep]DegradationProber)}
}

// SetDegraded marks a dependency as degraded (true) or healthy (false).
func (s *DegradationState) SetDegraded(dep DegradedDep, degraded bool) {
	bit := depBit(dep)
	for {
		cur := s.degraded.Load()
		var next uint32
		if degraded {
			next = cur | bit
		} else {
			next = cur & ^bit
		}
		if s.degraded.CompareAndSwap(cur, next) {
			return
		}
	}
}

// IsDegraded reports whether a specific dependency is currently degraded.
func (s *DegradationState) IsDegraded(dep DegradedDep) bool {
	return s.degraded.Load()&depBit(dep) != 0
}

// Status returns the set of degraded dependencies as a comma-separated string
// (e.g. "db,sso"), or "none" when every dependency is healthy. This value is
// written into the X-Aero-Degraded response header.
func (s *DegradationState) Status() string {
	mask := s.degraded.Load()
	if mask == 0 {
		return "none"
	}
	var deps []string
	if mask&bitDB != 0 {
		deps = append(deps, "db")
	}
	if mask&bitSSO != 0 {
		deps = append(deps, "sso")
	}
	if mask&bitStorage != 0 {
		deps = append(deps, "storage")
	}
	buf := make([]byte, 0, 32)
	for i, d := range deps {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, d...)
	}
	return string(buf)
}

// SetProber registers a health probe for a dependency. The probe is called
// periodically when StartProbing is active. A nil prober removes any existing
// registration for dep.
func (s *DegradationState) SetProber(dep DegradedDep, prober DegradationProber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prober == nil {
		delete(s.probers, dep)
	} else {
		s.probers[dep] = prober
	}
}

// StartProbing launches a background goroutine that probes all registered
// dependencies every interval. A dependency is marked degraded after
// failureThreshold consecutive failures, and restored to healthy after
// successThreshold consecutive successes. Returns a stop function.
//
//nolint:cyclop,funlen // the probe loop explicitly models per-dependency healthy and degraded thresholds.
func (s *DegradationState) StartProbing(ctx context.Context, interval time.Duration, failureThreshold, successThreshold int) func() {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if failureThreshold <= 0 {
		failureThreshold = 3
	}
	if successThreshold <= 0 {
		successThreshold = 2
	}

	t := time.NewTicker(interval)
	done := make(chan struct{})

	// Per-dep consecutive failure/success counters.
	type depState struct {
		consecutiveFailures  int
		consecutiveSuccesses int
	}
	var stateMu sync.Mutex
	states := make(map[DegradedDep]*depState)

	go func() {
		for {
			select {
			case <-t.C:
				s.mu.Lock()
				probers := make(map[DegradedDep]DegradationProber, len(s.probers))
				for k, v := range s.probers {
					probers[k] = v
				}
				s.mu.Unlock()

				for dep, probe := range probers {
					st := func() *depState {
						stateMu.Lock()
						defer stateMu.Unlock()
						if states[dep] == nil {
							states[dep] = &depState{}
						}
						return states[dep]
					}()

					probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
					err := probe(probeCtx)
					cancel()

					if err != nil {
						st.consecutiveFailures++
						st.consecutiveSuccesses = 0
						if st.consecutiveFailures >= failureThreshold {
							s.SetDegraded(dep, true)
						}
					} else {
						st.consecutiveSuccesses++
						st.consecutiveFailures = 0
						if st.consecutiveSuccesses >= successThreshold {
							s.SetDegraded(dep, false)
						}
					}
				}
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// degradationStateKey is the gin context key used to store the DegradationState
// reference so downstream handlers can read it (see DegradationFromCtx).
const degradationStateKey = "__deg_state"

// DegradationHeader returns a gin middleware that writes the X-Aero-Degraded
// response header with the current state summary on every request, AND stores
// the DegradationState reference in the gin context so downstream handlers can
// consult it via DegradationFromCtx / AbortIfDegraded.
//
// It does not block or modify the response body; it only decorates the response
// with operational metadata and provides the state reference.
func DegradationHeader(state *DegradationState) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(degradationStateKey, state)
		c.Next()
		c.Header("X-Aero-Degraded", state.Status())
	}
}

// DegradationFromCtx returns the DegradationState stored in the gin context by
// the DegradationHeader middleware, or nil if not set (which makes it safe to
// call in handlers that may not have the middleware installed).
func DegradationFromCtx(c *gin.Context) *DegradationState {
	v, ok := c.Get(degradationStateKey)
	if !ok {
		return nil
	}
	s, _ := v.(*DegradationState)
	return s
}

// depBit returns the bitmask for a dependency.
func depBit(dep DegradedDep) uint32 {
	switch dep {
	case DepDB:
		return bitDB
	case DepSSO:
		return bitSSO
	case DepStorage:
		return bitStorage
	default:
		panic("degradation: unknown dependency: " + string(dep))
	}
}

// DegradationConfig holds the configuration for the degradation state manager.
type DegradationConfig struct {
	ProbeInterval    time.Duration
	FailureThreshold int
	SuccessThreshold int
}

// NewDegradationStateFromConfig creates a DegradationState and starts probing
// with the given config. Returns the state, a stop function, and a logger that
// the caller should use to log degradation transitions.
func NewDegradationStateFromConfig(
	cfg DegradationConfig,
	log *zap.Logger,
	dbProber DegradationProber,
	ssoProber DegradationProber,
	storageProber DegradationProber,
) (*DegradationState, func()) {
	state := NewDegradationState()
	if dbProber != nil {
		state.SetProber(DepDB, dbProber)
	}
	if ssoProber != nil {
		state.SetProber(DepSSO, ssoProber)
	}
	if storageProber != nil {
		state.SetProber(DepStorage, storageProber)
	}
	stop := state.StartProbing(context.Background(),
		cfg.ProbeInterval,
		cfg.FailureThreshold,
		cfg.SuccessThreshold,
	)
	return state, stop
}

// ─────────────────────────────────────────────────────────────────────
// Handler adaptation helpers — unified behaviour for handlers to consult
// the current degradation state and respond appropriately.
// ─────────────────────────────────────────────────────────────────────

// DegradedResponse is a compact response style for degraded endpoints.
// Use this in handlers that depend on a degraded dependency.
type DegradedResponse int

const (
	// DegradedStaleOK — return stale/cached data with a 200 OK and a
	// warning header. Suitable for read endpoints whose data source is
	// degraded but the handler has a cached/fallback path.
	DegradedStaleOK DegradedResponse = iota

	// DegradedFail503 — abort the request with 503 Service Unavailable.
	// Suitable for write endpoints or reads that cannot serve stale data.
	DegradedFail503

	// DegradedProceed — no special action; the endpoint is unaffected
	// by the current degradation state. The X-Aero-Degraded header still
	// informs the client.
	DegradedProceed
)

// DegradationAction returns the suggested behaviour for an endpoint that
// depends on the given set of dependencies. This is the central policy
// function that every handler should call to determine its degradation
// response.
//
// deps is the list of dependencies this specific operation requires.
// The function checks each dep; if ANY required dep is degraded and
// the policy says 503, it returns DegradedFail503.
//
// Default policies:
//   - DB degraded + endpoint requires DB → DegradedFail503
//   - SSO degraded + endpoint requires SSO → DegradedFail503
//   - Storage degraded + endpoint requires storage → DegradedFail503
//   - Read-only endpoints with DB degraded → DegradedStaleOK (2020-06 RFC)
//     but the handler must implement its own stale-data fallback.
func DegradationAction(state *DegradationState, deps []DegradedDep, readOnly bool) DegradedResponse {
	if state == nil {
		return DegradedProceed
	}
	for _, dep := range deps {
		if state.IsDegraded(dep) {
			// DB degraded for reads can serve stale data.
			if dep == DepDB && readOnly {
				return DegradedStaleOK
			}
			return DegradedFail503
		}
	}
	return DegradedProceed
}

// DepsForOp is a convenience to build the dependency list for DegradationAction.
func DepsForOp(deps ...DegradedDep) []DegradedDep { return deps }

// AbortIfDegraded checks the degradation state against the required dependencies
// and, if the policy says the request should be refused, writes a 503 response
// and aborts the gin context. It returns true when the request was aborted
// (callers should return early) and false when the request may proceed.
//
// Usage in a handler:
//
//	if middleware.AbortIfDegraded(c, middleware.DepsForOp(middleware.DepDB), !readOnly) {
//	    return
//	}
func AbortIfDegraded(c *gin.Context, deps []DegradedDep, readOnly bool) bool {
	state := DegradationFromCtx(c)
	if state == nil {
		return false
	}
	action := DegradationAction(state, deps, readOnly)
	if action == DegradedFail503 {
		httpx.WriteError(c, apperr.Internal("degraded", "service unavailable — upstream dependency degraded"))
		c.Abort()
		return true
	}
	return false
}
