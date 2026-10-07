package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

const (
	// syncCorrelationLogicalID is the canonical rendering of the UUIDv7 aero-id
	// mints for a sync job (job.ID.String()) and transmits as the correlation
	// id of every erase/status request. It contains hex letters, so each
	// alternate rendering below is an observably different string for the same
	// 128-bit identity.
	syncCorrelationLogicalID = "0195b2c6-9f40-7c01-8000-000000000001"
	// syncCorrelationAltID is a different 128-bit identity in the same shape.
	syncCorrelationAltID = "0195b2c6-9f40-7c01-8000-000000000002"
	// syncCorrelation32Hex is syncCorrelationLogicalID without hyphens.
	syncCorrelation32Hex = "0195b2c69f407c018000000000000001"
	// syncCorrelationSourceID is the source-owned reconciliation target already
	// persisted for this job. It is opaque, source-minted, and must survive
	// every correlation-rendering change untouched.
	syncCorrelationSourceID = "vault-op-1"
)

// syncCorrelationJobID returns the domain job id whose canonical rendering is
// the correlation value the reconcile and execute paths transmit.
func syncCorrelationJobID(t *testing.T) domain.ID {
	t.Helper()
	id, err := idgen.Parse(syncCorrelationLogicalID)
	if err != nil {
		t.Fatalf("parse sync job id %q: %v", syncCorrelationLogicalID, err)
	}
	return domain.ID(id)
}

// syncCorrelationRenderings lists the legal renderings of the correlation id
// aero-id sends, including the canonical form it actually transmits.
func syncCorrelationRenderings() []struct{ name, text string } {
	return []struct{ name, text string }{
		{name: "canonical", text: syncCorrelationLogicalID},
		{name: "upper case", text: strings.ToUpper(syncCorrelationLogicalID)},
		{name: "mixed case", text: "0195B2c6-9F40-7c01-8000-000000000001"},
		{name: "urn prefix", text: "urn:uuid:" + syncCorrelationLogicalID},
		{name: "urn prefix upper case", text: "URN:UUID:" + strings.ToUpper(syncCorrelationLogicalID)},
		{name: "32 hex", text: syncCorrelation32Hex},
		{name: "32 hex upper case", text: strings.ToUpper(syncCorrelation32Hex)},
		{name: "braced", text: "{" + syncCorrelationLogicalID + "}"},
		{name: "space padded upper case", text: " " + strings.ToUpper(syncCorrelationLogicalID) + " "},
		{name: "tab padded", text: "\t" + syncCorrelationLogicalID + "\t"},
		{name: "newline padded", text: "\n" + syncCorrelationLogicalID + "\r\n"},
	}
}

// TestTrustedSyncEraseSourceOperationIDAcceptsAlternateCorrelationRenderings is
// AC-1: aero-id owns the correlation id and mints it as a UUID, so a source
// that parses it and re-renders it in any form idgen.Parse accepts correlated
// the same erase and must not be declared a mismatch. Such a response settles
// the step, and the durable source_operation_id stays the retained
// reconciliation target — the source's raw rendering of the echo must never
// become it.
func TestTrustedSyncEraseSourceOperationIDAcceptsAlternateCorrelationRenderings(t *testing.T) {
	for _, rendering := range syncCorrelationRenderings() {
		t.Run(rendering.name, func(t *testing.T) {
			got, correlated := trustedSyncEraseSourceOperationID(connector.SourceSnaplink,
				syncCorrelationLogicalID, syncCorrelationSourceID,
				&connector.CommandResult{OperationID: rendering.text, SourceOperationID: syncCorrelationSourceID})
			if !correlated || got != syncCorrelationSourceID {
				t.Fatalf("echo %q -> (%q, %v), want the correlated retained target (%q, true)",
					rendering.text, got, correlated, syncCorrelationSourceID)
			}
		})
	}
	t.Run("first returned source id becomes the target", func(t *testing.T) {
		got, correlated := trustedSyncEraseSourceOperationID(connector.SourceSnaplink,
			syncCorrelationLogicalID, "",
			&connector.CommandResult{OperationID: "urn:uuid:" + syncCorrelationLogicalID,
				SourceOperationID: syncCorrelationSourceID})
		if !correlated || got != syncCorrelationSourceID {
			t.Fatalf("no prior target -> (%q, %v), want (%q, true)", got, correlated, syncCorrelationSourceID)
		}
	})
}

