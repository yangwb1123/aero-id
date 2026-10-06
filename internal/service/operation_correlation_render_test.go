package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

// correlationRenderOperationID is a UUID whose canonical rendering contains hex
// letters, so upper-case, mixed-case, urn:uuid:, 32-hex and braced renderings
// are all observably different strings for the same 128-bit identity.
func correlationRenderOperationID() domain.ID { return domain.ID{0xab, 0xcd} }

func correlationRenderOperation() *domain.Operation {
	return &domain.Operation{ID: correlationRenderOperationID(), AccountID: domain.ID{1}, TenantID: "tenant-1",
		ActorUID: "actor-1", RequestID: "request-1"}
}

// correlationRenderFake answers the erase and status seams from fixed results
// so a test can choose the exact source rendering it wants to pin.
type correlationRenderFake struct {
	eraseCalls   int
	statusCalls  int
	lastErase    connector.EraseRequest
	lastStatus   connector.OperationStatusRequest
	eraseResult  *connector.CommandResult
	statusResult *connector.CommandResult
	eraseErr     error
}

func (f *correlationRenderFake) Source() connector.Source { return connector.SourceSnaplink }

func (f *correlationRenderFake) Summary(context.Context, connector.SummaryRequest) (*connector.Summary, error) {
	return nil, errors.New("summary not used by operation correlation tests")
}

func (f *correlationRenderFake) Health(context.Context) error { return nil }

func (f *correlationRenderFake) Erase(_ context.Context, req connector.EraseRequest) (*connector.CommandResult, error) {
	f.eraseCalls++
	f.lastErase = req
	return f.eraseResult, f.eraseErr
}

func (f *correlationRenderFake) OperationStatus(_ context.Context,
	req connector.OperationStatusRequest) (*connector.CommandResult, error) {
	f.statusCalls++
	f.lastStatus = req
	return f.statusResult, nil
}

var (
	_ connector.CommandConnector         = (*correlationRenderFake)(nil)
	_ connector.OperationStatusConnector = (*correlationRenderFake)(nil)
)

func correlationRenderRunner(fake connector.Connector) OperationRunner {
	return NewConnectorOperationRunner(&operationStatusAccountRepository{account: testOperationAccount()}, nil, fake)
}

// correlationRenderings lists the legal alternate renderings of the operation
// id the runner sends, including the canonical form aero-id actually transmits.
// The fixture id contains hex letters, so every rendering below is an
// observably different string for the same 128-bit identity.
func correlationRenderings() []struct{ name, text string } {
	canonical := correlationRenderOperationID().String()
	plain := strings.ReplaceAll(canonical, "-", "")
	return []struct{ name, text string }{
		{name: "canonical", text: canonical},
		{name: "upper-case", text: strings.ToUpper(canonical)},
		{name: "mixed-case", text: strings.ToUpper(canonical[:1]) + strings.ToLower(canonical[1:])},
		{name: "urn prefix", text: "urn:uuid:" + canonical},
		{name: "urn prefix upper-case", text: "URN:UUID:" + strings.ToUpper(canonical)},
		{name: "32 hex", text: plain},
		{name: "32 hex upper-case", text: strings.ToUpper(plain)},
		{name: "braced", text: "{" + canonical + "}"},
		{name: "whitespace wrapped", text: " " + canonical + "\n"},
	}
}

// TestSnaplinkCommandResultAcceptsAlternateCorrelationRenderings is AC-2: a
// Snaplink response that echoes the operation id in any legal rendering settles
// the step with the source status and keeps source_operation_id in the durable
// outcome, instead of a temporary unknown outcome that burns an attempt and
// re-issues the remote command.
func TestSnaplinkCommandResultAcceptsAlternateCorrelationRenderings(t *testing.T) {
	for _, rendering := range correlationRenderings() {
		t.Run(rendering.name, func(t *testing.T) {
			operation := correlationRenderOperation()
			fake := &correlationRenderFake{eraseResult: &connector.CommandResult{
				OperationID: rendering.text, SourceOperationID: "snaplink-op-1", Status: "completed",
			}}
			sourceOperationID, outcome, err := correlationRenderRunner(fake).Run(t.Context(), operation, testSnaplinkEraseStep())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if sourceOperationID != "snaplink-op-1" || outcome["source_operation_id"] != "snaplink-op-1" ||
				outcome["status"] != "completed" {
				t.Fatalf("source id/outcome = %q/%#v, want snaplink-op-1/completed with a correlated source id",
					sourceOperationID, outcome)
			}
			if fake.eraseCalls != 1 {
				t.Fatalf("erase calls = %d, want one remote command", fake.eraseCalls)
			}
			if fake.lastErase.OperationID != operation.ID.String() {
				t.Fatalf("sent operation id = %q, want the unchanged canonical %q",
					fake.lastErase.OperationID, operation.ID.String())
			}
		})
	}
}

// TestSnaplinkCommandResultRejectsNonIdentityCorrelationIDs is the AC-3
// negative set for Snaplink: an echo that is not the same identity stays an
// unknown, non-terminal outcome, and no untrusted source id is adopted.
func TestSnaplinkCommandResultRejectsNonIdentityCorrelationIDs(t *testing.T) {
	canonical := correlationRenderOperationID().String()
	for _, tc := range []struct{ name, echo string }{
		{name: "different UUID", echo: "ffff0000-0000-0000-0000-000000000000"},
		{name: "different UUID in urn form", echo: "urn:uuid:ffff0000-0000-0000-0000-000000000000"},
		{name: "truncated 29 hex form", echo: strings.ReplaceAll(canonical, "-", "")[:29]},
		{name: "last nibble flip", echo: canonical[:len(canonical)-1] + "1"},
		{name: "unparseable echo", echo: "logical-1"},
		{name: "zero UUID text", echo: "00000000-0000-0000-0000-000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := testSnaplinkEraseStep()
			step.SourceOperationID = ""
			fake := &correlationRenderFake{eraseResult: &connector.CommandResult{
				OperationID: tc.echo, SourceOperationID: "snaplink-op-foreign", Status: "completed",
			}}
			sourceOperationID, _, err := correlationRenderRunner(fake).Run(t.Context(),
				correlationRenderOperation(), step)
			assertSnaplinkCorrelationMismatch(t, err, tc.echo)
			if status, code, _ := operationRunStatus(err); status != domain.StepUnknown || code != "operation.unknown_outcome" {
				t.Fatalf("run status = %s/%s, want unknown/operation.unknown_outcome", status, code)
			}
			if sourceOperationID != step.SourceOperationID {
				t.Fatalf("source id = %q, want the step's own %q", sourceOperationID, step.SourceOperationID)
			}
		})
	}
}

// TestSnaplinkCommandResultRejectsEmptyCorrelationEcho is the AC-3 empty-echo
// case for the Snaplink sites, where an empty echo is a contradiction (the
// source correlated nothing) rather than an omission.
func TestSnaplinkCommandResultRejectsEmptyCorrelationEcho(t *testing.T) {
	fake := &correlationRenderFake{eraseResult: &connector.CommandResult{
		OperationID: "   ", SourceOperationID: "snaplink-op-1", Status: "completed",
	}}
	sourceOperationID, _, err := correlationRenderRunner(fake).Run(t.Context(),
		correlationRenderOperation(), testSnaplinkEraseStep())
	assertSnaplinkCorrelationMismatch(t, err, "snaplink-op-1")
	if status, code, _ := operationRunStatus(err); status != domain.StepUnknown || code != "operation.unknown_outcome" {
		t.Fatalf("run status = %s/%s, want unknown/operation.unknown_outcome", status, code)
	}
	if sourceOperationID != "" {
		t.Fatalf("source id = %q, want empty for a step with no recorded source id", sourceOperationID)
	}
}

// TestSnaplinkCommandResultKeepsStepSourceIDOnForeignCorrelation proves that
// accepting alternate renderings of operation_id did not loosen provenance: a
// response correlated to a different operation never redirects reconciliation
// away from the source id the step already recorded.
func TestSnaplinkCommandResultKeepsStepSourceIDOnForeignCorrelation(t *testing.T) {
	step := testSnaplinkEraseStep()
	step.SourceOperationID = "snaplink-op-prev"
	fake := &correlationRenderFake{eraseResult: &connector.CommandResult{
		OperationID: "ffff0000-0000-0000-0000-000000000000", SourceOperationID: "snaplink-op-foreign",
		Status: "completed",
	}}
	sourceOperationID, outcome, err := correlationRenderRunner(fake).Run(t.Context(),
		correlationRenderOperation(), step)
	assertSnaplinkCorrelationMismatch(t, err, "snaplink-op-foreign")
	if sourceOperationID != "snaplink-op-prev" || outcome["source_operation_id"] != "snaplink-op-prev" {
		t.Fatalf("source id/outcome = %q/%#v, want the step's own snaplink-op-prev", sourceOperationID, outcome)
	}
}

// TestSnaplinkReconcileAcceptsAlternateCorrelationRendering covers the
// reconcile path: a Snaplink status response that re-renders the operation id
// settles the step instead of looping as unknown.
func TestSnaplinkReconcileAcceptsAlternateCorrelationRendering(t *testing.T) {
	operation := correlationRenderOperation()
	step := testSnaplinkEraseStep()
	step.SourceOperationID = "snaplink-op-1"
	fake := &correlationRenderFake{statusResult: &connector.CommandResult{
		OperationID: "urn:uuid:" + operation.ID.String(), SourceOperationID: "snaplink-op-1", Status: "completed",
	}}
	status, sourceOperationID, result, err := correlationRenderRunner(fake).(OperationOutcomeReconciler).
		Reconcile(t.Context(), operation, step)
	if err != nil || status != domain.StepSucceeded || sourceOperationID != "snaplink-op-1" || result == nil {
		t.Fatalf("Reconcile = %s/%q/%#v/%v, want succeeded/snaplink-op-1/result/nil", status, sourceOperationID, result, err)
	}
	if fake.statusCalls != 1 {
		t.Fatalf("status calls = %d, want one status lookup", fake.statusCalls)
	}
	if fake.lastStatus.OperationID != operation.ID.String() {
		t.Fatalf("queried operation id = %q, want the canonical %q", fake.lastStatus.OperationID, operation.ID.String())
	}
}

