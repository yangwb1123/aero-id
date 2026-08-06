package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strings"
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

// TestAuditAnchor_ChainsToPreviousAnchor: a second anchoring run chains its
// anchor to the anchor written by the first run, so anchor objects form a
// tamper-evident chain instead of isolated snapshots.
func TestAuditAnchor_ChainsToPreviousAnchor(t *testing.T) {
	store := &memoryStore{objects: map[string][]byte{}}
	ledger := &fakeChainLedger{facts: []*domain.AuditFact{anchorFact(1), anchorFact(2), anchorFact(3)}}
	svc := anchorTestService(ledger, store)
	if _, err := svc.AnchorOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := storedAnchor(t, store, 3)
	if first["previous_anchor_hash"] != "" {
		t.Errorf("first anchor previous hash = %v, want empty", first["previous_anchor_hash"])
	}

	// The chain head advances past the previous run's head.
	ledger.facts = append(ledger.facts, anchorFact(4), anchorFact(5))
	if _, err := svc.AnchorOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := storedAnchor(t, store, 5)
	if second["previous_anchor_hash"] != first["anchor_hash"] {
		t.Errorf("second anchor previous hash = %v, want the first anchor's self-hash %v",
			second["previous_anchor_hash"], first["anchor_hash"])
	}
	if anchorDigest(second) != second["anchor_hash"] {
		t.Error("second anchor self-hash does not verify")
	}
}

// storedAnchor reads the stored anchor document with the given chain sequence.
func storedAnchor(t *testing.T, store *memoryStore, wantSequence int64) map[string]any {
	t.Helper()
	for _, raw := range store.objects {
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			continue
		}
		if document["kind"] == "audit_anchor" && document["chain_sequence"] == float64(wantSequence) {
			return document
		}
	}
	t.Fatalf("no stored anchor with chain sequence %d", wantSequence)
	return nil
}

// orderedAnchorStore lists keys in a fixed order to model backends whose
// enumeration is lexicographic rather than chronological.
type orderedAnchorStore struct {
	*memoryStore
	order []string
}

func (o *orderedAnchorStore) List(ctx context.Context, fn func(storage.ObjectInfo) (bool, error)) error {
	for _, key := range o.order {
		ok, err := fn(storage.ObjectInfo{Key: key, Modified: time.Now()})
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	return nil
}

// TestAuditAnchor_ManifestReturnsNewestAnchor: the manifest must return the
// anchor with the highest chain sequence, not the first object the backend
// happens to list (lexicographic order puts ...-10.json before ...-2.json).
func TestAuditAnchor_ManifestReturnsNewestAnchor(t *testing.T) {
	store := &orderedAnchorStore{memoryStore: &memoryStore{objects: map[string][]byte{}}}
	ledger := &fakeChainLedger{facts: []*domain.AuditFact{anchorFact(1)}}
	svc := anchorTestService(ledger, store)
	if _, err := svc.AnchorOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	ledger.facts = append(ledger.facts, anchorFact(2))
	if _, err := svc.AnchorOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	ledger.facts = append(ledger.facts, anchorFact(3), anchorFact(4), anchorFact(5),
		anchorFact(6), anchorFact(7), anchorFact(8), anchorFact(9), anchorFact(10))
	if _, err := svc.AnchorOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(store.objects))
	for key := range store.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	store.order = keys

	manifest, err := svc.AnchorManifest(context.Background(), "aero-id", "tenant-a")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest["chain_sequence"] != float64(10) || manifest["record_hash"] != "record-10" {
		t.Errorf("manifest = %v, want the newest anchor (sequence 10)", manifest)
	}
}

// advancingChainLedger models a live chain: the head advances right after the
// first verification, so a naive verify-then-read sequence would anchor a head
// that was never verified.
type advancingChainLedger struct {
	verifies int
	facts    []*domain.AuditFact
}

func (f *advancingChainLedger) Append(ctx context.Context, fact *domain.AuditFact) error { return nil }
func (f *advancingChainLedger) List(ctx context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	return f.facts, "", nil
}
func (f *advancingChainLedger) VerifyChain(ctx context.Context, source, partition string) error {
	f.verifies++
	return nil
}
func (f *advancingChainLedger) ChainPartitions(ctx context.Context) ([]repository.ChainPartition, error) {
	return []repository.ChainPartition{{Source: "aero-id", Partition: "tenant-a"}}, nil
}
func (f *advancingChainLedger) ChainHead(ctx context.Context, source, partition string) (*domain.AuditFact, error) {
	if f.verifies == 0 {
		return f.facts[0], nil // the head still sits at sequence 1 before the first verification
	}
	return f.facts[1], nil // a new fact lands right after the first verification
}

// TestAuditAnchor_AnchorsOnlyVerifiedHead: when the head advances during
// verification, the service re-verifies instead of anchoring an unverified
// head, and the anchored head is always covered by a verification.
func TestAuditAnchor_AnchorsOnlyVerifiedHead(t *testing.T) {
	store := &memoryStore{objects: map[string][]byte{}}
	ledger := &advancingChainLedger{facts: []*domain.AuditFact{anchorFact(1), anchorFact(2)}}
	svc := anchorTestService(ledger, store)
	result, err := svc.AnchorOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chains) != 1 || result.Chains[0].Sequence != 2 || result.Chains[0].RecordHash != "record-2" {
		t.Fatalf("anchored = %+v, want sequence 2 / record-2", result.Chains)
	}
	if ledger.verifies < 2 {
		t.Fatalf("chain verified %d times, want >= 2 (the anchored head must be covered by a verification)", ledger.verifies)
	}
}

// TestAuditAnchor_SanitizesKeySegments: untrusted source/partition values must
// not escape the anchor prefix inside the storage key.
func TestAuditAnchor_SanitizesKeySegments(t *testing.T) {
	store := &memoryStore{objects: map[string][]byte{}}
	fact := anchorFact(1)
	fact.PartitionKey = "tenant/a b"
	fact.Source = "aero id"
	svc := anchorTestService(&fakeChainLedger{facts: []*domain.AuditFact{fact}}, store)
	result, err := svc.AnchorOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	key := result.Chains[0].AnchorKey
	if strings.Contains(key, " ") || strings.Contains(key, "tenant/a") || strings.Contains(key, "aero id") {
		t.Errorf("anchor key contains unsanitized source/partition: %q", key)
	}
	if !strings.HasPrefix(key, "audit-anchor/aero_id/tenant_a_b/") {
		t.Errorf("anchor key = %q, want audit-anchor/aero_id/tenant_a_b/...", key)
	}
}

var _ = hmac.Equal
var _ = sha256.Sum256
var _ = hex.EncodeToString
var _ = storage.ErrNotFound
