package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	aeroIMAccountSummaryAssertionScope    = "aero.account.summary.read"
	aeroIMAccountSummaryAssertionAudience = "aero-im-account-summary"
	aeroIMAccountSummaryAssertionSubject  = "aero-id-sync"
	aeroIMAccountSummaryOAuthResource     = "aero-im"
)

func validateAeroIMTargetAssertion(source SourceEndpointConfig) error {
	if !aeroIMTargetAssertionConfigured(source) {
		return nil
	}
	if err := validateAeroIMTargetAssertionKey(source); err != nil {
		return err
	}
	if err := validateAeroIMTargetAssertionIdentity(source); err != nil {
		return err
	}
	if err := validateAeroIMTargetAssertionOAuth(source); err != nil {
		return err
	}
	if err := validateAeroIMTargetAssertionLifetime(source); err != nil {
		return err
	}
	if err := validateAeroIMTargetAssertionPath(source); err != nil {
		return err
	}
	return validateAeroIMTargetAssertionKeyReferences(source)
}

func aeroIMTargetAssertionConfigured(source SourceEndpointConfig) bool {
	values := []string{source.TargetAssertionKeyFile, source.TargetAssertionKeyEnv,
		source.TargetAssertionKeyID, source.TargetAssertionIssuer, source.TargetAssertionAudience,
		source.TargetAssertionSubject, source.TargetAssertionClientID}
	configured := false
	for _, value := range values {
		configured = configured || strings.TrimSpace(value) != ""
	}
	return configured || source.TargetAssertionLifetime != 0
}

func validateAeroIMTargetAssertionKey(source SourceEndpointConfig) error {
	fileEmpty := strings.TrimSpace(source.TargetAssertionKeyFile) == ""
	envEmpty := strings.TrimSpace(source.TargetAssertionKeyEnv) == ""
	if fileEmpty == envEmpty {
		return fmt.Errorf("sources.aero_im target assertion requires exactly one private-key file or environment reference")
	}
	return nil
}

func validateAeroIMTargetAssertionIdentity(source SourceEndpointConfig) error {
	for field, value := range map[string]string{
		"target_assertion_key_id": source.TargetAssertionKeyID,
		"target_assertion_issuer": source.TargetAssertionIssuer,
	} {
		if !validTargetAssertionConfigText(value) {
			return fmt.Errorf("sources.aero_im.%s is required and must be a bounded non-whitespace value", field)
		}
	}
	if strings.TrimSpace(source.TargetAssertionAudience) != aeroIMAccountSummaryAssertionAudience {
		return fmt.Errorf("sources.aero_im.target_assertion_audience must be %q", aeroIMAccountSummaryAssertionAudience)
	}
	if strings.TrimSpace(source.TargetAssertionSubject) != aeroIMAccountSummaryAssertionSubject {
		return fmt.Errorf("sources.aero_im.target_assertion_subject must be %q", aeroIMAccountSummaryAssertionSubject)
	}
	if !validTargetAssertionConfigText(source.TargetAssertionClientID) {
		return fmt.Errorf("sources.aero_im.target_assertion_client_id is required and must be a bounded non-whitespace value")
	}
	clientID := strings.TrimSpace(source.TargetAssertionClientID)
	if clientID != strings.TrimSpace(source.OAuthClientID) {
		return fmt.Errorf("sources.aero_im target assertion client ID must match oauth_client_id")
	}
	if strings.TrimSpace(source.BearerToken) != "" {
		return fmt.Errorf("sources.aero_im target assertions require OAuth client credentials, not bearer_token")
	}
	return nil
}

func validateAeroIMTargetAssertionOAuth(source SourceEndpointConfig) error {
	valid := strings.TrimSpace(source.OAuthTokenURL) != "" && strings.TrimSpace(source.OAuthSecret) != ""
	valid = valid && strings.Join(strings.Fields(source.OAuthScope), " ") == aeroIMAccountSummaryAssertionScope
	valid = valid && strings.TrimSpace(source.OAuthResource) == aeroIMAccountSummaryOAuthResource
	if !valid {
		return fmt.Errorf(
			"sources.aero_im target assertion requires OAuth scope %q and resource %q",
			aeroIMAccountSummaryAssertionScope, aeroIMAccountSummaryOAuthResource,
		)
	}
	return nil
}

func validateAeroIMTargetAssertionLifetime(source SourceEndpointConfig) error {
	lifetime := source.TargetAssertionLifetime
	if lifetime < 0 || lifetime > time.Minute || (lifetime > 0 && lifetime%time.Second != 0) {
		return fmt.Errorf("sources.aero_im target assertion lifetime must be 1m or less in whole seconds")
	}
	return nil
}

func validateAeroIMTargetAssertionPath(source SourceEndpointConfig) error {
	if source.SummaryPath != "" && source.SummaryPath != "/internal/account-summary" {
		return fmt.Errorf("sources.aero_im summary_path must be /internal/account-summary when target assertions are enabled")
	}
	return nil
}

func validateAeroIMTargetAssertionKeyReferences(source SourceEndpointConfig) error {
	if source.TargetAssertionKeyEnv != "" && !validEnvironmentName(strings.TrimSpace(source.TargetAssertionKeyEnv)) {
		return fmt.Errorf("sources.aero_im target assertion key environment reference is invalid")
	}
	file := strings.TrimSpace(source.TargetAssertionKeyFile)
	if file != "" && (!validTargetAssertionConfigText(source.TargetAssertionKeyFile) || !filepath.IsAbs(file)) {
		return fmt.Errorf("sources.aero_im target assertion key file must be an absolute safe path")
	}
	return nil
}

func validTargetAssertionConfigText(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 512 && strings.IndexFunc(value, func(character rune) bool {
		return character == ' ' || character == '\t' || character == '\r' || character == '\n' || character == '\x00' || character == '\x7f'
	}) == -1
}

func validEnvironmentName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		first := index == 0
		letter := character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if !letter && character != '_' && !(digit && !first) {
			return false
		}
	}
	return true
}
