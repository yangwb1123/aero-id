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
	ObserveOperationOutcome(status string)
	ObserveCallbackOutcome(status string)
	ObserveReconcileDrift(source string)
	ObserveProjectionLag(source, dataset string, lag time.Duration)
	ObserveConsumerLag(source string, lag time.Duration)
	ObserveOperationAge(status string, age time.Duration)
}

// noopControlPlaneMetrics is the default when no registry is wired.
type noopControlPlaneMetrics struct{}

func (noopControlPlaneMetrics) ObserveEventApplied(string)                    {}
func (noopControlPlaneMetrics) ObserveEventQuarantined(string)                {}
func (noopControlPlaneMetrics) ObserveAuditIngestFailure(string)              {}
func (noopControlPlaneMetrics) ObserveOperationOutcome(string)                {}
func (noopControlPlaneMetrics) ObserveCallbackOutcome(string)                 {}
func (noopControlPlaneMetrics) ObserveReconcileDrift(string)                  {}
func (noopControlPlaneMetrics) ObserveProjectionLag(string, string, time.Duration) {}
func (noopControlPlaneMetrics) ObserveConsumerLag(string, time.Duration)      {}
func (noopControlPlaneMetrics) ObserveOperationAge(string, time.Duration)     {}

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
