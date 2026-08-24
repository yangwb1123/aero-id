package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

const maxSSOUserBatchSize = 2048

func (c *Config) validate() error {
	c.applyDefaults()
	if err := c.validateRequired(); err != nil {
		return err
	}
	if err := c.validateEnvironmentSecurity(); err != nil {
		return err
	}
	if err := c.validateSourcesAndRegions(); err != nil {
		return err
	}
	if err := c.validateRuntimeLimits(); err != nil {
		return err
	}
	return c.validateTransport()
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.Account.Issuer) == "" {
		c.Account.Issuer = "snaplink"
	}
	if strings.TrimSpace(c.Account.Region) == "" {
		c.Account.Region = "local"
	}
	if len(c.Account.Regions) == 0 {
		c.Account.Regions = []string{c.Account.Region}
	}
	if strings.TrimSpace(c.Account.HomeRegionVersion) == "" {
		c.Account.HomeRegionVersion = "v1"
	}
	if strings.TrimSpace(c.Sources.Snaplink.Region) == "" {
		c.Sources.Snaplink.Region = c.Account.Region
	}
	if strings.TrimSpace(c.Sources.AeroIM.Region) == "" {
		c.Sources.AeroIM.Region = c.Account.Region
	}
	if strings.TrimSpace(c.Sources.AeroVault.Region) == "" {
		c.Sources.AeroVault.Region = c.Account.Region
	}
	if strings.TrimSpace(c.AuditGovernance.AnchorScheme) == "" {
		c.AuditGovernance.AnchorScheme = AnchorSchemeUUIDv5NulSeparated
	}
	if c.AuditGovernance.AnchorSchemeVersion <= 0 {
		c.AuditGovernance.AnchorSchemeVersion = AnchorSchemeVersionDefault
	}
}

func (c *Config) validateRequired() error {
	if c.SigningKey == "" {
		return fmt.Errorf("signing_key is required")
	}
	if err := rejectPlaceholderSecret(c.SigningKey, "signing_key"); err != nil {
		return err
	}
	if c.Database.DBName == "" {
		return fmt.Errorf("database.dbname is required")
	}
	if err := c.validateSSO(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateSSO() error {
	if c.SSO.Endpoint == "" {
		return fmt.Errorf("sso.endpoint is required")
	}
	if c.SSO.JWKSURL != "" && (strings.TrimSpace(c.SSO.Issuer) == "" || strings.TrimSpace(c.SSO.Audience) == "") {
		return fmt.Errorf("sso.issuer and sso.audience are required when sso.jwks_url is configured")
	}
	if c.SSO.MaxConcurrent < 0 {
		return fmt.Errorf("sso.max_concurrent must not be negative (0 = no cap)")
	}
	if c.SSO.UserBatchSize < 0 || c.SSO.UserBatchSize > maxSSOUserBatchSize {
		return fmt.Errorf("sso.user_batch_size must be between 0 and %d", maxSSOUserBatchSize)
	}
	return nil
}

func (c *Config) validateSourcesAndRegions() error {
	regions, err := validatedRegions(c.Account.Regions, c.Account.Region)
	if err != nil {
		return err
	}
	if err := c.validateConfiguredSources(regions); err != nil {
		return err
	}
	return validateBindingRegions(c.EventIngest.Bindings, regions)
}

func validatedRegions(values []string, primary string) (map[string]struct{}, error) {
	regions := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("account.regions must not contain blank values")
		}
		if _, exists := regions[value]; exists {
			return nil, fmt.Errorf("account.regions contains duplicate region %q", value)
		}
		regions[value] = struct{}{}
	}
	if _, found := regions[primary]; !found {
		return nil, fmt.Errorf("account.region must be present in account.regions")
	}
	return regions, nil
}

func (c *Config) validateConfiguredSources(regions map[string]struct{}) error {
	for name, source := range map[string]SourceEndpointConfig{
		"snaplink": c.Sources.Snaplink, "aero-im": c.Sources.AeroIM, "aero-vault": c.Sources.AeroVault,
	} {
		if err := validateSourceEndpoint(name, source); err != nil {
			return err
		}
		if name == "aero-im" {
			if err := c.validateAeroIMSource(source); err != nil {
				return err
			}
		}
		if !regionConfigured(source.Region, regions) {
			return fmt.Errorf("sources.%s.region must be listed in account.regions", name)
		}
	}
	return nil
}

