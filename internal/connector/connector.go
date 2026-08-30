// Package connector contains anti-corruption ports and source-facing clients.
// Connectors may call published gRPC or HTTP APIs, but never source databases.
package connector

import (
	"context"
	"errors"
	"reflect"
	"sort"

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

	// trustedTarget is populated only by NewTrustedSummaryRequest after the
	// constructor has loaded the account through an authoritative loader. Aero
	// IM refuses to sign or send a summary request without it; other source
	// connectors retain their existing request shape.
	trustedTarget *trustedSummaryTarget
}

type trustedSummaryTarget struct {
	source       Source
	accountID    string
	canonicalUID string
	tenantID     string
	region       string
	datasets     []domain.AccountDataset
}

// AuthoritativeAccountLoader is the minimum repository seam required to load
// an account by its tenant-scoped canonical ID. Keeping the interface here
// avoids importing the repository layer into the connector package while still
// preventing callers from presenting a caller-mutated Account as authority.
type AuthoritativeAccountLoader interface {
	GetByID(context.Context, string, domain.ID) (*domain.Account, error)
}

// TrustedSummaryRequestInput contains transport context plus the authoritative
// lookup key. Target identity fields are intentionally absent: they must come
// from the loader, never from caller-supplied request data.
type TrustedSummaryRequestInput struct {
	Source      Source
	TenantID    string
	AccountID   domain.ID
	ActorUID    string
	RequestID   string
	OperationID string
	Datasets    []domain.AccountDataset
	Reconcile   bool
}

// NewTrustedSummaryRequest loads the account by (tenant ID, account ID) and
// copies its canonical UID and home region before constructing the request.
// Only this constructor produces the private target marker used by the Aero IM
// signer. A caller can mutate its own Account value, but it cannot make that
// value authoritative without going through the loader.
func NewTrustedSummaryRequest(ctx context.Context, loader AuthoritativeAccountLoader,
	input TrustedSummaryRequestInput) (SummaryRequest, error) {
	input.Source = normalizeTrustedSummarySource(input.Source)
	if !validTrustedSummaryRequestInput(ctx, loader, input) {
		return SummaryRequest{}, errors.New("trusted summary account binding is invalid")
	}
	// Validate the requested source and scope before loading or contacting a
	// source. Stored scopes are explicit; an empty scope is never interpreted as
	// "all" by this authority-bound constructor.
	datasets, err := trustedSummaryDatasets(input.Source, input.Datasets)
	if err != nil {
		return SummaryRequest{}, err
	}
	account, err := loadTrustedSummaryAccount(ctx, loader, input)
	if err != nil {
		return SummaryRequest{}, err
	}
	sort.Slice(datasets, func(left, right int) bool { return string(datasets[left]) < string(datasets[right]) })
	return SummaryRequest{
		AccountID:    account.ID,
		CanonicalUID: account.CanonicalUID,
		ActorUID:     input.ActorUID,
		TenantID:     account.TenantID,
		RequestID:    input.RequestID,
		OperationID:  input.OperationID,
		Datasets:     datasets,
		Region:       account.HomeRegion,
		Reconcile:    input.Reconcile,
		trustedTarget: &trustedSummaryTarget{
			source:       input.Source,
			accountID:    account.ID.String(),
			canonicalUID: account.CanonicalUID,
			tenantID:     account.TenantID,
			region:       account.HomeRegion,
			datasets:     append([]domain.AccountDataset(nil), datasets...),
		},
	}, nil
}

func normalizeTrustedSummarySource(source Source) Source {
	if source == "" {
		return SourceAeroIM
	}
	return source
}

func validTrustedSummarySource(source Source) bool {
	switch source {
	case SourceSnaplink, SourceAeroIM, SourceAeroVault:
		return true
	default:
		return false
	}
}

func validTrustedSummaryRequestInput(ctx context.Context, loader AuthoritativeAccountLoader,
	input TrustedSummaryRequestInput) bool {
	return ctx != nil && validAuthoritativeLoader(loader) && validTrustedSummarySource(input.Source) &&
		input.AccountID != (domain.ID{}) && validTargetAssertionComponent(input.TenantID, 512)
}

func validAuthoritativeLoader(loader AuthoritativeAccountLoader) bool {
	if loader == nil {
		return false
	}
	value := reflect.ValueOf(loader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func loadTrustedSummaryAccount(ctx context.Context, loader AuthoritativeAccountLoader,
	input TrustedSummaryRequestInput) (*domain.Account, error) {
	account, err := loader.GetByID(ctx, input.TenantID, input.AccountID)
	if err != nil || !validTrustedSummaryAccount(account, input) {
		return nil, errors.New("trusted summary account binding is unavailable")
	}
	return account, nil
}

func validTrustedSummaryAccount(account *domain.Account, input TrustedSummaryRequestInput) bool {
	if account == nil {
		return false
	}
	return account.ID == input.AccountID && account.TenantID == input.TenantID &&
		account.Status == domain.AccountActive && account.Valid() &&
		validTargetAssertionComponent(account.CanonicalUID, 512) &&
		validTargetAssertionComponent(account.HomeRegion, 128)
}

func trustedSummaryDatasets(source Source, input []domain.AccountDataset) ([]domain.AccountDataset, error) {
	datasets, err := ResolveDatasetScope(source, input, DatasetScopeStored)
	if err != nil || len(datasets) == 0 {
		return nil, errors.New("trusted summary dataset scope is invalid")
	}
	return datasets, nil
}

type Summary struct {
	Source Source
	// SourceRegion is the authority region for Version. Version values must be
	// globally monotonic for an account/source/coverage stream; a region move
	// cannot restart the counter.
	SourceRegion string
	Version      int64
	Coverage     domain.SummaryCoverage
	Snapshots    []*domain.AccountSnapshot
	Sources      []*domain.AccountSource
	Memberships  []*domain.AccountMembership
	Complete     bool
	// SkippedDatasets lists dataset names received in a source payload that were
	// not projected, because they are not in the account dataset registry or are
	// registered to a different source. A non-empty list means the source summary
	// is incomplete from aero-id's perspective regardless of the source's own
	// "complete" flag.
	SkippedDatasets []string
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
	Region         string
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
	Region            string
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
