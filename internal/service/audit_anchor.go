package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

// AuditAnchorService implements P4-03 tamper-evidence anchoring: the current
// head of every (source, partition) audit chain is written to object storage
// as an anchor object carrying the chain sequence, record hash and signature,
// chained to the previous anchor. Anchors are immutable artifacts for
// WORM/object-lock archival.
type AuditAnchorService interface {
	// AnchorOnce verifies each chain and writes anchor objects. Returns the
	// anchors written this run.
	AnchorOnce(ctx context.Context) (*AnchorRunResult, error)
	// AnchorManifest returns the latest anchor for a chain, if any.
	AnchorManifest(ctx context.Context, source, partition string) (map[string]any, error)
}

type AnchorRunResult struct {
	Anchored int                 `json:"anchored"`
	Chains   []AnchorChainResult `json:"chains"`
}

type AnchorChainResult struct {
	Source     string `json:"source"`
	Partition  string `json:"partition"`
	Sequence   int64  `json:"sequence"`
	RecordHash string `json:"record_hash"`
	AnchorKey  string `json:"anchor_key"`
}

type auditAnchorService struct {
	ledger repository.AuditLedgerRepository
	store  storage.FileStorage
	audit  AuditLedgerService
}

type AuditAnchorOption func(*auditAnchorService)

func WithAuditAnchorAudit(audit AuditLedgerService) AuditAnchorOption {
	return func(s *auditAnchorService) { s.audit = audit }
}

