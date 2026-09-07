package config

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/connector/snaplink"
	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

func validateSourceEndpoint(name string, source SourceEndpointConfig) error {
	if err := validateNonSnaplinkCapabilities(name, source); err != nil {
		return err
	}
	if name == "snaplink" {
		return validateSnaplinkEndpoint(source)
	}
	return validateOtherSourceEndpoint(name, source)
}

func validateSnaplinkEndpoint(source SourceEndpointConfig) error {
	// Path shape remains a local configuration error even for a disabled
	// endpoint. Capability relationships are checked before authentication and
	// before any runtime client can be constructed.
	if err := validateSourcePaths("snaplink", source); err != nil {
		return err
	}
	if err := validateSnaplinkCapabilities(source); err != nil {
		return err
	}
	if strings.TrimSpace(source.Endpoint) == "" {
		return nil
	}
	return validateSourceEndpointBasics("snaplink", source)
}

func validateOtherSourceEndpoint(name string, source SourceEndpointConfig) error {
	if strings.TrimSpace(source.Endpoint) == "" {
		return nil
	}
	if err := validateSourceEndpointBasics(name, source); err != nil {
		return err
	}
	if name != "aero-vault" && strings.TrimSpace(source.OAuthTenantID) != "" {
		return fmt.Errorf("sources.%s.oauth_tenant_id is only valid for aero-vault", name)
	}
	if name == "aero-vault" {
		return validateVaultCapabilities(source)
	}
	if name == "aero-im" {
		return validateAeroIMTargetAssertion(source)
	}
	return nil
}

func validateNonSnaplinkCapabilities(name string, source SourceEndpointConfig) error {
	if name == "snaplink" {
		return nil
	}
	if source.Capabilities.Summary {
		return fmt.Errorf("sources.%s.capabilities.summary is only valid for sources.snaplink", name)
	}
	if strings.TrimSpace(source.Capabilities.EraseContractVersion) != "" {
		return fmt.Errorf("sources.%s.capabilities.erase_contract_version is only valid for sources.snaplink", name)
	}
	return nil
}

func validateSnaplinkCapabilities(source SourceEndpointConfig) error {
	return snaplink.ValidateCapabilities(connector.HTTPConfig{Endpoint: source.Endpoint, SummaryPath: source.SummaryPath,
		ErasePath: source.ErasePath, OperationStatusPath: source.StatusPath}, snaplink.Capabilities{Summary: source.Capabilities.Summary,
		Erase: source.Capabilities.Erase, OperationStatus: source.Capabilities.OperationStatus,
		EraseContractVersion: source.Capabilities.EraseContractVersion})
}

func validateSourceEndpointBasics(name string, source SourceEndpointConfig) error {
	if err := validateSourceURL(name, source.Endpoint); err != nil {
		return err
	}
	if !validSourceLimits(source) {
		return fmt.Errorf("sources.%s timeout, limits, and retry values must not be negative", name)
	}
	if (source.TLSCertFile == "") != (source.TLSKeyFile == "") {
		return fmt.Errorf("sources.%s mTLS requires both tls_cert_file and tls_key_file", name)
	}
	if (source.OAuthTLSCertFile == "") != (source.OAuthTLSKeyFile == "") {
		return fmt.Errorf("sources.%s OAuth mTLS requires both oauth_tls_cert_file and oauth_tls_key_file", name)
	}
	if err := validateSourceOAuth(name, source); err != nil {
		return err
	}
	return validateSourcePaths(name, source)
}

func (c *Config) validateAuthenticatedSource(name string, source SourceEndpointConfig) error {
	_, err := sourceauth.Evaluate(sourceauth.Config{
		Source: name, Label: "sources." + name, Endpoint: source.Endpoint, Environment: c.App.Env,
		AllowInsecureLoopback: source.AllowInsecureLoopback, BearerToken: source.BearerToken,
		OAuth: sourceauth.OAuthConfig{TokenURL: source.OAuthTokenURL, ClientID: source.OAuthClientID,
			ClientSecret: source.OAuthSecret, Scope: source.OAuthScope, Resource: source.OAuthResource,
			AllowedHosts: source.OAuthAllowedHosts},
		TLSCertFile: source.TLSCertFile, TLSKeyFile: source.TLSKeyFile,
		OAuthTLSCertFile: source.OAuthTLSCertFile, OAuthTLSKeyFile: source.OAuthTLSKeyFile,
	})
	if err != nil {
		return err
	}
	if name == "aero-vault" && strings.TrimSpace(source.Endpoint) != "" &&
		!validOAuthPrincipalTenant(source.OAuthTenantID) {
		return fmt.Errorf("sources.aero-vault requires an explicit OAuth principal tenant")
	}
	return nil
}

