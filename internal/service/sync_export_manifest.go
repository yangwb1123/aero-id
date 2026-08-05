package service

import (
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

func exportAccount(account *domain.Account) map[string]any {
	return map[string]any{"account_id": account.ID.String(), "canonical_uid": account.CanonicalUID,
		"home_region": account.HomeRegion, "status": account.Status, "created_at": account.CreatedAt, "updated_at": account.UpdatedAt}
}

func exportProfile(profile *domain.AccountProfile) map[string]any {
	if profile == nil {
		return map[string]any{}
	}
	return map[string]any{"display_name": profile.DisplayName, "avatar_url": profile.AvatarURL, "locale": profile.Locale,
		"timezone": profile.Timezone, "preferences": connector.RedactSummaryData(profile.Preferences), "updated_at": profile.UpdatedAt}
}

func exportSources(values []*domain.AccountSource) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		result = append(result, map[string]any{"source": value.Source, "source_region": value.SourceRegion,
			"source_account_id": value.SourceAccountID, "scope_type": value.ScopeType, "scope_id": value.ScopeID,
			"status": value.Status, "version": value.Version, "last_synced_at": value.LastSyncedAt, "data": connector.RedactSummaryData(value.Data)})
	}
	return result
}

func exportMemberships(values []*domain.AccountMembership) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		result = append(result, map[string]any{"source": value.Source, "source_region": value.SourceRegion,
			"scope_type": value.ScopeType, "scope_id": value.ScopeID, "source_member_id": value.SourceMemberID,
			"role": value.Role, "status": value.Status, "version": value.Version, "last_synced_at": value.LastSyncedAt,
			"data": connector.RedactSummaryData(value.Data)})
	}
	return result
}

func exportSnapshot(value *domain.AccountSnapshot) map[string]any {
	return map[string]any{"source": value.Source, "source_region": value.SourceRegion, "dataset": value.Dataset,
		"status": value.Status, "version": value.Version, "generated_at": value.GeneratedAt,
		"last_synced_at": value.LastSyncedAt, "expires_at": value.ExpiresAt, "data": connector.RedactSummaryData(value.Data)}
}
