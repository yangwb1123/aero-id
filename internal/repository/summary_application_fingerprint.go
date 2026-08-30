package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

type snapshotFingerprint struct {
	Present      bool                  `json:"present"`
	Dataset      domain.AccountDataset `json:"dataset"`
	Source       string                `json:"source"`
	SourceRegion string                `json:"source_region"`
	Status       domain.SnapshotStatus `json:"status"`
	Version      int64                 `json:"version"`
	GeneratedAt  time.Time             `json:"generated_at"`
	ExpiresAt    *time.Time            `json:"expires_at,omitempty"`
	ErrorCode    string                `json:"error_code"`
	Data         map[string]any        `json:"data"`
}

type sourceFingerprint struct {
	AccountID       domain.ID      `json:"account_id"`
	Source          string         `json:"source"`
	SourceRegion    string         `json:"source_region"`
	SourceAccountID string         `json:"source_account_id"`
	ScopeType       string         `json:"scope_type"`
	ScopeID         string         `json:"scope_id"`
	Status          string         `json:"status"`
	Version         int64          `json:"version"`
	GeneratedAt     *time.Time     `json:"generated_at,omitempty"`
	ErrorCode       string         `json:"error_code"`
	Data            map[string]any `json:"data"`
}

type membershipFingerprint struct {
	AccountID      domain.ID      `json:"account_id"`
	Source         string         `json:"source"`
	SourceRegion   string         `json:"source_region"`
	ScopeType      string         `json:"scope_type"`
	ScopeID        string         `json:"scope_id"`
	SourceMemberID string         `json:"source_member_id"`
	Role           string         `json:"role"`
	Status         string         `json:"status"`
	Version        int64          `json:"version"`
	ErrorCode      string         `json:"error_code"`
	Data           map[string]any `json:"data"`
}

type summaryFingerprintEnvelope struct {
	Source       string      `json:"source"`
	SourceRegion string      `json:"source_region"`
	Coverage     string      `json:"coverage"`
	Skipped      []string    `json:"skipped,omitempty"`
	Payload      interface{} `json:"payload"`
}

func summaryApplicationFingerprints(request domain.SummaryApplicationRequest,
	tokens []summaryCoverageToken) (map[string]string, error) {
	result := make(map[string]string, len(tokens))
	skipped := append([]string(nil), request.SkippedDatasets...)
	sort.Strings(skipped)
	for _, token := range tokens {
		var payload interface{}
		switch token.kind {
		case summaryCoverageDataset:
			payload = snapshotFingerprintPayload(request.Snapshots, token.dataset)
		case summaryCoverageSources:
			payload = sourceFingerprintPayload(request.Sources)
		case summaryCoverageMemberships:
			payload = membershipFingerprintPayload(request.Memberships)
		default:
			return nil, summaryValidationError("summary.coverage_invalid", "summary coverage is not registered")
		}
		encoded, err := json.Marshal(summaryFingerprintEnvelope{Source: request.Source, SourceRegion: request.SourceRegion,
			Coverage: token.key, Skipped: skipped, Payload: payload})
		if err != nil {
			return nil, summaryValidationError("summary.payload_invalid", "summary payload is not serializable")
		}
		digest := sha256.Sum256(encoded)
		result[token.key] = hex.EncodeToString(digest[:])
	}
	return result, nil
}

func snapshotFingerprintPayload(values []*domain.AccountSnapshot, dataset domain.AccountDataset) snapshotFingerprint {
	for _, snapshot := range values {
		if snapshot != nil && snapshot.Dataset == dataset {
			return snapshotFingerprint{Present: true, Dataset: snapshot.Dataset, Source: snapshot.Source,
				SourceRegion: snapshot.SourceRegion, Status: snapshot.Status, Version: snapshot.Version,
				GeneratedAt: snapshot.GeneratedAt, ExpiresAt: snapshot.ExpiresAt, ErrorCode: snapshot.ErrorCode,
				Data: snapshot.Data}
		}
	}
	return snapshotFingerprint{Dataset: dataset}
}

func sourceFingerprintPayload(values []*domain.AccountSource) []sourceFingerprint {
	result := make([]sourceFingerprint, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		result = append(result, sourceFingerprint{AccountID: value.AccountID, Source: value.Source,
			SourceRegion: value.SourceRegion, SourceAccountID: value.SourceAccountID, ScopeType: value.ScopeType,
			ScopeID: value.ScopeID, Status: value.Status, Version: value.Version, GeneratedAt: value.GeneratedAt,
			ErrorCode: value.ErrorCode, Data: value.Data})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SourceAccountID != result[j].SourceAccountID {
			return result[i].SourceAccountID < result[j].SourceAccountID
		}
		if result[i].ScopeType != result[j].ScopeType {
			return result[i].ScopeType < result[j].ScopeType
		}
		return result[i].ScopeID < result[j].ScopeID
	})
	return result
}

func membershipFingerprintPayload(values []*domain.AccountMembership) []membershipFingerprint {
	result := make([]membershipFingerprint, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		result = append(result, membershipFingerprint{AccountID: value.AccountID, Source: value.Source,
			SourceRegion: value.SourceRegion, ScopeType: value.ScopeType, ScopeID: value.ScopeID,
			SourceMemberID: value.SourceMemberID, Role: value.Role, Status: value.Status, Version: value.Version,
			ErrorCode: value.ErrorCode, Data: value.Data})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ScopeType != result[j].ScopeType {
			return result[i].ScopeType < result[j].ScopeType
		}
		if result[i].ScopeID != result[j].ScopeID {
			return result[i].ScopeID < result[j].ScopeID
		}
		return result[i].SourceMemberID < result[j].SourceMemberID
	})
	return result
}