func validateVaultCapabilities(source SourceEndpointConfig) error {
	if source.Capabilities.Erase != (strings.TrimSpace(source.ErasePath) != "") {
		if source.Capabilities.Erase {
			return fmt.Errorf("sources.aero-vault capabilities.erase requires erase_path")
		}
		return fmt.Errorf("sources.aero-vault erase_path requires capabilities.erase")
	}
	if source.Capabilities.OperationStatus != (strings.TrimSpace(source.StatusPath) != "") {
		if source.Capabilities.OperationStatus {
			return fmt.Errorf("sources.aero-vault capabilities.operation_status requires status_path")
		}
		return fmt.Errorf("sources.aero-vault status_path requires capabilities.operation_status")
	}
	return nil
}

func validateSourceOAuth(name string, source SourceEndpointConfig) error {
	configured, complete := sourceOAuthCredentialState(source)
	if configured && !complete {
		return fmt.Errorf("sources.%s OAuth token URL, client ID, client secret, scope, and resource are required together", name)
	}
	if configured && strings.TrimSpace(source.BearerToken) != "" {
		return fmt.Errorf("sources.%s must configure either bearer_token or OAuth client credentials", name)
	}
	if !configured {
		return nil
	}
	if err := sourceauth.ValidateOAuthTokenURLShape(source.OAuthTokenURL); err != nil {
		return fmt.Errorf("sources.%s.oauth_token_url is invalid", name)
	}
	if err := sourceauth.ValidateOAuthAllowedHosts(source.OAuthAllowedHosts); err != nil {
		return fmt.Errorf("sources.%s.oauth_allowed_hosts is invalid", name)
	}
	return nil
}

func sourceOAuthCredentialState(source SourceEndpointConfig) (configured, complete bool) {
	values := []string{source.OAuthTokenURL, source.OAuthClientID, source.OAuthSecret,
		strings.Join(strings.Fields(source.OAuthScope), " "), strings.TrimSpace(source.OAuthResource)}
	complete = true
	for _, value := range values {
		present := strings.TrimSpace(value) != ""
		configured = configured || present
		complete = complete && present
	}
	return configured, complete
}

func validateSourceURL(name, raw string) error {
	// Keep the source-specific error categories while using the strict shared
	// parser for the accepted shape. ParseEndpoint trims one outer whitespace
	// layer before retaining the canonical URL used by runtime construction.
	if _, err := sourceauth.ParseEndpoint(raw); err == nil {
		return nil
	}
	if err := sourceURLPolicyError(name, raw); err != nil {
		return err
	}
	return fmt.Errorf("sources.%s.endpoint must be a valid URL without userinfo, query, or fragment", name)
}

func sourceURLPolicyError(name, raw string) error {
	parsed, ok := sourceURLForMessage(raw)
	if !ok {
		return nil
	}
	if parsed.Scheme == "http" && !sourceauth.IsExactLoopbackHost(parsed.Hostname()) {
		return fmt.Errorf("sources.%s.endpoint must use https outside localhost", name)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("sources.%s.endpoint must use http or https", name)
	}
	return nil
}

func sourceURLForMessage(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !validSourceURLShapeForMessage(parsed, raw) {
		return nil, false
	}
	return parsed, true
}

func validSourceURLShapeForMessage(parsed *url.URL, raw string) bool {
	if parsed == nil || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	return !strings.Contains(strings.TrimSpace(raw), "#")
}

func validSourceLimits(source SourceEndpointConfig) bool {
	return source.Timeout >= 0 && source.MaxBodyBytes >= 0 && source.MaxConcurrency >= 0 &&
		source.QueryMaxConcurrency >= 0 && source.CommandMaxConcurrency >= 0 &&
		source.HealthMaxConcurrency >= 0 && source.MaxAttempts >= 0 && source.RetryBackoff >= 0
}

func validateSourcePaths(name string, source SourceEndpointConfig) error {
	paths := map[string]string{
		"summary_path": source.SummaryPath, "erase_path": source.ErasePath, "status_path": source.StatusPath,
	}
	for field, value := range paths {
		if value != "" && !sourceauth.ValidAbsolutePath(value) {
			return fmt.Errorf("sources.%s.%s must be an absolute path without control characters, query, or fragment", name, field)
		}
	}
	return nil
}

func isLocalEndpoint(host string) bool { return sourceauth.IsExactLoopbackHost(host) }

func validOAuthPrincipalTenant(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "*" && len(value) <= 512 &&
		strings.IndexFunc(value, func(character rune) bool {
			return unicode.IsControl(character) || unicode.IsSpace(character)
		}) < 0
}
