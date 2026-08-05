package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

// fakeChainLedger serves one chain with two facts and verifies them.
type fakeChainLedger struct {
	facts []*domain.AuditFact
	bad   bool
}

func (f *fakeChainLedger) Append(ctx context.Context, fact *domain.AuditFact) error { return nil }
func (f *fakeChainLedger) List(ctx context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	return f.facts, "", nil
}
func (f *fakeChainLedger) VerifyChain(ctx context.Context, source, partition string) error {
	if f.bad {
		return errFakeChainBroken
	}
	return nil
}
func (f *fakeChainLedger) ChainPartitions(ctx context.Context) ([]repository.ChainPartition, error) {
	return []repository.ChainPartition{{Source: "aero-id", Partition: "tenant-a"}}, nil
}
func (f *fakeChainLedger) ChainHead(ctx context.Context, source, partition string) (*domain.AuditFact, error) {
	return f.facts[len(f.facts)-1], nil
}

var errFakeChainBroken = &fakeChainError{}

type fakeChainError struct{}

func (*fakeChainError) Error() string { return "chain broken" }

func anchorFact(sequence int64) *domain.AuditFact {
	previous := ""
	if sequence > 1 {
		previous = "prev-hash"
	}
	return &domain.AuditFact{ID: idgen.NewID(), HashVersion: 1, ChainSequence: sequence,
		Source: "aero-id", SourceRegion: "local", PartitionKey: "tenant-a",
		Action: "account.provisioned", TenantID: "tenant-a", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"outcome": "success"}, PreviousHash: previous,
		RecordHash: "record-" + itoa(sequence), SignatureVersion: 1, Signature: "sig-" + itoa(sequence)}
}

func anchorTestService(ledger repository.AuditLedgerRepository, store storage.FileStorage) *auditAnchorService {
	return NewAuditAnchorService(ledger, store).(*auditAnchorService)
}

// TestAuditAnchor_WritesImmutableAnchor: anchoring a verified chain writes an
// object whose document carries the chain head sequence, hash and signature,
// with a self-hash that covers the document.
func TestAuditAnchor_WritesImmutableAnchor(t *testing.T) {
	store := &memoryStore{objects: map[string][]byte{}}
	svc := anchorTestService(&fakeChainLedger{facts: []*domain.AuditFact{anchorFact(1), anchorFact(2)}}, store)

	result, err := svc.AnchorOnce(context.Background())
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if result.Anchored != 1 || len(result.Chains) != 1 {
		t.Fatalf("anchored = %d", result.Anchored)
	}
	if result.Chains[0].RecordHash != "record-2" {
		t.Errorf("anchored hash = %s, want the chain head record-2", result.Chains[0].RecordHash)
	}
	if result.Chains[0].AnchorKey == "" {
		t.Fatal("anchor key missing")
	}
	reader, err := store.Get(context.Background(), result.Chains[0].AnchorKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(reader)
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document["kind"] != "audit_anchor" || document["chain_sequence"] != float64(2) ||
		document["signature"] != "sig-2" {
		t.Errorf("anchor document wrong: %v", document)
	}
	// The self-hash covers the stable fields and survives JSON round-trips.
	if document["anchor_hash"] != anchorDigest(document) {
		t.Error("anchor_hash does not verify")
	}
}

// TestAuditAnchor_RefusesBrokenChain: a chain that fails verification is never
// anchored (fail-closed tamper evidence).
func TestAuditAnchor_RefusesBrokenChain(t *testing.T) {
	store := &memoryStore{objects: map[string][]byte{}}
	svc := anchorTestService(&fakeChainLedger{facts: []*domain.AuditFact{anchorFact(1)}, bad: true}, store)
	if _, err := svc.AnchorOnce(context.Background()); err == nil {
		t.Fatal("broken chain must refuse anchoring")
	}
	if len(store.objects) != 0 {
		t.Fatal("no anchor may be written for a broken chain")
	}
}

// TestAuditAnchor_ManifestReadback: the anchor manifest can be read back by
// chain identity.
func TestAuditAnchor_ManifestReadback(t *testing.T) {
	store := &memoryStore{objects: map[string][]byte{}}
	ledger := &fakeChainLedger{facts: []*domain.AuditFact{anchorFact(3)}}
	svc := anchorTestService(ledger, store)
	if _, err := svc.AnchorOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	manifest, err := svc.AnchorManifest(context.Background(), "aero-id", "tenant-a")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest["record_hash"] != "record-3" {
		t.Errorf("manifest hash = %v, want record-3", manifest["record_hash"])
	}
}

var _ = hmac.Equal
var _ = sha256.Sum256
var _ = hex.EncodeToString
var _ = storage.ErrNotFound
