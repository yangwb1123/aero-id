package service

import (
	"time"

	"github.com/aero/aero-id/internal/pkg/metrics"
)

// ControlPlaneMetrics is the narrow observability seam services consume (P6
// release gates). Implemented by metrics.Registry via a small adapter; services
// depend on the interface so unit tests can use a no-op.
type ControlPlaneMetrics interface {
	ObserveEventApplied(dataset string)
	ObserveEventQuarantined(dataset string)
	ObserveAuditIngestFailure(source string)
	ObserveAuditScopeMissing(source string)
	// ObserveAuditStandaloneRecorded counts every completed out-of-tx audit
	// Record execution (append success and ErrAuditDuplicate fold), keyed by
	// the action vocabulary — the standalone/fallback path observability proof
	// (sweep-and-lock REQ-3). In-tx AppendInTx paths never touch Record, so the
	// counter counts exactly the out-of-tx surface.
	ObserveAuditStandaloneRecorded(action string)
	ObserveAuditPublicationFinishFailure(status string, err error)
	ObserveAuditPublicationOutcome(status string)
	ObserveAuditPublicationDrainSkipped()
	ObserveAuditPublicationClaimFailed(phase string, err error)
	ObserveAuditPublicationPurged(kind string, count int64)
	ObserveAuditPublicationPurgeFailed(kind string, err error)
	ObserveOperationOutcome(status string)
	ObserveCallbackOutcome(status string)
	ObserveReconcileDrift(source string)
	ObserveProjectionLag(source, dataset string, lag time.Duration)
	ObserveConsumerLag(source string, lag time.Duration)
	ObserveOperationAge(status string, age time.Duration)
}

// noopControlPlaneMetrics is the default when no registry is wired.
type noopControlPlaneMetrics struct{}

func (noopControlPlaneMetrics) ObserveEventApplied(string)                         {}
func (noopControlPlaneMetrics) ObserveEventQuarantined(string)                     {}
func (noopControlPlaneMetrics) ObserveAuditIngestFailure(string)                   {}
func (noopControlPlaneMetrics) ObserveAuditScopeMissing(string)                    {}
func (noopControlPlaneMetrics) ObserveAuditStandaloneRecorded(string)              {}
func (noopControlPlaneMetrics) ObserveAuditPublicationFinishFailure(string, error) {}
func (noopControlPlaneMetrics) ObserveAuditPublicationOutcome(string)              {}
func (noopControlPlaneMetrics) ObserveAuditPublicationDrainSkipped()               {}
func (noopControlPlaneMetrics) ObserveAuditPublicationClaimFailed(string, error)   {}
func (noopControlPlaneMetrics) ObserveAuditPublicationPurged(string, int64)        {}
func (noopControlPlaneMetrics) ObserveAuditPublicationPurgeFailed(string, error)   {}
func (noopControlPlaneMetrics) ObserveOperationOutcome(string)                     {}
func (noopControlPlaneMetrics) ObserveCallbackOutcome(string)                      {}
func (noopControlPlaneMetrics) ObserveReconcileDrift(string)                       {}
func (noopControlPlaneMetrics) ObserveProjectionLag(string, string, time.Duration) {}
func (noopControlPlaneMetrics) ObserveConsumerLag(string, time.Duration)           {}
func (noopControlPlaneMetrics) ObserveOperationAge(string, time.Duration)          {}

// RegistryControlPlaneMetrics adapts the metrics.Registry to the service seam.
type RegistryControlPlaneMetrics struct {
	registry *metrics.Registry
}

func NewRegistryControlPlaneMetrics(registry *metrics.Registry) ControlPlaneMetrics {
	if registry == nil {
		return noopControlPlaneMetrics{}
	}
	return &RegistryControlPlaneMetrics{registry: registry}
}

func (r *RegistryControlPlaneMetrics) ObserveEventApplied(dataset string) {
	r.registry.ObserveEventApplied(dataset)
}
func (r *RegistryControlPlaneMetrics) ObserveEventQuarantined(dataset string) {
	r.registry.ObserveEventQuarantined(dataset)
}
func (r *RegistryControlPlaneMetrics) ObserveAuditIngestFailure(source string) {
	r.registry.ObserveAuditIngestFailure(source)
}
func (r *RegistryControlPlaneMetrics) ObserveAuditScopeMissing(source string) {
	r.registry.ObserveAuditScopeMissing(source)
}
func (r *RegistryControlPlaneMetrics) ObserveAuditStandaloneRecorded(action string) {
	r.registry.ObserveAuditStandaloneRecorded(action)
}
func (r *RegistryControlPlaneMetrics) ObserveAuditPublicationFinishFailure(status string, err error) {
	r.registry.ObserveAuditPublicationFinishFailure(status, err)
}
func (r *RegistryControlPlaneMetrics) ObserveAuditPublicationOutcome(status string) {
	r.registry.ObserveAuditPublicationOutcome(status)
}
func (r *RegistryControlPlaneMetrics) ObserveAuditPublicationDrainSkipped() {
	r.registry.ObserveAuditPublicationDrainSkipped()
}
func (r *RegistryControlPlaneMetrics) ObserveAuditPublicationClaimFailed(phase string, err error) {
	r.registry.ObserveAuditPublicationClaimFailed(phase, err)
}
func (r *RegistryControlPlaneMetrics) ObserveAuditPublicationPurged(kind string, count int64) {
	r.registry.ObserveAuditPublicationPurged(kind, count)
}
func (r *RegistryControlPlaneMetrics) ObserveAuditPublicationPurgeFailed(kind string, err error) {
	r.registry.ObserveAuditPublicationPurgeFailed(kind, err)
}
func (r *RegistryControlPlaneMetrics) ObserveOperationOutcome(status string) {
	r.registry.ObserveOperationOutcome(status)
}
func (r *RegistryControlPlaneMetrics) ObserveCallbackOutcome(status string) {
	r.registry.ObserveCallbackOutcome(status)
}
func (r *RegistryControlPlaneMetrics) ObserveReconcileDrift(source string) {
	r.registry.ObserveReconcileDrift(source)
}
func (r *RegistryControlPlaneMetrics) ObserveProjectionLag(source, dataset string, lag time.Duration) {
	r.registry.ObserveProjectionLag(source, dataset, lag)
}
func (r *RegistryControlPlaneMetrics) ObserveConsumerLag(source string, lag time.Duration) {
	r.registry.ObserveConsumerLag(source, lag)
}
func (r *RegistryControlPlaneMetrics) ObserveOperationAge(status string, age time.Duration) {
	r.registry.ObserveOperationAge(status, age)
}

// ControlPlaneMetricsSetter is implemented by services whose observability
// sink is attached after construction (composition-root friendly).
type ControlPlaneMetricsSetter interface {
	SetControlPlaneMetrics(ControlPlaneMetrics)
}
