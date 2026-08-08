package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

const (
	maxAccountDisplayName = 256
	maxAccountAvatarURL   = 2048
	maxAccountLocale      = 32
	maxAccountTimezone    = 64
)

func (s *accountService) GetProfile(ctx context.Context) (*domain.AccountProfile, error) {
	account, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	return s.profileOrDefault(ctx, account.ID)
}

func (s *accountService) UpdateProfile(ctx context.Context, input UpdateAccountProfileInput) (*domain.AccountProfile, error) {
	account, err := s.ensureWrite(ctx)
	if err != nil {
		return nil, err
	}
	profile, err := s.profileOrDefault(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	changed, err := applyProfileInput(profile, input)
	if err != nil {
		return nil, err
	}
	profile.Normalize()
	if s.audit != nil {
		repo, ok := s.repo.(repository.AccountProfileAuditRepository)
		if !ok {
			// Fail closed (B2-1 REQ-4): a repo without the WithAudit surface must
			// not silently fall back to a plain UpdateProfile that drops the
			// account.profile.updated fact.
			return nil, apperr.Internal("account.profile_audit_unavailable", "profile audit persistence is unavailable")
		}
		err = repo.UpdateProfileWithAudit(ctx, profile, buildAuditFact(ctx, AuditFactInput{Source: "aero-id",
			ActorUID: actorFromContext(ctx), AccountID: account.ID, TenantID: TenantFrom(ctx),
			Action: "account.profile.updated", ResourceType: "account_profile", ResourceID: account.ID.String(),
			Payload: map[string]any{"fields": changed}}))
	} else {
		err = s.repo.UpdateProfile(ctx, profile)
	}
	if err != nil {
		return nil, mapAccountError(err, "account.profile_update_failed", "failed to update account profile")
	}
	return profile, nil
}

func applyProfileInput(profile *domain.AccountProfile, input UpdateAccountProfileInput) ([]string, error) {
	changed := make([]string, 0, 5)
	if err := validateProfileURL(input.AvatarURL); err != nil {
		return nil, err
	}
	if err := validateProfileTimezone(input.Timezone); err != nil {
		return nil, err
	}
	updates := profileTextUpdates(profile, input)
	for _, update := range updates {
		ok, err := applyProfileString(update.target, update.input, update.max, update.code, update.message)
		if err != nil {
			return nil, err
		}
		if ok {
			changed = append(changed, update.name)
		}
	}
	if input.Preferences != nil {
		if err := validateAccountPreferences(input.Preferences); err != nil {
			return nil, err
		}
		profile.Preferences = input.Preferences
		changed = append(changed, "preferences")
	}
	return changed, nil
}

func validateProfileURL(value *string) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(*value))
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return apperr.BadRequest("account.profile_avatar_invalid", "avatar URL must be an http or https URL")
	}
	return nil
}

func validateProfileTimezone(value *string) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	if _, err := time.LoadLocation(strings.TrimSpace(*value)); err != nil {
		return apperr.BadRequest("account.profile_timezone_invalid", "timezone is not supported")
	}
	return nil
}

type profileTextUpdate struct {
	target  *string
	input   *string
	max     int
	code    string
	message string
	name    string
}

func profileTextUpdates(profile *domain.AccountProfile, input UpdateAccountProfileInput) []profileTextUpdate {
	return []profileTextUpdate{
		{target: &profile.DisplayName, input: input.DisplayName, max: maxAccountDisplayName,
			code: "account.profile_display_name_too_long", message: "display name is too long", name: "display_name"},
		{target: &profile.AvatarURL, input: input.AvatarURL, max: maxAccountAvatarURL,
			code: "account.profile_avatar_url_too_long", message: "avatar URL is too long", name: "avatar_url"},
		{target: &profile.Locale, input: input.Locale, max: maxAccountLocale,
			code: "account.profile_locale_too_long", message: "locale is too long", name: "locale"},
		{target: &profile.Timezone, input: input.Timezone, max: maxAccountTimezone,
			code: "account.profile_timezone_too_long", message: "timezone is too long", name: "timezone"},
	}
}

func applyProfileString(target *string, input *string, max int, code, message string) (bool, error) {
	if input == nil {
		return false, nil
	}
	value := strings.TrimSpace(*input)
	if len(value) > max {
		return false, apperr.BadRequest(code, message)
	}
	*target = value
	return true, nil
}

func validateAccountPreferences(value map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 32*1024 {
		return apperr.BadRequest("account.preferences_too_large", "preferences are too large")
	}
	if err := validatePreferenceValue(value, 0); err != nil {
		return apperr.BadRequest("account.preferences_invalid", "preferences contain an unsupported value")
	}
	return nil
}

func validatePreferenceValue(value any, depth int) error {
	if depth > 4 {
		return apperr.BadRequest("account.preferences_invalid", "preferences are nested too deeply")
	}
	switch typed := value.(type) {
	case map[string]any:
		return validatePreferenceMap(typed, depth)
	case []any:
		return validatePreferenceList(typed, depth)
	case string:
		if len(typed) > 4096 {
			return apperr.BadRequest("account.preferences_invalid", "a preference value is too long")
		}
	}
	return nil
}

func validatePreferenceMap(value map[string]any, depth int) error {
	if len(value) > 128 {
		return apperr.BadRequest("account.preferences_invalid", "preferences contain too many fields")
	}
	for key, child := range value {
		if preferenceSensitiveKey(key) || len(key) > 128 {
			return apperr.BadRequest("account.preferences_invalid", "preferences contain a restricted field")
		}
		if err := validatePreferenceValue(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validatePreferenceList(value []any, depth int) error {
	if len(value) > 128 {
		return apperr.BadRequest("account.preferences_invalid", "preferences contain too many values")
	}
	for _, child := range value {
		if err := validatePreferenceValue(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func preferenceSensitiveKey(value string) bool {
	value = strings.ToLower(strings.ReplaceAll(value, "-", "_"))
	for _, part := range []string{"password", "token", "secret", "cookie", "mfa", "recovery", "credential", "private_key", "access_key"} {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}
