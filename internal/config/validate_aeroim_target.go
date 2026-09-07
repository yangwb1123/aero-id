package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/pkg/sourceauth"
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
	if err := validateAeroIMTargetAssertionTrust(source, ""); err != nil {
		return err
	}
	return validateAeroIMTargetAssertionKeyReferences(source)
}

func aeroIMTargetAssertionConfigured(source SourceEndpointConfig) bool {
	values := []string{source.TargetAssertionKeyFile, source.TargetAssertionKeyEnv,
		source.TargetAssertionJWKSURL, source.TargetAssertionTrustDomain, source.TargetAssertionKeyID,
		source.TargetAssertionIssuer, source.TargetAssertionAudience, source.TargetAssertionSubject,
		source.TargetAssertionClientID}
	configured := false
	for _, value := range values {
		configured = configured || strings.TrimSpace(value) != ""
	}
	return configured || source.TargetAssertionLifetime != 0
}

func (c *Config) validateAeroIMTargetAssertionRequirement(source SourceEndpointConfig) error {
	if strings.TrimSpace(source.Endpoint) == "" || sourceauth.IsExplicitNonProductionLoopback(source.Endpoint, c.App.Env, source.AllowInsecureLoopback) {
		return nil
	}
	if !aeroIMTargetAssertionConfigured(source) {
		return fmt.Errorf("sources.aero_im target assertion is required for an enabled non-loopback or production-like endpoint")
	}
	if sourceauth.ProductionLikeEnvironment(c.App.Env) && strings.TrimSpace(source.TargetAssertionJWKSURL) == "" {
		return fmt.Errorf("sources.aero_im target assertion JWKS URL is required in production-like environments")
	}
	if err := validateAeroIMTargetAssertionSecretReference(source, c.App.Env); err != nil {
		return err
	}
	return validateAeroIMTargetAssertionTrust(source, c.App.Env)
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
	endpoint, err := url.Parse(strings.TrimSpace(source.Endpoint))
	if err != nil || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawPath != "" {
		return fmt.Errorf("sources.aero_im endpoint must not contain a base path when target assertions are enabled")
	}
	return nil
}

func validateAeroIMTargetAssertionKeyReferences(source SourceEndpointConfig) error {
	if source.TargetAssertionKeyEnv != "" &&
		(source.TargetAssertionKeyEnv != strings.TrimSpace(source.TargetAssertionKeyEnv) ||
			!validEnvironmentName(source.TargetAssertionKeyEnv)) {
		return fmt.Errorf("sources.aero_im target assertion key environment reference is invalid")
	}
	file := strings.TrimSpace(source.TargetAssertionKeyFile)
	if file != "" && (source.TargetAssertionKeyFile != file || !validTargetAssertionConfigText(source.TargetAssertionKeyFile) ||
		!filepath.IsAbs(file) || filepath.Clean(file) != file) {
		return fmt.Errorf("sources.aero_im target assertion key file must be an absolute safe path")
	}
	return validateAeroIMTargetAssertionJWKSURL(source.TargetAssertionJWKSURL)
}

func validateAeroIMTargetAssertionJWKSURL(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" || validAeroIMTargetAssertionJWKSURL(value) {
		return nil
	}
	return fmt.Errorf("sources.aero_im target assertion JWKS URL must be HTTPS without userinfo, query, or fragment")
}

func validAeroIMTargetAssertionJWKSURL(value string) bool {
	if strings.IndexAny(value, "?#") >= 0 {
		return false
	}
	parsed, err := sourceauth.ParseEndpoint(value)
	return err == nil && parsed.Scheme == "https"
}

func validateAeroIMTargetAssertionSecretReference(source SourceEndpointConfig, environment string) error {
	if !sourceauth.ProductionLikeEnvironment(environment) {
		return nil
	}
	if envName := strings.TrimSpace(source.TargetAssertionKeyEnv); envName != "" &&
		(!strings.HasPrefix(envName, "AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_") ||
			len(envName) == len("AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_")) {
		return fmt.Errorf("sources.aero_im target assertion key environment reference must use the dedicated secret namespace")
	}
	if file := strings.TrimSpace(source.TargetAssertionKeyFile); file != "" &&
		((!strings.HasPrefix(file, "/run/secrets/") && !strings.HasPrefix(file, "/var/run/secrets/")) || filepath.Clean(file) != file) {
		return fmt.Errorf("sources.aero_im target assertion key file must use a protected secret directory")
	}
	return nil
}

func validateAeroIMTargetAssertionTrust(source SourceEndpointConfig, environment string) error {
	issuer, err := parseAeroIMTrustURL(source.TargetAssertionIssuer)
	if err != nil {
		return fmt.Errorf("sources.aero_im target_assertion_issuer must be an HTTPS trust-domain URL")
	}
	trustValue := strings.TrimSpace(source.TargetAssertionTrustDomain)
	if trustValue == "" {
		if strings.TrimSpace(environment) != "" && sourceauth.ProductionLikeEnvironment(environment) {
			return fmt.Errorf("sources.aero_im target assertion trust domain is required in production-like environments")
		}
		return nil
	}
	trust, err := parseAeroIMTrustURL(trustValue)
	if err != nil || issuer.String() != trust.String() {
		return fmt.Errorf("sources.aero_im target assertion issuer must match the configured trust domain")
	}
	if jwks := strings.TrimSpace(source.TargetAssertionJWKSURL); jwks != "" {
		jwksURL, parseErr := parseAeroIMTrustURL(jwks)
		if parseErr != nil || !sameAeroIMTrustOrigin(trust, jwksURL) {
			return fmt.Errorf("sources.aero_im target assertion JWKS URL must use the configured trust domain")
		}
	}
	return nil
}

func parseAeroIMTrustURL(value string) (*url.URL, error) {
	parsed, err := sourceauth.ParseEndpoint(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid trust URL")
	}
	return parsed, nil
}

func sameAeroIMTrustOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || left.Scheme != right.Scheme ||
		!strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	return effectiveAeroIMPort(left) == effectiveAeroIMPort(right)
}

func effectiveAeroIMPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	return "443"
}

func validTargetAssertionConfigText(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 512 && strings.IndexFunc(value, func(character rune) bool {
		return character == ' ' || character == '\t' || character == '\r' || character == '\n' || character == '\x00' || character == '\x7f'
	}) == -1
}

func validEnvironmentName(value string) bool {
	return sourceauth.ValidSecretEnvironmentName(value)
}
