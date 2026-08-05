// Package connector contains anti-corruption ports and source-facing clients.
// Connectors may call published gRPC or HTTP APIs, but never source databases.
package connector

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/domain"
)

type Source string

const (
	SourceSnaplink  Source = "snaplink"
	SourceAeroIM    Source = "aero-im"
	SourceAeroVault Source = "aero-vault"
)

type SummaryRequest struct {
	AccountID    domain.ID
	CanonicalUID string
	ActorUID     string
	TenantID     string
	RequestID    string
	OperationID  string
	Datasets     []domain.AccountDataset
	Region       string
	Reconcile    bool
}

type Summary struct {
	Source       Source
	SourceRegion string
	Version      int64
	Snapshots    []*domain.AccountSnapshot
	Sources      []*domain.AccountSource
	Memberships  []*domain.AccountMembership
	Complete     bool
}

type Connector interface {
	Source() Source
	Summary(context.Context, SummaryRequest) (*Summary, error)
	Health(context.Context) error
}

// CommandConnector is the optional source command seam. A source implements
// it only when it publishes an authenticated, idempotent command API. The
// aggregation layer never treats an absent command seam as successful.
type CommandConnector interface {
	Connector
	Erase(context.Context, EraseRequest) (*CommandResult, error)
}

// OperationStatusConnector is an optional source seam used to reconcile a
// command whose response was lost after the source may already have applied
// it. A connector that does not publish an outcome endpoint leaves the step in
// unknown state; the operation worker must never guess that a timeout failed.
type OperationStatusConnector interface {
	Connector
	OperationStatus(context.Context, OperationStatusRequest) (*CommandResult, error)
}

type EraseRequest struct {
	AccountID      domain.ID
	CanonicalUID   string
	ActorUID       string
	TenantID       string
	RequestID      string
	Datasets       []domain.AccountDataset
	IdempotencyKey string
	OperationID    string
}

type OperationStatusRequest struct {
	AccountID         domain.ID
	CanonicalUID      string
	ActorUID          string
	TenantID          string
	RequestID         string
	OperationID       string
	SourceOperationID string
	IdempotencyKey    string
}

type CommandResult struct {
	SourceOperationID string
	Status            string
	Result            map[string]any
}

type ErrorKind string

const (
	ErrorUnavailable  ErrorKind = "unavailable"
	ErrorTimeout      ErrorKind = "timeout"
	ErrorUnauthorized ErrorKind = "unauthorized"
	ErrorForbidden    ErrorKind = "forbidden"
	ErrorInvalid      ErrorKind = "invalid"
	ErrorUnknown      ErrorKind = "unknown"
)

type RemoteError struct {
	Source    Source
	Kind      ErrorKind
	Code      string
	Temporary bool
	Cause     error
}

func (e *RemoteError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return string(e.Source) + ": " + e.Code
	}
	return string(e.Source) + ": remote call failed"
}

func (e *RemoteError) Unwrap() error { return e.Cause }

func IsRemote(err error) (*RemoteError, bool) {
	var result *RemoteError
	return result, errors.As(err, &result)
}