// TestTrustedSyncEraseSourceOperationIDRejectsNonIdentityCorrelation is AC-2's
// negative correlation set: only an echo that is not the same 128-bit identity
// is a mismatch. Every case keeps the retained reconciliation target and never
// adopts the source's value.
func TestTrustedSyncEraseSourceOperationIDRejectsNonIdentityCorrelation(t *testing.T) {
	for _, tc := range []struct{ name, echo string }{
		{name: "different UUID", echo: syncCorrelationAltID},
		{name: "different UUID in urn form", echo: "urn:uuid:" + syncCorrelationAltID},
		{name: "last nibble flip", echo: "0195b2c6-9f40-7c01-8000-00000000000a"},
		{name: "truncated 29 hex form", echo: "0195b2c69f407c018000000000001"},
		{name: "first 8 truncation", echo: syncCorrelationLogicalID[:8]},
		{name: "zero UUID text", echo: "00000000-0000-0000-0000-000000000000"},
		{name: "unparseable echo", echo: "logical-2"},
		{name: "whitespace only echo", echo: "   \t\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, correlated := trustedSyncEraseSourceOperationID(connector.SourceSnaplink,
				syncCorrelationLogicalID, syncCorrelationSourceID,
				&connector.CommandResult{OperationID: tc.echo, SourceOperationID: syncCorrelationSourceID})
			if correlated || got != syncCorrelationSourceID {
				t.Fatalf("echo %q -> (%q, %v), want the retained target (%q, false)",
					tc.echo, got, correlated, syncCorrelationSourceID)
			}
		})
	}
}

// TestTrustedSyncEraseSourceOperationIDRejectsDivergentSourceOperationID pins
// the other half of the rule: the source-owned source_operation_id is the
// reconciliation target and stays exact-string, including when both values are
// UUID-shaped and denote the same identity. Identity comparison is scoped to
// the aero-id-owned correlation id only.
func TestTrustedSyncEraseSourceOperationIDRejectsDivergentSourceOperationID(t *testing.T) {
	for _, tc := range []struct{ name, existing, returned string }{
		{name: "different value", existing: syncCorrelationSourceID, returned: "vault-op-2"},
		{name: "non-UUID case difference", existing: syncCorrelationSourceID, returned: "VAULT-OP-1"},
		{name: "UUID-shaped case difference", existing: "abcd0000-0000-0000-0000-000000000001",
			returned: "ABCD0000-0000-0000-0000-000000000001"},
		{name: "UUID-shaped alternate rendering of the same identity",
			existing: "abcd0000-0000-0000-0000-000000000001", returned: "abcd000000000000000000000000000001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, correlated := trustedSyncEraseSourceOperationID(connector.SourceSnaplink,
				syncCorrelationLogicalID, tc.existing,
				&connector.CommandResult{OperationID: syncCorrelationLogicalID, SourceOperationID: tc.returned})
			if correlated || got != tc.existing {
				t.Fatalf("source id %q over retained %q -> (%q, %v), want the retained (%q, false)",
					tc.returned, tc.existing, got, correlated, tc.existing)
			}
		})
	}
}

