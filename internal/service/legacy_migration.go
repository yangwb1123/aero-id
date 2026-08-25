package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// LegacyMigrationService runs the P8-02 shadow comparison: legacy
// compatibility rows (files/messages/comments) are digested locally and, when
// the target authority connector exposes comparable counters, compared with
// the source summary. It never mutates legacy data and produces no effects.
type LegacyMigrationService interface {
	Compare(ctx context.Context, input LegacyCompareInput) (*LegacyCompareResult, error)
}

type LegacyCompareInput struct {
	OwnerUID string           // legacy uid (Snaplink subject)
	Source   connector.Source // target authority to compare against (aero-vault | aero-im)
	Limit    int
}

// LegacyCompareResult reports legacy digests and source counters.
type LegacyCompareResult struct {
	OwnerUID        string              `json:"owner_uid"`
	Legacy          LegacyDigestSummary `json:"legacy"`
	SourceCounters  map[string]int64    `json:"source_counters,omitempty"` // e.g. aero-vault.object_count
	SourceAvailable bool                `json:"source_available"`
	SourceErrors    map[string]string   `json:"source_errors,omitempty"`
	Drift           []string            `json:"drift,omitempty"` // human-readable mismatches
	Matched         bool                `json:"matched"`
	Partial         bool                `json:"partial"` // legacy digest truncated by limit
}

type LegacyDigestSummary struct {
	Files    *repository.LegacyDataDigest `json:"files,omitempty"`
	Messages *repository.LegacyDataDigest `json:"messages,omitempty"`
	Comments *repository.LegacyDataDigest `json:"comments,omitempty"`
}

type legacyMigrationService struct {
	legacy       repository.LegacyMigrationRepository
	connectors   map[connector.Source]connector.Connector
	audit        AuditLedgerService
	defaultLimit int
}

func NewLegacyMigrationService(legacy repository.LegacyMigrationRepository,
	connectors ...connector.Connector) LegacyMigrationService {
	return newLegacyMigrationService(legacy, nil, connectors...)
}

func NewLegacyMigrationServiceWithAudit(legacy repository.LegacyMigrationRepository,
	audit AuditLedgerService, connectors ...connector.Connector) LegacyMigrationService {
	return newLegacyMigrationService(legacy, audit, connectors...)
}

func newLegacyMigrationService(legacy repository.LegacyMigrationRepository, audit AuditLedgerService,
	connectors ...connector.Connector) LegacyMigrationService {
	bySource := make(map[connector.Source]connector.Connector, len(connectors))
	for _, client := range connectors {
		if client != nil {
			bySource[client.Source()] = client
		}
	}
	return &legacyMigrationService{legacy: legacy, connectors: bySource, audit: audit,
		defaultLimit: defaultLegacyMigrationLimit}
}

// defaultLegacyMigrationLimit caps one compare run.
const defaultLegacyMigrationLimit = 1000
const maxLegacyMigrationLimit = 5000

// compareWithSource fills the source counters when the connector is configured
// and reachable. A missing connector is a partial result (no error); a source
// failure is returned so the caller can record the detail in the result.
func (s *legacyMigrationService) compareWithSource(ctx context.Context, input LegacyCompareInput, result *LegacyCompareResult) error {
	client, ok := s.connectors[input.Source]
	if !ok {
		result.SourceAvailable = false
		result.SourceErrors["source"] = "source summary unavailable"
		return nil
	}
	request := connector.SummaryRequest{CanonicalUID: input.OwnerUID, Region: ""}
	if input.Source == connector.SourceAeroIM {
		request.Datasets = []domain.AccountDataset{domain.DatasetAeroIMActivitySummary}
	}
	summary, err := client.Summary(ctx, request)
	if err != nil {
		return err
	}
	result.SourceAvailable = true
	result.SourceCounters = sourceMigrationCounters(summary)
	result.Drift, result.Matched = compareMigrationCounters(input.Source, result)
	return nil
}