// TestTrustedSourceOperationIDKeepsSourceOwnedComparisonExact is AC-4 at the
// service boundary: the aero-id-owned correlation id is compared by identity,
// while the source-owned source_operation_id stays exact-string, including when
// both sides are UUID-shaped and denote the same identity.
func TestTrustedSourceOperationIDKeepsSourceOwnedComparisonExact(t *testing.T) {
	canonical := correlationRenderOperationID().String()
	const sourceID = "snaplink-op-1"
	for _, tc := range []struct {
		name      string
		operation *domain.Operation
		step      *domain.OperationStep
		source    connector.Source
		result    *connector.CommandResult
		want      string
	}{
		{name: "correlated identity", operation: correlationRenderOperation(), source: connector.SourceSnaplink,
			result: &connector.CommandResult{OperationID: canonical, SourceOperationID: sourceID}, want: sourceID},
		{name: "alternate rendering of the same identity", operation: correlationRenderOperation(),
			source: connector.SourceSnaplink,
			result: &connector.CommandResult{OperationID: "urn:uuid:" + canonical, SourceOperationID: sourceID},
			want:   sourceID},
		{name: "non-UUID source id case difference", operation: correlationRenderOperation(),
			step: &domain.OperationStep{SourceOperationID: sourceID}, source: connector.SourceSnaplink,
			result: &connector.CommandResult{OperationID: canonical, SourceOperationID: "Snaplink-Op-1"}, want: sourceID},
		{name: "UUID-shaped source id case difference", operation: correlationRenderOperation(),
			step:   &domain.OperationStep{SourceOperationID: "abcd0000-0000-0000-0000-000000000001"},
			source: connector.SourceSnaplink,
			result: &connector.CommandResult{OperationID: canonical,
				SourceOperationID: "ABCD0000-0000-0000-0000-000000000001"},
			want: "abcd0000-0000-0000-0000-000000000001"},
		{name: "UUID-shaped source id alternate rendering of same identity", operation: correlationRenderOperation(),
			step:   &domain.OperationStep{SourceOperationID: "abcd0000-0000-0000-0000-000000000001"},
			source: connector.SourceSnaplink,
			result: &connector.CommandResult{OperationID: canonical,
				SourceOperationID: "abcd000000000000000000000000000001"},
			want: "abcd0000-0000-0000-0000-000000000001"},
		{name: "foreign correlation keeps step source id", operation: correlationRenderOperation(),
			step: &domain.OperationStep{SourceOperationID: "snaplink-op-prev"}, source: connector.SourceSnaplink,
			result: &connector.CommandResult{OperationID: "ffff0000-0000-0000-0000-000000000000",
				SourceOperationID: "snaplink-op-foreign"},
			want: "snaplink-op-prev"},
		{name: "nil operation keeps step source id", step: &domain.OperationStep{SourceOperationID: sourceID},
			source: connector.SourceSnaplink,
			result: &connector.CommandResult{OperationID: canonical, SourceOperationID: sourceID}, want: sourceID},
		{name: "nil operation and step", source: connector.SourceSnaplink,
			result: &connector.CommandResult{OperationID: canonical, SourceOperationID: sourceID}, want: ""},
		{name: "nil result", operation: correlationRenderOperation(), source: connector.SourceSnaplink, want: ""},
		{name: "non-Snaplink source is returned verbatim", operation: correlationRenderOperation(),
			source: connector.SourceAeroIM,
			result: &connector.CommandResult{OperationID: "different-logical", SourceOperationID: "im-op-1"},
			want:   "im-op-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := trustedSourceOperationID(tc.source, tc.operation, tc.step, tc.result); got != tc.want {
				t.Fatalf("trustedSourceOperationID = %q, want %q", got, tc.want)
			}
		})
	}
}

func assertSnaplinkCorrelationMismatch(t *testing.T, err error, secrets ...string) {
	t.Helper()
	var remote *connector.RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("error = %v, want RemoteError", err)
	}
	if _, ok := connector.IsRemote(err); !ok || remote.Source != connector.SourceSnaplink ||
		remote.Kind != connector.ErrorUnknown || remote.Code != "source.operation_id_mismatch" || !remote.Temporary {
		t.Fatalf("remote = %+v, want Snaplink temporary unknown operation_id_mismatch", remote)
	}
	text := fmt.Sprintf("%v %+v", err, remote)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("correlation mismatch error leaked %q: %q", secret, text)
		}
	}
}