// TestTrustedSyncEraseSourceOperationIDFallsBackToExactEqualityForOpaqueCorrelation
// is AC-3: a legacy opaque correlation id has no 128-bit identity, so it keeps
// byte-exact comparison — including for padded and case-differing echoes, which
// must stay false rather than being normalised into a match.
func TestTrustedSyncEraseSourceOperationIDFallsBackToExactEqualityForOpaqueCorrelation(t *testing.T) {
	const opaque = "logical-erase-1"
	for _, tc := range []struct {
		name, echo string
		want       bool
	}{
		{name: "identical", echo: opaque, want: true},
		{name: "case difference", echo: "LOGICAL-ERASE-1", want: false},
		{name: "leading space", echo: " logical-erase-1", want: false},
		{name: "trailing space", echo: "logical-erase-1 ", want: false},
		{name: "different value", echo: "logical-erase-2", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, correlated := trustedSyncEraseSourceOperationID(connector.SourceSnaplink, opaque,
				syncCorrelationSourceID,
				&connector.CommandResult{OperationID: tc.echo, SourceOperationID: syncCorrelationSourceID})
			if correlated != tc.want {
				t.Fatalf("opaque echo %q -> correlated %v, want %v", tc.echo, correlated, tc.want)
			}
			if got != syncCorrelationSourceID {
				t.Fatalf("opaque echo %q -> %q, want the retained target %q",
					tc.echo, got, syncCorrelationSourceID)
			}
		})
	}
}

// TestTrustedSyncEraseSourceOperationIDPreservesGuardBranches pins the
// pre-existing branches the correlation change must not erode: nil results, the
// Snaplink completeness rule that a durable erase carries a source-owned id,
// the pre-existing source-id trim, and the non-Snaplink source that returns no
// source id of its own.
func TestTrustedSyncEraseSourceOperationIDPreservesGuardBranches(t *testing.T) {
	t.Run("nil result keeps the retained target", func(t *testing.T) {
		got, correlated := trustedSyncEraseSourceOperationID(connector.SourceSnaplink,
			syncCorrelationLogicalID, " "+syncCorrelationSourceID+" ", nil)
		if !correlated || got != syncCorrelationSourceID {
			t.Fatalf("nil result -> (%q, %v), want the trimmed retained target (%q, true)",
				got, correlated, syncCorrelationSourceID)
		}
	})
	t.Run("snaplink without a returned source id stays uncorrelated", func(t *testing.T) {
		got, correlated := trustedSyncEraseSourceOperationID(connector.SourceSnaplink,
			syncCorrelationLogicalID, syncCorrelationSourceID,
			&connector.CommandResult{OperationID: syncCorrelationLogicalID})
		if correlated || got != syncCorrelationSourceID {
			t.Fatalf("missing source id -> (%q, %v), want the retained target (%q, false)",
				got, correlated, syncCorrelationSourceID)
		}
	})
	t.Run("snaplink whitespace-only correlation echo stays uncorrelated", func(t *testing.T) {
		got, correlated := trustedSyncEraseSourceOperationID(connector.SourceSnaplink,
			syncCorrelationLogicalID, syncCorrelationSourceID,
			&connector.CommandResult{OperationID: "   ", SourceOperationID: syncCorrelationSourceID})
		if correlated || got != syncCorrelationSourceID {
			t.Fatalf("whitespace-only echo -> (%q, %v), want the retained target (%q, false)",
				got, correlated, syncCorrelationSourceID)
		}
	})
	t.Run("non-snaplink source may return no source id", func(t *testing.T) {
		got, correlated := trustedSyncEraseSourceOperationID(connector.SourceAeroIM,
			syncCorrelationLogicalID, "",
			&connector.CommandResult{OperationID: syncCorrelationLogicalID})
		if !correlated || got != "" {
			t.Fatalf("aero-im without a source id -> (%q, %v), want (\"\", true)", got, correlated)
		}
	})
	t.Run("padded returned source id is trimmed before comparison", func(t *testing.T) {
		got, correlated := trustedSyncEraseSourceOperationID(connector.SourceSnaplink,
			syncCorrelationLogicalID, syncCorrelationSourceID,
			&connector.CommandResult{OperationID: syncCorrelationLogicalID,
				SourceOperationID: " " + syncCorrelationSourceID + "\t"})
		if !correlated || got != syncCorrelationSourceID {
			t.Fatalf("padded source id -> (%q, %v), want (%q, true)", got, correlated, syncCorrelationSourceID)
		}
	})
}