func (s *legacyMigrationService) Compare(ctx context.Context, input LegacyCompareInput) (*LegacyCompareResult, error) {
	input, err := s.normalizeCompareInput(input)
	if err != nil {
		return nil, err
	}
	legacy, partial, err := s.loadLegacyDigests(ctx, input)
	if err != nil {
		return nil, err
	}
	result := &LegacyCompareResult{OwnerUID: input.OwnerUID, Legacy: legacy, Partial: partial,
		SourceErrors: map[string]string{}}

	// A missing connector or a source failure is a partial result: the legacy
	// digest is still returned with SourceAvailable=false and a stable detail.
	if err := s.compareWithSource(ctx, input, result); err != nil {
		result.SourceAvailable = false
		result.SourceErrors["source"] = "source summary unavailable"
	}
	if err := s.recordCompare(ctx, input, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *legacyMigrationService) normalizeCompareInput(input LegacyCompareInput) (LegacyCompareInput, error) {
	input.OwnerUID = strings.TrimSpace(input.OwnerUID)
	if input.OwnerUID == "" {
		return LegacyCompareInput{}, apperr.BadRequest("migration.owner_required", "owner_uid is required")
	}
	if input.Source != connector.SourceAeroVault && input.Source != connector.SourceAeroIM {
		return LegacyCompareInput{}, apperr.BadRequest("migration.source_invalid", "source must be aero-vault or aero-im")
	}
	if input.Limit <= 0 {
		input.Limit = s.defaultLimit
	}
	if input.Limit > maxLegacyMigrationLimit {
		return LegacyCompareInput{}, apperr.BadRequest("migration.limit_invalid", "limit exceeds the maximum batch size")
	}
	return input, nil
}

func (s *legacyMigrationService) loadLegacyDigests(ctx context.Context,
	input LegacyCompareInput) (LegacyDigestSummary, bool, error) {
	files, err := s.legacy.LegacyFileDigest(ctx, input.OwnerUID, input.Limit)
	if err != nil {
		return LegacyDigestSummary{}, false,
			apperr.Internal("migration.legacy_scan_failed", "failed to digest legacy files").Wrap(err)
	}
	messages, err := s.legacy.LegacyMessageDigest(ctx, input.OwnerUID, input.Limit)
	if err != nil {
		return LegacyDigestSummary{}, false,
			apperr.Internal("migration.legacy_scan_failed", "failed to digest legacy messages").Wrap(err)
	}
	comments, err := s.legacy.LegacyCommentDigest(ctx, input.OwnerUID, input.Limit)
	if err != nil {
		return LegacyDigestSummary{}, false,
			apperr.Internal("migration.legacy_scan_failed", "failed to digest legacy comments").Wrap(err)
	}
	legacy := LegacyDigestSummary{Files: files, Messages: messages, Comments: comments}
	return legacy, files.Truncated || messages.Truncated || comments.Truncated, nil
}

func (s *legacyMigrationService) recordCompare(ctx context.Context, input LegacyCompareInput,
	result *LegacyCompareResult) error {
	if s.audit == nil {
		return nil // unit-test compatibility; composition wiring forbids this
	}
	// audit-site: standalone-fact — the comparison reads content-derived
	// digests and source counters; a failed append prevents evidence leaving.
	if err := s.audit.Record(ctx, AuditFactInput{Source: "aero-id", Action: "legacy.migration.compared",
		ResourceType: "legacy_identity", ResourceID: input.OwnerUID,
		Payload: map[string]any{"source": string(input.Source), "limit": input.Limit,
			"matched": result.Matched, "partial": result.Partial,
			"source_available": result.SourceAvailable, "drift_count": len(result.Drift)}}); err != nil {
		return apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)
	}
	return nil
}

// sourceMigrationCounters extracts comparable counters from the connector's
// redacted summary snapshots. The key convention (e.g. object_count) is part of
// the source contract to be confirmed during P0-07 capability verification.
func sourceMigrationCounters(summary *connector.Summary) map[string]int64 {
	counters := map[string]int64{}
	if summary == nil {
		return counters
	}
	for _, snapshot := range summary.Snapshots {
		for key, value := range snapshot.Data {
			switch key {
			case "file_count", "object_count", "message_count", "comment_count":
				if number, ok := numericInt64(value); ok {
					counters[string(snapshot.Source)+"."+key] = number
				}
			}
		}
	}
	return counters
}

func compareMigrationCounters(target connector.Source, result *LegacyCompareResult) ([]string, bool) {
	drift := []string{}
	matched := true
	legacy := result.Legacy
	source := result.SourceCounters

	check := func(label string, legacyCount int64, sourceKeys ...string) {
		if legacyCount == 0 {
			return // nothing local to compare; missing counters are fine
		}
		want, ok := firstPresentCounter(source, sourceKeys)
		if !ok {
			drift = append(drift, label+": source counter "+sourceKeys[0]+" not exposed")
			matched = false
			return
		}
		if legacyCount != want {
			drift = append(drift, label+": legacy="+itoa(legacyCount)+" source="+itoa(want))
			matched = false
		}
	}
	// Only surfaces owned by the target authority are comparable: legacy files
	// migrate to Vault, messages/comments to IM (ADR-011).
	switch target {
	case connector.SourceAeroVault:
		if legacy.Files != nil {
			check("files", legacy.Files.Count, "aero-vault.file_count", "aero-vault.object_count")
		}
	case connector.SourceAeroIM:
		if legacy.Messages != nil {
			check("messages", legacy.Messages.Count, "aero-im.message_count")
		}
		if legacy.Comments != nil {
			check("comments", legacy.Comments.Count, "aero-im.comment_count")
		}
	}
	return drift, matched
}

func firstPresentCounter(source map[string]int64, keys []string) (int64, bool) {
	for _, key := range keys {
		if value, ok := source[key]; ok {
			return value, true
		}
	}
	return 0, false
}

func numericInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	default:
		return 0, false
	}
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }
