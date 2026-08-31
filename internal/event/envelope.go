// Package event defines the transport-neutral canonical event contract.
package event

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// CurrentSchemaVersion is the only canonical envelope version understood by
// this binary. Future versions must be explicitly supported before they can
// reach a projector; the NATS adapter quarantines unsupported messages.
const CurrentSchemaVersion = 1

type Envelope struct {
	EventID          domain.ID      `json:"event_id"`
	Source           string         `json:"source"`
	SourceRegion     string         `json:"source_region"`
	EventType        string         `json:"event_type"`
	SchemaVersion    int            `json:"schema_version"`
	AggregateType    string         `json:"aggregate_type"`
	AggregateID      string         `json:"aggregate_id"`
	AggregateVersion int64          `json:"aggregate_version"`
	ActorUID         string         `json:"actor_uid,omitempty"`
	TenantID         string         `json:"tenant_id,omitempty"`
	OperationID      domain.ID      `json:"operation_id"`
	CorrelationID    domain.ID      `json:"correlation_id"`
	CausationID      domain.ID      `json:"causation_id"`
	TraceID          string         `json:"trace_id,omitempty"`
	OccurredAt       time.Time      `json:"occurred_at"`
	Payload          map[string]any `json:"payload"`
}

const (
	MaxEventBytes         = 256 * 1024
	MaxSourceBytes        = 64
	MaxRegionBytes        = 64
	MaxEventTypeBytes     = 128
	MaxAggregateTypeBytes = 128
	MaxAggregateIDBytes   = 256
	MaxActorUIDBytes      = 256
	MaxTenantIDBytes      = 256
	MaxTraceIDBytes       = 256
	MaxIDBytes            = 64
)

type SnapshotMutation struct {
	AccountID    domain.ID
	Dataset      domain.AccountDataset
	Source       string
	SourceRegion string
	Status       domain.SnapshotStatus
	Version      int64
	Data         map[string]any
}

func (e *Envelope) Validate() error {
	if e == nil {
		return fmt.Errorf("event is nil")
	}
	if e.EventID == (domain.ID{}) {
		return fmt.Errorf("event_id is required")
	}
	if err := e.validateBounds(); err != nil {
		return err
	}
	checks := []func() error{e.validateSource, e.validateType, e.validateAggregate, e.validatePayload}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (e *Envelope) validateSource() error {
	if strings.TrimSpace(e.Source) == "" || strings.TrimSpace(e.SourceRegion) == "" {
		return fmt.Errorf("source and source_region are required")
	}
	// aero-id is the control plane: it relays approved projections across
	// regions (P6-03) and reports its own control-plane facts.
	if e.Source != "snaplink" && e.Source != "aero-im" && e.Source != "aero-vault" && e.Source != "aero-id" {
		return fmt.Errorf("source is not allow-listed")
	}
	return nil
}

func (e *Envelope) validateType() error {
	if strings.TrimSpace(e.EventType) == "" || e.SchemaVersion <= 0 {
		return fmt.Errorf("event_type and schema_version are required")
	}
	if e.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("schema_version %d is unsupported", e.SchemaVersion)
	}
	return nil
}

func (e *Envelope) validateAggregate() error {
	if strings.TrimSpace(e.AggregateType) == "" || strings.TrimSpace(e.AggregateID) == "" {
		return fmt.Errorf("aggregate identity is required")
	}
	if e.AggregateVersion <= 0 || e.OccurredAt.IsZero() {
		return fmt.Errorf("aggregate_version or occurred_at is invalid")
	}
	return nil
}

func (e *Envelope) validatePayload() error {
	if e.Payload == nil {
		return fmt.Errorf("payload is required")
	}
	encoded, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("payload is not serializable")
	}
	if len(encoded) > MaxEventBytes {
		return fmt.Errorf("payload is too large")
	}
	return nil
}

func containsControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func (e *Envelope) validateBounds() error {
	fields := []struct {
		name  string
		value string
		max   int
	}{
		{"source", e.Source, MaxSourceBytes}, {"source_region", e.SourceRegion, MaxRegionBytes},
		{"event_type", e.EventType, MaxEventTypeBytes}, {"aggregate_type", e.AggregateType, MaxAggregateTypeBytes},
		{"aggregate_id", e.AggregateID, MaxAggregateIDBytes}, {"actor_uid", e.ActorUID, MaxActorUIDBytes},
		{"tenant_id", e.TenantID, MaxTenantIDBytes}, {"trace_id", e.TraceID, MaxTraceIDBytes},
	}
	for _, field := range fields {
		if len(field.value) > field.max {
			return fmt.Errorf("%s is too long", field.name)
		}
		if !utf8.ValidString(field.value) || containsControl(field.value) {
			return fmt.Errorf("%s is invalid", field.name)
		}
	}
	return nil
}

func (e *Envelope) Snapshot() (*SnapshotMutation, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	accountID, err := snapshotAccountID(e.Payload)
	if err != nil {
		return nil, err
	}
	dataset, err := snapshotDataset(e.Payload, e.Source)
	if err != nil {
		return nil, err
	}
	data, err := snapshotData(e.Payload)
	if err != nil {
		return nil, err
	}
	status, err := snapshotStatus(e.Payload)
	if err != nil {
		return nil, err
	}
	version := snapshotVersion(e.Payload, e.AggregateVersion)
	return &SnapshotMutation{
		AccountID: accountID, Dataset: dataset, Source: e.Source, SourceRegion: e.SourceRegion,
		Status: status, Version: version, Data: boundedProjectionData(redactProjectionData(data, 0)),
	}, nil
}

func snapshotAccountID(payload map[string]any) (domain.ID, error) {
	value, ok := payload["account_id"].(string)
	if !ok {
		return domain.ID{}, fmt.Errorf("payload.account_id is required")
	}
	accountID, err := idgen.Parse(value)
	if err != nil {
		return domain.ID{}, fmt.Errorf("payload.account_id is invalid")
	}
	return accountID, nil
}

func snapshotDataset(payload map[string]any, source string) (domain.AccountDataset, error) {
	value, ok := payload["dataset"].(string)
	if !ok {
		return "", fmt.Errorf("payload.dataset is required")
	}
	dataset := domain.AccountDataset(value)
	descriptor, ok := domain.DatasetDescriptorOf(dataset)
	// The control plane (aero-id) relays approved projections of every source
	// during cross-region replication (P6-03); all other sources may only write
	// their own datasets.
	if !ok || (descriptor.Source != source && source != "aero-id") {
		return "", fmt.Errorf("payload.dataset is not valid for source")
	}
	return dataset, nil
}

func snapshotData(payload map[string]any) (map[string]any, error) {
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload.data is required")
	}
	return data, nil
}

func snapshotStatus(payload map[string]any) (domain.SnapshotStatus, error) {
	status := domain.SnapshotFresh
	if value, ok := payload["status"].(string); ok && value != "" {
		status = domain.SnapshotStatus(value)
	}
	if !status.Valid() {
		return "", fmt.Errorf("payload.status is invalid")
	}
	return status, nil
}

func snapshotVersion(payload map[string]any, fallback int64) int64 {
	if value, ok := payload["version"].(float64); ok && value > 0 {
		return int64(value)
	}
	return fallback
}