func NewAuditAnchorService(ledger repository.AuditLedgerRepository, store storage.FileStorage,
	opts ...AuditAnchorOption) AuditAnchorService {
	svc := &auditAnchorService{ledger: ledger, store: store}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

const anchorPrefix = "audit-anchor"

func (s *auditAnchorService) AnchorOnce(ctx context.Context) (*AnchorRunResult, error) {
	if s.ledger == nil || s.store == nil {
		return nil, apperr.Unavailable("audit_anchor.unavailable", "audit anchor storage is not configured")
	}
	if err := s.recordAnchorRequest(ctx); err != nil {
		return nil, err
	}
	partitions, err := s.ledger.ChainPartitions(ctx)
	if err != nil {
		return nil, apperr.Internal("audit_anchor.partitions_failed", "failed to enumerate audit chains").Wrap(err)
	}
	result := &AnchorRunResult{}
	for _, partition := range partitions {
		head, err := s.verifiedHead(ctx, partition.Source, partition.Partition)
		if err != nil {
			// A broken or racing chain must not be anchored silently.
			return nil, err
		}
		anchor, err := s.writeAnchor(ctx, head)
		if err != nil {
			return nil, err
		}
		result.Anchored++
		result.Chains = append(result.Chains, AnchorChainResult{
			Source: partition.Source, Partition: partition.Partition, Sequence: head.ChainSequence,
			RecordHash: head.RecordHash, AnchorKey: anchor,
		})
	}
	return result, nil
}

func (s *auditAnchorService) recordAnchorRequest(ctx context.Context) error {
	if s.audit == nil {
		return nil // unit-test compatibility; composition wiring forbids this
	}
	// audit-site: standalone-fact — records the administrative intent before
	// any immutable archive object is written, so audit failure is fail-closed.
	return s.audit.Record(ctx, AuditFactInput{Source: "aero-id", Action: "audit.anchor.requested",
		ResourceType: "audit_anchor", ResourceID: "all_chains"})
}

func (s *auditAnchorService) AuditLoggerWired() bool { return s.audit != nil }

// verifiedHead returns the chain head only after verifying the chain and
// confirming the head did not advance while verification ran. Anchoring a
// head that was appended after the verification would commit an unverified
// record, silently defeating the fail-closed tamper-evidence guarantee; a
// head that keeps advancing (concurrent appends) fails closed after a
// bounded number of attempts.
func (s *auditAnchorService) verifiedHead(ctx context.Context, source, partition string) (*domain.AuditFact, error) {
	const attempts = 3
	for attempt := 0; attempt < attempts; attempt++ {
		head, err := s.ledger.ChainHead(ctx, source, partition)
		if err != nil {
			return nil, apperr.Internal("audit_anchor.head_failed", "failed to read the audit chain head").Wrap(err)
		}
		if err := s.ledger.VerifyChain(ctx, source, partition); err != nil {
			return nil, apperr.Internal("audit_anchor.chain_broken",
				"audit chain verification failed; refusing to anchor").Wrap(err)
		}
		after, err := s.ledger.ChainHead(ctx, source, partition)
		if err != nil {
			return nil, apperr.Internal("audit_anchor.head_failed", "failed to read the audit chain head").Wrap(err)
		}
		if after.ChainSequence == head.ChainSequence && after.RecordHash == head.RecordHash {
			return after, nil
		}
	}
	return nil, apperr.Unavailable("audit_anchor.chain_busy",
		"audit chain kept advancing during verification; refusing to anchor an unverified head")
}

func (s *auditAnchorService) writeAnchor(ctx context.Context, head *domain.AuditFact) (string, error) {
	previous, err := s.previousAnchorHash(ctx, head.Source, head.PartitionKey, head.ChainSequence)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	document := map[string]any{
		"schema_version": 1, "kind": "audit_anchor",
		"source": head.Source, "source_region": head.SourceRegion, "partition": head.PartitionKey,
		"chain_sequence": head.ChainSequence, "record_hash": head.RecordHash,
		"signature": head.Signature, "signature_version": head.SignatureVersion,
		"previous_anchor_hash": previous, "anchored_at": now,
	}
	document["anchor_hash"] = anchorDigest(document)
	raw, err := json.Marshal(document)
	if err != nil {
		return "", apperr.Internal("audit_anchor.encode_failed", "failed to encode the audit anchor").Wrap(err)
	}
	key, _, err := s.store.Put(ctx, "aero-id", anchorKey(head.Source, head.PartitionKey, head.ChainSequence), bytes.NewReader(raw))
	if err != nil {
		return "", apperr.Internal("audit_anchor.write_failed", "failed to write audit anchor").Wrap(err)
	}
	return key, nil
}

// previousAnchorHash resolves the previous anchor's self-hash so anchors form
// a chain (P4-03). Anchor objects are matched by content because storage
// backends may assign opaque keys; the predecessor is the newest anchor of
// this chain with a chain sequence strictly below the current head. A store
// without a lister cannot resolve a predecessor and yields an empty previous
// hash (single-anchor chain); a listing failure aborts the run fail-closed.
func (s *auditAnchorService) previousAnchorHash(ctx context.Context, source, partition string, sequence int64) (string, error) {
	lister, ok := s.store.(storage.Lister)
	if !ok {
		return "", nil
	}
	previous := ""
	var previousSequence int64 = -1
	err := lister.List(ctx, func(info storage.ObjectInfo) (bool, error) {
		document, err := s.anchorObject(ctx, info.Key, source, partition)
		if err != nil {
			return false, err
		}
		if document == nil {
			return true, nil
		}
		seq, ok := document["chain_sequence"].(float64)
		if !ok || int64(seq) >= sequence {
			return true, nil // not an anchor of this chain, or not a predecessor
		}
		if int64(seq) > previousSequence {
			previousSequence = int64(seq)
			if hash, ok := document["anchor_hash"].(string); ok {
				previous = hash
			}
		}
		return true, nil
	})
	if err != nil {
		return "", apperr.Internal("audit_anchor.list_failed", "failed to enumerate audit anchors").Wrap(err)
	}
	return previous, nil
}

// anchorKey builds the storage name of a chain anchor. Source and partition
// are sanitized in every segment so untrusted values cannot escape the anchor
// prefix or break the key convention.
func anchorKey(source, partition string, sequence int64) string {
	return strings.Join([]string{anchorPrefix, sanitizeSource(source), sanitizePartition(partition),
		sanitizeSource(source) + "-" + sanitizePartition(partition) + "-" + itoa(sequence) + ".json"}, "/")
}

func sanitizeSource(source string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '_'
	}, source)
}

func sanitizePartition(partition string) string {
	return sanitizeSource(partition)
}

// anchorDigest hashes the stable anchor fields only (timestamps are excluded
// so the digest is reproducible across marshalling and read-back).
func anchorDigest(document map[string]any) string {
	stable, _ := json.Marshal(map[string]any{
		"kind": document["kind"], "source": document["source"], "partition": document["partition"],
		"chain_sequence": document["chain_sequence"], "record_hash": document["record_hash"],
		"signature": document["signature"], "signature_version": document["signature_version"],
		"previous_anchor_hash": document["previous_anchor_hash"],
	})
	sum := sha256.Sum256(stable)
	return hex.EncodeToString(sum[:])
}

// anchorObject reads and parses one candidate object. nil, nil means the object
// is not an anchor for this chain or cannot be read (skip). A non-nil error
// aborts the enumeration.
func (s *auditAnchorService) anchorObject(ctx context.Context, key, source, partition string) (map[string]any, error) {
	if !strings.HasPrefix(key, anchorPrefix) {
		return nil, nil
	}
	reader, err := s.store.Get(ctx, key)
	if err != nil {
		//nolint:nilerr // unreadable archive object: skip, never fail the manifest.
		return nil, nil
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
	reader.Close()
	if readErr != nil {
		//nolint:nilerr // truncated archive object: skip, never fail the manifest.
		return nil, nil
	}
	var document map[string]any
	if json.Unmarshal(data, &document) != nil {
		//nolint:nilerr // corrupt archive object: skip, never fail the manifest.
		return nil, nil
	}
	if document["kind"] != "audit_anchor" || document["source"] != source || document["partition"] != partition {
		return nil, nil
	}
	return document, nil
}

// AnchorManifest returns the newest stored anchor for a chain, resolved by
// enumerating anchor objects and matching their content (storage backends may
// assign opaque keys, so the manifest fields are the source of truth).
func (s *auditAnchorService) AnchorManifest(ctx context.Context, source, partition string) (map[string]any, error) {
	if s.ledger == nil || s.store == nil {
		return nil, apperr.Unavailable("audit_anchor.unavailable", "audit anchor storage is not configured")
	}
	lister, ok := s.store.(storage.Lister)
	if !ok {
		return nil, apperr.Unavailable("audit_anchor.listing_unavailable", "anchor storage cannot enumerate objects")
	}
	var newest map[string]any
	var newestSequence int64 = -1
	if err := lister.List(ctx, func(info storage.ObjectInfo) (bool, error) {
		// Unreadable/non-anchor objects are skipped by design: the manifest is
		// best-effort enumeration over possibly-partial archives.
		document, _ := s.anchorObject(ctx, info.Key, source, partition)
		if document == nil {
			return true, nil
		}
		// Listing order is backend-defined (map iteration, lexicographic
		// keys), never chronological, so the newest anchor is the maximum
		// chain sequence, not the first object listed.
		if seq, ok := document["chain_sequence"].(float64); ok && int64(seq) > newestSequence {
			newestSequence = int64(seq)
			newest = document
		}
		return true, nil
	}); err != nil {
		return nil, apperr.Internal("audit_anchor.list_failed", "failed to enumerate audit anchors").Wrap(err)
	}
	if newest == nil {
		return nil, apperr.NotFound("audit_anchor.not_found", "anchor not found")
	}
	if err := s.recordManifestRead(ctx, source, partition); err != nil {
		return nil, err
	}
	return newest, nil
}

func (s *auditAnchorService) recordManifestRead(ctx context.Context, source, partition string) error {
	if s.audit == nil {
		return nil // unit-test compatibility; composition wiring forbids this
	}
	// audit-site: standalone-fact — successful sensitive evidence reads are
	// durable; a failed append prevents the manifest from leaving the service.
	return s.audit.Record(ctx, AuditFactInput{Source: "aero-id", Action: "audit.anchor.manifest_viewed",
		ResourceType: "audit_anchor", ResourceID: source + ":" + partition,
		Payload: map[string]any{"source": source, "partition": partition}})
}
