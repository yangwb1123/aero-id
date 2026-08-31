package event

import (
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestSourceBindingUsesSignedClientAndTenant(t *testing.T) {
	bindings := SourceBindings{"source-client": {Source: "aero-im", SourceRegion: "west"}}
	binding, err := bindings.Resolve(&domain.TokenIdentity{ClientID: "source-client", TenantID: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	envelope := &Envelope{}
	if err := binding.Apply(envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.TenantID != "tenant-a" || envelope.Source != "aero-im" || envelope.SourceRegion != "west" {
		t.Fatalf("bound envelope = %+v", envelope)
	}
}

func TestSourceBindingRejectsUnknownOrConflictingClaims(t *testing.T) {
	bindings := SourceBindings{"source-client": {TenantID: "tenant-a", Source: "aero-im", SourceRegion: "west"}}
	if _, err := bindings.Resolve(&domain.TokenIdentity{ClientID: "unknown", TenantID: "tenant-a"}); err == nil {
		t.Fatal("unknown client was accepted")
	}
	if _, err := bindings.Resolve(&domain.TokenIdentity{ClientID: "source-client", TenantID: "tenant-b"}); err == nil {
		t.Fatal("cross-tenant client was accepted")
	}
	binding, err := bindings.Resolve(&domain.TokenIdentity{ClientID: "source-client", TenantID: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Apply(&Envelope{Source: "aero-vault"}); err == nil {
		t.Fatal("body-selected source was accepted")
	}
}

func TestSourceBindingAllowsWildcardTenantOnlyForReplication(t *testing.T) {
	bindings := SourceBindings{"replication-client": {TenantID: "*", Source: "aero-id", SourceRegion: "west", AllowedTenants: []string{"tenant-a"}}}
	binding, err := bindings.Resolve(&domain.TokenIdentity{Verified: true, ClientID: "replication-client", AllowedTenants: []string{"tenant-a"}})
	if err != nil {
		t.Fatal(err)
	}
	envelope := &Envelope{TenantID: "tenant-a", Source: "aero-id", SourceRegion: "west"}
	if err := binding.Apply(envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.TenantID != "tenant-a" || envelope.Source != "aero-id" || envelope.SourceRegion != "west" {
		t.Fatalf("wildcard binding changed event scope: %+v", envelope)
	}
	if err := binding.Apply(&Envelope{TenantID: "tenant-b", Source: "aero-id", SourceRegion: "west"}); err == nil {
		t.Fatal("wildcard binding accepted an undelegated tenant")
	}
	if err := binding.Apply(&Envelope{Source: "aero-id", SourceRegion: "west"}); err == nil {
		t.Fatal("wildcard binding accepted an event without a tenant")
	}
	if (SourceBinding{TenantID: "*", Source: "aero-im", SourceRegion: "west"}).valid() {
		t.Fatal("wildcard tenant was accepted for a source other than aero-id")
	}
}