func validateBindingRegions(bindings map[string]EventSourceBindingConfig, regions map[string]struct{}) error {
	for clientID, binding := range bindings {
		if !regionConfigured(binding.SourceRegion, regions) {
			return fmt.Errorf("event_ingest.bindings.%s.source_region must be listed in account.regions", clientID)
		}
	}
	return nil
}

func regionConfigured(value string, regions map[string]struct{}) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	_, exists := regions[value]
	return exists
}

func (c *Config) validateRuntimeLimits() error {
	checks := []error{
		validateCORSOrigins(c.Server.HTTP.CORSOrigins),
		validateStorage(c.Storage), validateRateLimit(c.RateLimit), validateStorageGC(c.Storage.GC),
		validateExport(c.Export), validateWebhook(c.Outbox.Webhook), validateEventIngest(c.EventIngest),
		validateEventBus(c.EventBus), validateReplication(c.Replication), validateAuditLedger(c.AuditLedger),
		validateDataRetention(c.DataRetention), validateAuditGovernance(c.AuditGovernance), validateCallback(c.Callback),
		c.validateCommercialIntegrations(),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

func validateRateLimit(value RateLimitConfig) error {
	if value.Enabled && (value.RPS <= 0 || value.Burst <= 0) {
		return fmt.Errorf("rate_limit.rps and rate_limit.burst must be > 0 when enabled")
	}
	return nil
}

func validateStorageGC(value StorageGCConfig) error {
	if value.Enabled && (value.Interval <= 0 || value.Retention <= 0) {
		return fmt.Errorf("storage.gc.interval and storage.gc.retention must be > 0 when enabled")
	}
	if value.PurgePaused && !value.Enabled {
		return fmt.Errorf("storage.gc.purge_paused requires storage.gc.enabled")
	}
	return nil
}

func validateExport(value ExportConfig) error {
	if value.MaxBytes < 0 || value.ArtifactTTL < 0 {
		return fmt.Errorf("export limits must not be negative")
	}
	return nil
}

func validateWebhook(value WebhookConfig) error {
	if value.URL == "" || value.Timeout == "" {
		return nil
	}
	duration, err := time.ParseDuration(value.Timeout)
	if err != nil || duration <= 0 {
		return fmt.Errorf("outbox.webhook.timeout must be a positive duration")
	}
	return nil
}

func validateEventIngest(value EventIngestConfig) error {
	if err := validateEventSourceKeys(value.SourceKeys); err != nil {
		return err
	}
	if value.SigningKey != "" && len(strings.TrimSpace(value.SigningKey)) < 32 {
		return fmt.Errorf("event_ingest.signing_key must be at least 32 bytes when configured")
	}
	if err := validateEventBindings(value.Bindings); err != nil {
		return err
	}
	if value.MaxBodyBytes < 0 || value.ClockSkew < 0 {
		return fmt.Errorf("event_ingest max_body_bytes and clock_skew must not be negative")
	}
	return nil
}

func validateAuditLedger(value AuditLedgerConfig) error {
	key := strings.TrimSpace(value.SigningKey)
	if key == "" {
		return fmt.Errorf("audit_ledger.signing_key is required")
	}
	if err := rejectPlaceholderSecret(key, "audit_ledger.signing_key"); err != nil {
		return err
	}
	if len(key) < 32 {
		return fmt.Errorf("audit_ledger.signing_key must be at least 32 bytes")
	}
	return nil
}

// validateAuditGovernance rejects a negative dead-letter retention bound; a
// negative bound would put the purge cutoff in the future and delete every
// dead-letter row on the next drain tick. purge_pause is inert when the
// governance relay is disabled (the drain loop does not run).
//
// B2-6 activation gate (F-03): enabling the relay without any tenant
// binding is a boot failure — an enabled relay with zero bindings would
// silently drop every publication (no tenant is captured), which is the
// exact silent-loss class the outbox contract forbids. The gate runs at
// Load() so env-enable (AERO_ID_AUDIT_GOVERNANCE_ENABLED=true) cannot
// bypass it; disabling the relay (the kill switch) accepts empty bindings.
func validateAuditGovernance(value AuditGovernanceConfig) error {
	if value.DeadLetterRetention < 0 {
		return fmt.Errorf("audit_governance.dead_letter_retention must not be negative")
	}
	if value.Enabled && len(value.Bindings) == 0 {
		return fmt.Errorf("audit_governance.enabled requires at least one tenant binding (activation gate)")
	}
	if err := validateAuditAnchor(value); err != nil {
		return err
	}
	return nil
}

// AnchorSchemeUUIDv5NulSeparated is the compiled-in deterministic event_id
// derivation contract: idgen.Anchor produces an RFC-4122 v5 UUID over
// NUL-separated identity parts in the fixed AnchorNamespace. The name is the
// single accepted value of audit_governance.anchor_scheme; the parity test
// pins it to the idgen implementation.
const AnchorSchemeUUIDv5NulSeparated = "uuidv5-nul-separated"

// AnchorSchemeVersionDefault is the only existing derivation version.
const AnchorSchemeVersionDefault = 1

// validateAuditAnchor rejects a config that contradicts the compiled-in
// event_id derivation. Enabled-independent: the ledger dedup guard
// (checkAuditDuplicate) runs on every append regardless of whether the
// governance relay is enabled, so a disabled relay must not silently accept
// a derivation contradiction. Empty/zero values mean the defaults (the
// AGENTS.md zero-value convention). Errors never echo the raw value.
func validateAuditAnchor(value AuditGovernanceConfig) error {
	scheme := strings.TrimSpace(value.AnchorScheme)
	if scheme != "" && scheme != AnchorSchemeUUIDv5NulSeparated {
		return fmt.Errorf("audit_governance.anchor_scheme must be %q", AnchorSchemeUUIDv5NulSeparated)
	}
	if value.AnchorSchemeVersion != 0 && value.AnchorSchemeVersion != AnchorSchemeVersionDefault {
		return fmt.Errorf("audit_governance.anchor_scheme_version must be %d", AnchorSchemeVersionDefault)
	}
	return nil
}

// validateDataRetention guards the outbox retention purge: a paused cleaner
// must actually be wired (both interval and dead-letter retention positive,
// matching the composition-root gate in app/new.go) and negative retentions
// would delete every eligible row on the next tick.
func validateDataRetention(value DataRetentionConfig) error {
	if value.PurgePaused && value.OutboxCleanInterval <= 0 {
		return fmt.Errorf("data_retention.purge_paused requires data_retention.outbox_clean_interval > 0")
	}
	if value.PurgePaused && value.OutboxDeadLetterRetention <= 0 {
		return fmt.Errorf("data_retention.purge_paused requires data_retention.outbox_dead_letter_retention > 0")
	}
	if value.OutboxDeadLetterRetention < 0 {
		return fmt.Errorf("data_retention.outbox_dead_letter_retention must not be negative")
	}
	if value.OutboxPublishedRetention < 0 {
		return fmt.Errorf("data_retention.outbox_published_retention must not be negative")
	}
	return nil
}

func validateEventBus(value EventBusConfig) error {
	if !value.NATS.Enabled {
		return nil
	}
	if value.NATS.URL == "" || value.NATS.Stream == "" || value.NATS.Subject == "" ||
		value.NATS.DeadLetterSubject == "" || value.NATS.Durable == "" {
		return fmt.Errorf("event_bus.nats url, stream, subject, dead_letter_subject and durable are required when enabled")
	}
	if value.NATS.Subject == value.NATS.DeadLetterSubject {
		return fmt.Errorf("event_bus.nats dead_letter_subject must differ from subject")
	}
	if value.NATS.MaxRedeliveries < 0 || value.NATS.AckWait < 0 {
		return fmt.Errorf("event_bus.nats max_redeliveries and ack_wait must not be negative")
	}
	return nil
}

func validateEventSourceKeys(keys map[string]string) error {
	for source, key := range keys {
		if source != "snaplink" && source != "aero-im" && source != "aero-vault" && source != "aero-id" {
			return fmt.Errorf("event_ingest.source_keys contains unsupported source %q", source)
		}
		if len(strings.TrimSpace(key)) < 32 {
			return fmt.Errorf("event_ingest.source_keys.%s must be at least 32 bytes", source)
		}
	}
	return nil
}

func validateEventBindings(bindings map[string]EventSourceBindingConfig) error {
	for clientID, binding := range bindings {
		if err := validateEventBinding(clientID, binding); err != nil {
			return err
		}
	}
	return nil
}

func validateEventBinding(clientID string, binding EventSourceBindingConfig) error {
	checks := []func() error{
		func() error { return validateBindingClientID(clientID) },
		func() error { return validateBindingTenant(clientID, binding) },
		func() error { return validateBindingSource(clientID, binding) },
		func() error { return validateBindingRegion(clientID, binding) },
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateTransport() error {
	if value := c.Server.GRPC.KeepaliveMinPingInterval; value > 0 && value < time.Second {
		return fmt.Errorf("server.grpc.keepalive_min_ping_interval must be >= 1s")
	}
	if value := c.Server.HTTP.ReadHeaderTimeout; value > 0 && value < time.Second {
		return fmt.Errorf("server.http.read_header_timeout must be >= 1s when set")
	}
	if value := c.Server.GRPC.MaxRecvMsgSize; value > 0 && value < 1024 {
		return fmt.Errorf("server.grpc.max_recv_msg_size must be >= 1KB when set")
	}
	if value := c.Server.GRPC.MaxSendMsgSize; value > 0 && value < 1024 {
		return fmt.Errorf("server.grpc.max_send_msg_size must be >= 1KB when set")
	}
	return nil
}

func validateSourceEndpoint(name string, source SourceEndpointConfig) error {
	if strings.TrimSpace(source.Endpoint) == "" {
		return nil
	}
	if err := validateSourceURL(name, source.Endpoint); err != nil {
		return err
	}
	if !validSourceLimits(source) {
		return fmt.Errorf("sources.%s timeout, limits, and retry values must not be negative", name)
	}
	if (source.TLSCertFile == "") != (source.TLSKeyFile == "") {
		return fmt.Errorf("sources.%s mTLS requires both tls_cert_file and tls_key_file", name)
	}
	if err := validateSourceOAuth(name, source); err != nil {
		return err
	}
	if err := validateSourcePaths(name, source); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateAeroIMSource(source SourceEndpointConfig) error {
	_, err := sourceauth.Evaluate(sourceauth.Config{
		Source: "aero-im", Label: "sources.aero-im", Endpoint: source.Endpoint, Environment: c.App.Env,
		AllowInsecureLoopback: source.AllowInsecureLoopback, BearerToken: source.BearerToken,
		OAuth: sourceauth.OAuthConfig{TokenURL: source.OAuthTokenURL, ClientID: source.OAuthClientID,
			ClientSecret: source.OAuthSecret, Scope: source.OAuthScope, Resource: source.OAuthResource},
		TLSCertFile: source.TLSCertFile, TLSKeyFile: source.TLSKeyFile,
	})
	return err
}

func validateSourceOAuth(name string, source SourceEndpointConfig) error {
	scope := strings.Join(strings.Fields(source.OAuthScope), " ")
	resource := strings.TrimSpace(source.OAuthResource)
	values := []string{source.OAuthTokenURL, source.OAuthClientID, source.OAuthSecret, scope, resource}
	configured := false
	complete := true
	for _, value := range values {
		configured = configured || strings.TrimSpace(value) != ""
		complete = complete && strings.TrimSpace(value) != ""
	}
	if configured && !complete {
		return fmt.Errorf("sources.%s OAuth token URL, client ID, client secret, scope, and resource are required together", name)
	}
	if configured && strings.TrimSpace(source.BearerToken) != "" {
		return fmt.Errorf("sources.%s must configure either bearer_token or OAuth client credentials", name)
	}
	if configured {
		if err := validateSourceURL(name, source.OAuthTokenURL); err != nil {
			return fmt.Errorf("sources.%s.oauth_token_url: %w", name, err)
		}
	}
	return nil
}

func validateSourceURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("sources.%s.endpoint must be a valid URL without userinfo", name)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("sources.%s.endpoint must use http or https", name)
	}
	if parsed.Scheme != "https" && !isLocalEndpoint(parsed.Hostname()) {
		return fmt.Errorf("sources.%s.endpoint must use https outside localhost", name)
	}
	return nil
}

func validSourceLimits(source SourceEndpointConfig) bool {
	return source.Timeout >= 0 && source.MaxBodyBytes >= 0 && source.MaxConcurrency >= 0 &&
		source.MaxAttempts >= 0 && source.RetryBackoff >= 0
}

func validateSourcePaths(name string, source SourceEndpointConfig) error {
	paths := map[string]string{
		"summary_path": source.SummaryPath, "erase_path": source.ErasePath, "status_path": source.StatusPath,
	}
	for field, value := range paths {
		if value != "" && (!strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\r\n")) {
			return fmt.Errorf("sources.%s.%s must be an absolute path without control characters", name, field)
		}
	}
	return nil
}

func isLocalEndpoint(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