func syncCorrelationReconcileFixture(t *testing.T, result *connector.CommandResult) (
	*syncJobService, *correlationRenderFake, *domain.Account, *domain.SyncJob) {
	t.Helper()
	fake := &correlationRenderFake{statusResult: result}
	svc := &syncJobService{connectors: map[connector.Source]connector.Connector{
		connector.SourceSnaplink: fake,
	}}
	job := &domain.SyncJob{ID: syncCorrelationJobID(t), ActorUID: "actor-1", TenantID: "tenant-1",
		RequestID: "request-1", IdempotencyKey: "idem-1"}
	account := &domain.Account{ID: domain.ID{1}, CanonicalUID: "uid-1", HomeRegion: "region-1"}
	return svc, fake, account, job
}

// TestReconcileEraseSourceAcceptsAlternateCorrelationRenderingAndKeepsSourceID
// is AC-5 at the reconcile seam: a source that re-renders the correlation UUID
// converges the erase to completed and keeps the source-owned reconciliation
// target, instead of pinning the job in erase.unknown_outcome forever. The
// value actually transmitted is unchanged.
func TestReconcileEraseSourceAcceptsAlternateCorrelationRenderingAndKeepsSourceID(t *testing.T) {
	svc, fake, account, job := syncCorrelationReconcileFixture(t, &connector.CommandResult{
		OperationID:       " " + strings.ToUpper(syncCorrelationLogicalID) + " ",
		SourceOperationID: syncCorrelationSourceID,
		Status:            "completed",
	})
	status, operationID, _, err := svc.reconcileEraseSource(t.Context(), account, job,
		string(connector.SourceSnaplink), map[string]any{"source_operation_id": syncCorrelationSourceID})
	if err != nil || status != "completed" || operationID != syncCorrelationSourceID {
		t.Fatalf("reconcileEraseSource = %q/%q/%v, want completed/%s/nil",
			status, operationID, err, syncCorrelationSourceID)
	}
	if fake.statusCalls != 1 {
		t.Fatalf("status calls = %d, want one status lookup", fake.statusCalls)
	}
	if fake.lastStatus.OperationID != job.ID.String() {
		t.Fatalf("queried correlation id = %q, want the unchanged canonical %q",
			fake.lastStatus.OperationID, job.ID.String())
	}
	if fake.lastStatus.SourceOperationID != syncCorrelationSourceID {
		t.Fatalf("queried source id = %q, want the retained %q",
			fake.lastStatus.SourceOperationID, syncCorrelationSourceID)
	}
}

// TestReconcileEraseSourceRejectsForeignCorrelation is AC-5's negative twin: an
// echo that is not the same identity stays an unknown, temporary outcome that
// keeps the retained reconciliation target rather than adopting the remote one.
func TestReconcileEraseSourceRejectsForeignCorrelation(t *testing.T) {
	svc, fake, account, job := syncCorrelationReconcileFixture(t, &connector.CommandResult{
		OperationID:       "urn:uuid:" + syncCorrelationAltID,
		SourceOperationID: syncCorrelationSourceID,
		Status:            "completed",
	})
	status, operationID, result, err := svc.reconcileEraseSource(t.Context(), account, job,
		string(connector.SourceSnaplink), map[string]any{"source_operation_id": syncCorrelationSourceID})
	if status != "unknown" || operationID != syncCorrelationSourceID || result != nil {
		t.Fatalf("reconcileEraseSource = %q/%q/%#v, want unknown/%s/nil result",
			status, operationID, result, syncCorrelationSourceID)
	}
	var remote *connector.RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("err = %v, want a *connector.RemoteError", err)
	}
	if remote.Code != "source.source_operation_id_mismatch" ||
		remote.Kind != connector.ErrorUnknown || !remote.Temporary {
		t.Fatalf("remote error = %+v, want unknown/temporary source.source_operation_id_mismatch", remote)
	}
	if fake.statusCalls != 1 {
		t.Fatalf("status calls = %d, want one status lookup", fake.statusCalls)
	}
}
