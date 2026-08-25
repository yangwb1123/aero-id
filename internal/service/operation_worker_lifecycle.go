package service

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// The connector currently bounds a source call (including its retry budget) at
// five minutes. The lease is deliberately longer than that budget plus the
// durable-finish margin. All forward, reconcile, and compensation claims use
// these same bounds; a healthy worker must not lose its lease while a source
// call is still in flight.
const (
	operationRemoteTimeout  = 5 * time.Minute
	operationLeaseDuration  = 6 * time.Minute
	operationPersistTimeout = 15 * time.Second
)

type operationWorkerGate struct {
	accepting atomic.Bool
}

func newOperationWorkerGate() *operationWorkerGate {
	gate := &operationWorkerGate{}
	gate.accepting.Store(true)
	return gate
}

func (g *operationWorkerGate) beginDrain() {
	if g != nil {
		g.accepting.Store(false)
	}
}

func (g *operationWorkerGate) acceptingWork() bool {
	return g == nil || g.accepting.Load()
}

func (s *operationService) SetDraining(draining bool) {
	if s.workerGate == nil {
		return
	}
	if draining {
		s.workerGate.beginDrain()
		return
	}
	s.workerGate.accepting.Store(true)
}

func (s *operationService) ensureOperationWorkersAccepting() error {
	if s.workerGate != nil && !s.workerGate.acceptingWork() {
		return apperr.Unavailable("operation.draining", "operation workers are draining")
	}
	return nil
}

func operationWorkerRole(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return "operation-worker"
	}
	if len(role) > 96 {
		return role[:96]
	}
	return role
}

// operationLeaseOwner is an opaque per-attempt fencing token. The role is only
// a diagnostic prefix; the UUID is the authority. A new token is generated for
// every claim, so a reclaimed lease can never be completed by the old attempt.
func operationLeaseOwner(role string) string {
	return operationWorkerRole(role) + "/" + idgen.NewID().String()
}

// operationRemoteContext is independent from the persistence context. A
// shutdown cancels source work, while the caller still gets a short fresh
// context in which to record the resulting unknown outcome.
func operationRemoteContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, operationRemoteTimeout)
}

func operationPersistenceContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent != nil && parent.Err() == nil {
		return context.WithTimeout(parent, operationPersistTimeout)
	}
	return context.WithTimeout(context.Background(), operationPersistTimeout)
}
