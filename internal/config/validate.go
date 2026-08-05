package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

func (c *Config) validate() error {
	c.applyDefaults()
	if err := c.validateRequired(); err != nil {
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
}

func (c *Config) validateRequired() error {
	if c.SigningKey == "" {
		return fmt.Errorf("signing_key is required")
	}
	if c.Database.DBName == "" {
		return fmt.Errorf("database.dbname is required")
	}
	if c.SSO.Endpoint == "" {
		return fmt.Errorf("sso.endpoint is required")
	}
	if c.SSO.JWKSURL != "" && (strings.TrimSpace(c.SSO.Issuer) == "" || strings.TrimSpace(c.SSO.Audience) == "") {
		return fmt.Errorf("sso.issuer and sso.audience are required when sso.jwks_url is configured")
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
		validateStorage(c.Storage), validateRateLimit(c.RateLimit), validateStorageGC(c.Storage.GC),
		validateExport(c.Export), validateWebhook(c.Outbox.Webhook), validateEventIngest(c.EventIngest),
		validateEventBus(c.EventBus), validateReplication(c.Replication),
		c.validateCommercialIntegrations(),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

func validateStorage(value StorageConfig) error {
	if value.Type != "local" && value.Type != "s3" {
		return fmt.Errorf("storage.type must be 'local' or 's3'")
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

func validateEventBus(value EventBusConfig) error {
	if !value.NATS.Enabled {
		return nil
	}
	if value.NATS.URL == "" || value.NATS.Stream == "" || value.NATS.Subject == "" || value.NATS.Durable == "" {
		return fmt.Errorf("event_bus.nats url, stream, subject and durable are required when enabled")
	}
	if value.NATS.MaxRedeliveries < 0 || value.NATS.AckWait < 0 {
		return fmt.Errorf("event_bus.nats max_redeliveries and ack_wait must not be negative")
	}
	return nil
}

func validateReplication(value ReplicationConfig) error {
	if !value.Enabled {
		return nil
	}
	switch value.Transport {
	case "nats":
		if value.NATSSubject == "" {
			return fmt.Errorf("replication.nats_subject is required when transport=nats")
		}
	case "http":
		if value.HTTPEndpoint == "" || value.HTTPKey == "" {
			return fmt.Errorf("replication.http_endpoint and http_key are required when transport=http")
		}
	default:
		return fmt.Errorf("replication.transport must be nats or http")
	}
	if value.Interval <= 0 || value.BatchSize < 0 {
		return fmt.Errorf("replication interval must be positive and batch_size must not be negative")
	}
	return nil
}

func validateEventSourceKeys(keys map[string]string) error {
	for source, key := range keys {
		if source != "snaplink" && source != "aero-im" && source != "aero-vault" {
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
	normalizedClientID := strings.TrimSpace(clientID)
	if normalizedClientID == "" || normalizedClientID != clientID {
		return fmt.Errorf("event_ingest.bindings contains a non-canonical client id %q", clientID)
	}
	if binding.TenantID != strings.TrimSpace(binding.TenantID) {
		return fmt.Errorf("event_ingest.bindings.%s.tenant_id must be canonical", clientID)
	}
	if binding.Source != "snaplink" && binding.Source != "aero-im" && binding.Source != "aero-vault" {
		return fmt.Errorf("event_ingest.bindings.%s has unsupported source", clientID)
	}
	region := strings.TrimSpace(binding.SourceRegion)
	if region == "" || region != binding.SourceRegion {
		return fmt.Errorf("event_ingest.bindings.%s.source_region must be canonical and non-blank", clientID)
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
	return validateSourcePaths(name, source)
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
