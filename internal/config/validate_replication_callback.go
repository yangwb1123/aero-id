package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	maxCallbackRequestTimeout    = 5 * time.Minute
	maxCallbackResponseBytes     = int64(16 << 20)
	maxCallbackAttempts          = 10
	maxCallbackRetryBase         = time.Minute
	maxCallbackWorkerConcurrency = 64
)

func validateCallback(value CallbackConfig) error {
	if err := validateCallbackNonNegative(value); err != nil {
		return err
	}
	return validateCallbackCaps(value)
}

func validateCallbackNonNegative(value CallbackConfig) error {
	if value.RequestTimeout < 0 || value.MaxResponseBytes < 0 || value.MaxAttempts < 0 ||
		value.RetryBase < 0 || value.WorkerConcurrency < 0 {
		return fmt.Errorf("callback timeout, response, retry, and concurrency values must not be negative")
	}
	return nil
}

func validateCallbackCaps(value CallbackConfig) error {
	caps := []struct {
		bad  bool
		text string
	}{
		{value.RequestTimeout > maxCallbackRequestTimeout,
			fmt.Sprintf("callback.request_timeout must not exceed %s", maxCallbackRequestTimeout)},
		{value.MaxResponseBytes > maxCallbackResponseBytes,
			fmt.Sprintf("callback.max_response_bytes must not exceed %d", maxCallbackResponseBytes)},
		{value.MaxAttempts > maxCallbackAttempts,
			fmt.Sprintf("callback.max_attempts must not exceed %d", maxCallbackAttempts)},
		{value.RetryBase > maxCallbackRetryBase,
			fmt.Sprintf("callback.retry_base must not exceed %s", maxCallbackRetryBase)},
		{value.WorkerConcurrency > maxCallbackWorkerConcurrency,
			fmt.Sprintf("callback.worker_concurrency must not exceed %d", maxCallbackWorkerConcurrency)},
	}
	for _, cap := range caps {
		if cap.bad {
			return fmt.Errorf("%s", cap.text)
		}
	}
	return nil
}

func validateReplication(value ReplicationConfig) error {
	if value.MaxTerminalAttempts < 0 || value.MaxTerminalAttempts > MaxReplicationTerminalAttempts {
		return fmt.Errorf("replication.max_terminal_attempts must be between 0 and %d", MaxReplicationTerminalAttempts)
	}
	if !value.Enabled {
		return nil
	}
	if err := validateReplicationTransport(value); err != nil {
		return err
	}
	return validateReplicationLimits(value)
}

func validateReplicationTransport(value ReplicationConfig) error {
	switch value.Transport {
	case "nats":
		return validateNATSReplicationTransport(value)
	case "http":
		return validateHTTPReplicationTransport(value)
	default:
		return fmt.Errorf("replication.transport must be nats or http")
	}
}

func validateNATSReplicationTransport(value ReplicationConfig) error {
	if value.NATSSubject == "" {
		return fmt.Errorf("replication.nats_subject is required when transport=nats")
	}
	return nil
}

func validateHTTPReplicationTransport(value ReplicationConfig) error {
	if value.HTTPEndpoint == "" || value.HTTPKey == "" || value.HTTPBearerToken == "" {
		return fmt.Errorf("replication.http_endpoint, http_key and http_bearer_token are required when transport=http")
	}
	if err := validateReplicationEndpoint(value.HTTPEndpoint); err != nil {
		return err
	}
	if len(strings.TrimSpace(value.HTTPKey)) < 32 || value.HTTPKey != strings.TrimSpace(value.HTTPKey) {
		return fmt.Errorf("replication.http_key must be at least 32 bytes and must not be padded")
	}
	if !validBearerToken(value.HTTPBearerToken) {
		return fmt.Errorf("replication.http_bearer_token must be a non-empty token without control characters")
	}
	return nil
}

func validateReplicationEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("replication.http_endpoint must be a valid URL without userinfo, query, or fragment")
	}
	if parsed.Scheme != "https" && !isLocalEndpoint(parsed.Hostname()) {
		return fmt.Errorf("replication.http_endpoint must use https outside localhost")
	}
	return nil
}

func validBearerToken(value string) bool {
	trimmed := strings.TrimSpace(value)
	return value == trimmed && trimmed != "" && len(trimmed) <= 16<<10 && !containsControl(trimmed)
}

func validateReplicationLimits(value ReplicationConfig) error {
	if value.Interval <= 0 || value.BatchSize < 0 {
		return fmt.Errorf("replication interval must be positive and batch_size must not be negative")
	}
	if value.Lease < 0 {
		return fmt.Errorf("replication.lease must not be negative")
	}
	return nil
}

func validateBindingClientID(clientID string) error {
	normalized := strings.TrimSpace(clientID)
	if normalized == "" || normalized != clientID {
		return fmt.Errorf("event_ingest.bindings contains a non-canonical client id %q", clientID)
	}
	return nil
}

func validateBindingTenant(clientID string, binding EventSourceBindingConfig) error {
	if binding.TenantID != strings.TrimSpace(binding.TenantID) {
		return fmt.Errorf("event_ingest.bindings.%s.tenant_id must be canonical", clientID)
	}
	return nil
}

func validateBindingSource(clientID string, binding EventSourceBindingConfig) error {
	if binding.Source != "snaplink" && binding.Source != "aero-im" && binding.Source != "aero-vault" && binding.Source != "aero-id" {
		return fmt.Errorf("event_ingest.bindings.%s has unsupported source", clientID)
	}
	if binding.TenantID == "*" && binding.Source != "aero-id" {
		return fmt.Errorf("event_ingest.bindings.%s wildcard tenant requires aero-id source", clientID)
	}
	return nil
}

func validateBindingRegion(clientID string, binding EventSourceBindingConfig) error {
	region := strings.TrimSpace(binding.SourceRegion)
	if region == "" || region != binding.SourceRegion {
		return fmt.Errorf("event_ingest.bindings.%s.source_region must be canonical and non-blank", clientID)
	}
	return nil
}
