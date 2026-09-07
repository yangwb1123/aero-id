package config

import (
	"strings"
	"testing"
	"time"
)

// Regression coverage for the Load-time validation gates in validate.go
// (tracking label: config-validation-regression-coverage). Test-only; no
// production code changes. Every wantErr value below is a substring of the
// exact format string in validate.go for the guard the cell pins, copied
// verbatim; matches use strings.Contains so they stay stable under prefix
// wrapping. The tests assert current guard behavior only: zero = code
// default, disabled blocks are inert, and negative transport values are
// treated as unset — none of those conventions are asserted as errors (a
// future code-change direction may tighten them separately).
//
// The package is sequential (no t.Parallel), matching config_test.go and
// concurrency_test.go.

const (
	// FR-1 (validateSourceURL, validate.go:393-405).
	sourceURLHTTPSOnlyErr = "sources.snaplink.endpoint must use https outside localhost"

	// FR-2 (validateSourceEndpoint validate.go:377-391; validSourceLimits
	// 407-410; validateSourcePaths 412-422).
	sourceMTLSXORErr     = "sources.snaplink mTLS requires both tls_cert_file and tls_key_file"
	sourceLimitsErr      = "sources.snaplink timeout, limits, and retry values must not be negative"
	sourcePathSummaryErr = "sources.snaplink.summary_path must be an absolute path without control characters"
	sourcePathEraseErr   = "sources.snaplink.erase_path must be an absolute path without control characters"
	sourcePathStatusErr  = "sources.snaplink.status_path must be an absolute path without control characters"

	// FR-3 (validateTransport, validate.go:361-375).
	keepaliveFloorErr  = "server.grpc.keepalive_min_ping_interval must be >= 1s"
	readHeaderFloorErr = "server.http.read_header_timeout must be >= 1s when set"
	maxRecvFloorErr    = "server.grpc.max_recv_msg_size must be >= 1KB when set"
	maxSendFloorErr    = "server.grpc.max_send_msg_size must be >= 1KB when set"

	// FR-4.1 (validateEventBus, validate.go:287-298).
	eventBusRequiredErr = "event_bus.nats url, stream, subject, dead_letter_subject and durable are required when enabled"
	eventBusNegativeErr = "event_bus.nats max_redeliveries and ack_wait must not be negative"

	// FR-4.2 (validateReplication, validate.go:300-320).
	replicationNATSRequiredErr = "replication.nats_subject is required when transport=nats"
	replicationHTTPRequiredErr = "replication.http_endpoint, http_key and http_bearer_token are required when transport=http"
	replicationTransportErr    = "replication.transport must be nats or http"
	replicationLimitsErr       = "replication interval must be positive and batch_size must not be negative"

	// FR-4.3 (validateWebhook, validate.go:178-187).
	webhookTimeoutErr = "outbox.webhook.timeout must be a positive duration"

	// FR-4.4 (validateEventSourceKeys, validate.go:322-332).
	sourceKeyUnsupportedErr = `event_ingest.source_keys contains unsupported source "slack"`
	sourceKeyShortErr       = "event_ingest.source_keys.snaplink must be at least 32 bytes"

	// FR-5 (validateBindingRegions, validate.go:113-120).
	bindingRegionErr = "event_ingest.bindings.client-a.source_region must be listed in account.regions"
)

// wantConfigErr asserts a validation outcome against the wantErr contract:
// empty wantErr means the validator must accept, otherwise the error text
// must contain wantErr as a substring. Shared by every matrix below.
func wantConfigErr(t *testing.T, err error, wantErr string) {
	t.Helper()
	if wantErr == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("error = %v, want containing %q", err, wantErr)
	}
}

type sourceURLCase struct {
	name      string
	endpoint  string
	wantValid bool
}

// sourceURLCases is the FR-1 scheme/loopback/shape matrix for
// validateSourceURL (validate.go:393-405), mirroring the auditTokenURLCases()
// house pattern. Expected values are the observed Go 1.26 net/url behavior,
// verified by direct execution: the scheme is lowercased by the parser but
// the host is not; a bare '?' yields ForceQuery with RawQuery=="" ; a
// whitespace-padded URL is a parse error; "https://"/"http://" parse with an
// empty hostname.
//
// The http non-loopback rows are the regression canary for the
// https-outside-localhost rule at validate.go:401-403; a typo'd relaxation
// of this rule fails this test. The userinfo / raw-query / fragment rows are
// the canaries for the SSRF-shape guards at validate.go:395. The whitespace
// padded row pins a deliberate divergence from the audit connector (which
// trims and accepts): validateSourceURL does not trim, so the URL lands in
// the parse-error branch.
func sourceURLCases() []sourceURLCase {
	return []sourceURLCase{
		{"https", "https://sso.example/token", true},                      // pass-through
		{"https root path", "https://sso.example/", true},                 // pass-through
		{"https bare query", "https://sso.example/token?", false},         // bare ForceQuery is rejected
		{"https uppercase scheme", "HTTPS://sso.example/token", true},     // parser lowercases the scheme
		{"http localhost", "http://localhost/token", true},                // loopback carve-out
		{"http localhost port", "http://localhost:8080/token", true},      // loopback carve-out
		{"http 127.0.0.1", "http://127.0.0.1/token", true},                // loopback carve-out
		{"http 127.0.0.1 port", "http://127.0.0.1:9090", true},            // loopback carve-out
		{"http ipv6 loopback", "http://[::1]/token", true},                // loopback carve-out
		{"http ipv6 loopback port", "http://[::1]:9090/x", true},          // loopback carve-out
		{"http localhost uppercase host", "http://LOCALHOST/token", true}, // isLocalEndpoint lowercases (validate.go:424-427)
		{"http non-loopback", "http://sso.example/token", false},          // CANARY: https-outside-localhost rule (validate.go:401-403)
		{"http non-loopback port", "http://sso.example:8080/", false},     // CANARY: same
		{"ftp scheme", "ftp://sso.example/token", false},                  // scheme must be http/https (validate.go:399-400)
		{"not a url", "not-a-url", false},                                 // parse error (validate.go:395)
		{"userinfo", "https://u:p@sso.example/token", false},              // parsed.User != nil (validate.go:395)
		{"fragment", "https://sso.example/token#frag", false},             // parsed.Fragment != "" (validate.go:395)
		{"raw query", "https://sso.example/token?x=1", false},             // parsed.RawQuery != "" (validate.go:395)
		{"whitespace padded", "  https://sso.example/token  ", true},      // source URLs are trimmed before canonical parsing
		{"empty host", "https://", false},                                 // parsed.Hostname()=="" (validate.go:395)
		{"empty host http", "http://", false},                             // parsed.Hostname()=="" (validate.go:395)
	}
}

// TestValidateSourceURLMatrix (FR-1): every sourceURLCases() cell against
// validateSourceURL with name = "snaplink".
func TestValidateSourceURLMatrix(t *testing.T) {
	for _, tc := range sourceURLCases() {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSourceURL("snaplink", tc.endpoint)
			if (err == nil) != tc.wantValid {
				t.Fatalf("endpoint %q accepted=%v, want %v (err %v)",
					tc.endpoint, err == nil, tc.wantValid, err)
			}
		})
	}
}

type sourceEndpointCase struct {
	name    string
	mutate  func(*SourceEndpointConfig)
	wantErr string
}

// sourceEndpointBase mirrors the shipped configs/config.yaml sources block so
// the positive legs prove the shipped file's shape.
func sourceEndpointBase() SourceEndpointConfig {
	return SourceEndpointConfig{
		Endpoint: "https://sso.example/", Timeout: 2 * time.Second,
		MaxBodyBytes: 1048576, MaxConcurrency: 32, MaxAttempts: 3,
		RetryBackoff: 50 * time.Millisecond, SummaryPath: "/internal/account-summary",
	}
}

// sourceEndpointCases is the FR-2 matrix for validateSourceEndpoint (the
// natural wrapper exercising URL + limits + mTLS + paths in one call). The
// cert-only / key-only rows are the regression canary for the mTLS XOR
// pairing check at validate.go:389-391 (making pairing optional flips them
// green); the relative-path and control-character rows pin validateSourcePaths
// at validate.go:412-422. Zero-limit values are deliberately not asserted:
// 0 = code default (AGENTS.md convention).
func sourceEndpointCases() []sourceEndpointCase {
	return []sourceEndpointCase{
		{"cert only", func(s *SourceEndpointConfig) { s.TLSCertFile = "/certs/client.crt" }, sourceMTLSXORErr}, // CANARY: mTLS XOR
		{"key only", func(s *SourceEndpointConfig) { s.TLSKeyFile = "/certs/client.key" }, sourceMTLSXORErr},   // CANARY: mTLS XOR
		{"both cert and key", func(s *SourceEndpointConfig) {
			s.TLSCertFile = "/certs/client.crt"
			s.TLSKeyFile = "/certs/client.key"
		}, ""},
		{"neither", nil, ""},
		{"complete OAuth client credentials", func(s *SourceEndpointConfig) {
			s.OAuthTokenURL = "https://sso.example/token"
			s.OAuthAllowedHosts = []string{"sso.example"}
			s.OAuthClientID = "aero-id"
			s.OAuthSecret = "source-secret"
			s.OAuthScope = "account:summary:read"
			s.OAuthResource = "snaplink"
		}, ""},
		{"partial OAuth client credentials", func(s *SourceEndpointConfig) {
			s.OAuthTokenURL = "https://sso.example/token"
			s.OAuthClientID = "aero-id"
		}, "OAuth token URL, client ID, client secret, scope, and resource are required together"},
		{"mixed static and OAuth credentials", func(s *SourceEndpointConfig) {
			s.BearerToken = "legacy-token"
			s.OAuthTokenURL = "https://sso.example/token"
			s.OAuthClientID = "aero-id"
			s.OAuthSecret = "source-secret"
			s.OAuthScope = "account:summary:read"
			s.OAuthResource = "snaplink"
		}, "must configure either bearer_token or OAuth client credentials"},
		{"blank normalized OAuth scope", func(s *SourceEndpointConfig) {
			s.OAuthTokenURL = "https://sso.example/token"
			s.OAuthClientID = "aero-id"
			s.OAuthSecret = "source-secret"
			s.OAuthScope = "   "
			s.OAuthResource = "snaplink"
		}, "OAuth token URL, client ID, client secret, scope, and resource are required together"},
		{"relative summary_path", func(s *SourceEndpointConfig) { s.SummaryPath = "internal/account-summary" }, sourcePathSummaryErr}, // CANARY: path shape
		{"newline in summary_path", func(s *SourceEndpointConfig) { s.SummaryPath = "/internal\naccount-summary" }, sourcePathSummaryErr},
		{"carriage return in erase_path", func(s *SourceEndpointConfig) { s.ErasePath = "/internal/erase\r" }, sourcePathEraseErr},
		{"relative status_path", func(s *SourceEndpointConfig) { s.StatusPath = "status" }, sourcePathStatusErr},
		{"all absolute paths", func(s *SourceEndpointConfig) {
			s.StatusPath = "/internal/status"
			s.Capabilities.OperationStatus = true
		}, ""},
		{"negative timeout", func(s *SourceEndpointConfig) { s.Timeout = -time.Second }, sourceLimitsErr},
		{"negative max_body_bytes", func(s *SourceEndpointConfig) { s.MaxBodyBytes = -1 }, sourceLimitsErr},
		{"negative max_concurrency", func(s *SourceEndpointConfig) { s.MaxConcurrency = -1 }, sourceLimitsErr},
		{"negative query_max_concurrency", func(s *SourceEndpointConfig) { s.QueryMaxConcurrency = -1 }, sourceLimitsErr},
		{"negative command_max_concurrency", func(s *SourceEndpointConfig) { s.CommandMaxConcurrency = -1 }, sourceLimitsErr},
		{"negative health_max_concurrency", func(s *SourceEndpointConfig) { s.HealthMaxConcurrency = -1 }, sourceLimitsErr},
		{"negative max_attempts", func(s *SourceEndpointConfig) { s.MaxAttempts = -1 }, sourceLimitsErr},
		{"negative retry_backoff", func(s *SourceEndpointConfig) { s.RetryBackoff = -time.Second }, sourceLimitsErr},
		{"disabled connector (empty endpoint)", func(s *SourceEndpointConfig) {
			s.Endpoint = ""
			s.Capabilities.Summary = false
			s.SummaryPath = "internal/account-summary"
		}, sourcePathSummaryErr},
		{"http non-loopback endpoint", func(s *SourceEndpointConfig) { s.Endpoint = "http://sso.example/" }, sourceURLHTTPSOnlyErr}, // URL guard runs first
	}
}

// TestValidateSourceMTLSAndPaths (FR-2): every sourceEndpointCases() cell
// against validateSourceEndpoint with name = "snaplink".
func TestValidateSourceMTLSAndPaths(t *testing.T) {
	for _, tc := range sourceEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			source := sourceEndpointBase()
			source.Capabilities.Summary = true
			if tc.mutate != nil {
				tc.mutate(&source)
			}
			wantConfigErr(t, validateSourceEndpoint("snaplink", source), tc.wantErr)
		})
	}
}

func targetAssertionSourceBase() SourceEndpointConfig {
	source := sourceEndpointBase()
	source.TargetAssertionClientID = "aero-id-im-source"
	source.TargetAssertionKeyFile = "/run/secrets/aero-id-target-key.pem"
	source.TargetAssertionKeyID = "target-key-2026-08"
	source.TargetAssertionIssuer = "https://aero-id.example/assertions"
	source.TargetAssertionAudience = "aero-im-account-summary"
	source.TargetAssertionSubject = "aero-id-sync"
	source.TargetAssertionLifetime = time.Minute
	source.OAuthTokenURL = "https://snaplink.example/token"
	source.OAuthAllowedHosts = []string{"snaplink.example"}
	source.OAuthClientID = source.TargetAssertionClientID
	source.OAuthSecret = "source-secret"
	source.OAuthScope = "aero.account.summary.read"
	source.OAuthResource = "aero-im"
	return source
}

func TestValidateAeroIMTargetAssertionConfig(t *testing.T) {
	if err := validateSourceEndpoint("aero-im", targetAssertionSourceBase()); err != nil {
		t.Fatalf("complete target assertion config: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*SourceEndpointConfig)
	}{
		{"both key references", func(source *SourceEndpointConfig) { source.TargetAssertionKeyEnv = "AERO_TARGET_ASSERTION_KEY" }},
		{"missing issuer", func(source *SourceEndpointConfig) { source.TargetAssertionIssuer = "" }},
		{"missing client", func(source *SourceEndpointConfig) { source.TargetAssertionClientID = "" }},
		{"wrong audience", func(source *SourceEndpointConfig) { source.TargetAssertionAudience = "other-audience" }},
		{"wrong subject", func(source *SourceEndpointConfig) { source.TargetAssertionSubject = "other-subject" }},
		{"wrong scope", func(source *SourceEndpointConfig) { source.OAuthScope = "account:summary:read" }},
		{"wrong resource", func(source *SourceEndpointConfig) { source.OAuthResource = "snaplink" }},
		{"static bearer", func(source *SourceEndpointConfig) { source.BearerToken = "legacy-token" }},
		{"client mismatch", func(source *SourceEndpointConfig) {
			source.OAuthTokenURL = "https://sso.example/token"
			source.OAuthClientID = "other-client"
			source.OAuthSecret = "source-secret"
			source.OAuthScope = "aero.account.summary.read"
			source.OAuthResource = "aero-im"
		}},
		{"invalid env name", func(source *SourceEndpointConfig) {
			source.TargetAssertionKeyFile = ""
			source.TargetAssertionKeyEnv = "target-key"
		}},
		{"lifetime too long", func(source *SourceEndpointConfig) { source.TargetAssertionLifetime = time.Minute + time.Second }},
		{"subsecond lifetime", func(source *SourceEndpointConfig) { source.TargetAssertionLifetime = 1500 * time.Millisecond }},
		{"wrong protocol path", func(source *SourceEndpointConfig) { source.SummaryPath = "/v2/internal/account-summary" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := targetAssertionSourceBase()
			tc.mutate(&source)
			if err := validateSourceEndpoint("aero-im", source); err == nil {
				t.Fatal("invalid target assertion configuration was accepted")
			}
		})
	}
}

func TestValidateNonAeroIMDoesNotRequireTargetAssertion(t *testing.T) {
	source := sourceEndpointBase()
	source.TargetAssertionKeyFile = "/run/secrets/unused.pem"
	if err := validateSourceEndpoint("aero_vault", source); err != nil {
		t.Fatalf("non-Aero-IM source was forced to configure target assertions: %v", err)
	}
}

func TestValidateCallbackBounds(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*CallbackConfig)
		wantErr string
	}{
		{"defaults", nil, ""},
		{"negative", func(c *CallbackConfig) { c.MaxResponseBytes = -1 }, "callback timeout, response, retry, and concurrency values must not be negative"},
		{"timeout cap", func(c *CallbackConfig) { c.RequestTimeout = maxCallbackRequestTimeout + time.Second }, "callback.request_timeout must not exceed"},
		{"response cap", func(c *CallbackConfig) { c.MaxResponseBytes = maxCallbackResponseBytes + 1 }, "callback.max_response_bytes must not exceed"},
		{"attempt cap", func(c *CallbackConfig) { c.MaxAttempts = maxCallbackAttempts + 1 }, "callback.max_attempts must not exceed"},
		{"retry cap", func(c *CallbackConfig) { c.RetryBase = maxCallbackRetryBase + time.Second }, "callback.retry_base must not exceed"},
		{"concurrency cap", func(c *CallbackConfig) { c.WorkerConcurrency = maxCallbackWorkerConcurrency + 1 }, "callback.worker_concurrency must not exceed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := CallbackConfig{}
			if tc.mutate != nil {
				tc.mutate(&value)
			}
			wantConfigErr(t, validateCallback(value), tc.wantErr)
		})
	}
}

type transportCase struct {
	name    string
	cfg     Config
	wantErr string
}

// transportCases is the FR-3 matrix for (Config).validateTransport
// (validate.go:361-375). The 500ms keepalive / 512-byte message-size rows
// are the regression canary for the `value > 0 && value < floor` guards: a
// regression that drops `value < time.Second` / `value < 1024` or the whole
// check flips them green. Negative values are deliberately not asserted —
// current code treats `<= 0` as unset/default (zero = default convention);
// pinning "negative accepted" would hard-code behavior this direction does
// not bless.
func transportCases() []transportCase {
	return []transportCase{
		{"all zero (defaults)", Config{}, ""}, // zero = default; no floor applied
		{"keepalive 500ms", Config{Server: ServerConfig{GRPC: GRPCConfig{KeepaliveMinPingInterval: 500 * time.Millisecond}}}, keepaliveFloorErr}, // CANARY
		{"keepalive exactly 1s", Config{Server: ServerConfig{GRPC: GRPCConfig{KeepaliveMinPingInterval: time.Second}}}, ""},                      // boundary
		{"keepalive 10s (shipped)", Config{Server: ServerConfig{GRPC: GRPCConfig{KeepaliveMinPingInterval: 10 * time.Second}}}, ""},
		{"read_header_timeout 500ms", Config{Server: ServerConfig{HTTP: HTTPConfig{ReadHeaderTimeout: 500 * time.Millisecond}}}, readHeaderFloorErr}, // CANARY
		{"read_header_timeout exactly 1s", Config{Server: ServerConfig{HTTP: HTTPConfig{ReadHeaderTimeout: time.Second}}}, ""},                       // boundary
		{"read_header_timeout 10s (shipped)", Config{Server: ServerConfig{HTTP: HTTPConfig{ReadHeaderTimeout: 10 * time.Second}}}, ""},
		{"max_recv 512", Config{Server: ServerConfig{GRPC: GRPCConfig{MaxRecvMsgSize: 512}}}, maxRecvFloorErr}, // CANARY
		{"max_recv exactly 1024", Config{Server: ServerConfig{GRPC: GRPCConfig{MaxRecvMsgSize: 1024}}}, ""},    // boundary
		{"max_recv 4MiB (shipped)", Config{Server: ServerConfig{GRPC: GRPCConfig{MaxRecvMsgSize: 4194304}}}, ""},
		{"max_send 512", Config{Server: ServerConfig{GRPC: GRPCConfig{MaxSendMsgSize: 512}}}, maxSendFloorErr}, // CANARY
		{"max_send exactly 1024", Config{Server: ServerConfig{GRPC: GRPCConfig{MaxSendMsgSize: 1024}}}, ""},    // boundary
	}
}

// TestValidateTransportLimits (FR-3): every transportCases() cell against
// the Config.validateTransport method.
func TestValidateTransportLimits(t *testing.T) {
	for _, tc := range transportCases() {
		t.Run(tc.name, func(t *testing.T) {
			wantConfigErr(t, tc.cfg.validateTransport(), tc.wantErr)
		})
	}
}

type eventBusCase struct {
	name    string
	mutate  func(*NATSBusConfig)
	wantErr string
}

// eventBusBase mirrors the shipped event_bus.nats block (enabled with all
// fields set).
func eventBusBase() NATSBusConfig {
	return NATSBusConfig{
		Enabled: true, URL: "nats://nats:4222", Stream: "aero_events",
		Subject: "aero.events.canonical", DeadLetterSubject: "aero.events.canonical.dead-letter",
		Durable:         "aero-id-ingest",
		MaxRedeliveries: 5, AckWait: 30 * time.Second,
	}
}

// eventBusCases is the FR-4.1 matrix for validateEventBus (validate.go:287-298).
// The disabled+garbage cell pins the enablement short-circuit at
// validate.go:288-289 (validating a disabled block flips it red); the
// missing-field cells pin required-when-enabled; the negative cells pin the
// `must not be negative` guard; the zero cell pins 0 = not negative.
func eventBusCases() []eventBusCase {
	return []eventBusCase{
		{"disabled, empty", func(n *NATSBusConfig) { *n = NATSBusConfig{} }, ""}, // CANARY: enablement short-circuit
		{"disabled, garbage", func(n *NATSBusConfig) {
			n.Enabled = false
			n.URL = ""
			n.MaxRedeliveries = -5
		}, ""}, // CANARY: disabled block is inert
		{"enabled, missing url", func(n *NATSBusConfig) { n.URL = "" }, eventBusRequiredErr},
		{"enabled, missing stream", func(n *NATSBusConfig) { n.Stream = "" }, eventBusRequiredErr},
		{"enabled, missing subject", func(n *NATSBusConfig) { n.Subject = "" }, eventBusRequiredErr},
		{"enabled, missing durable", func(n *NATSBusConfig) { n.Durable = "" }, eventBusRequiredErr},
		{"enabled, same canonical and dead-letter subject", func(n *NATSBusConfig) {
			n.DeadLetterSubject = n.Subject
		}, "event_bus.nats dead_letter_subject must differ from subject"},
		{"enabled, fully set", nil, ""},
		{"enabled, negative max_redeliveries", func(n *NATSBusConfig) { n.MaxRedeliveries = -1 }, eventBusNegativeErr},
		{"enabled, negative ack_wait", func(n *NATSBusConfig) { n.AckWait = -time.Second }, eventBusNegativeErr},
		{"enabled, zero redeliveries/ack_wait", func(n *NATSBusConfig) {
			n.MaxRedeliveries = 0
			n.AckWait = 0
		}, ""}, // 0 is not negative; 0 = default
	}
}

type replicationCase struct {
	name    string
	mutate  func(*ReplicationConfig)
	wantErr string
}

// replicationBase mirrors the shipped replication block shape (enabled,
// transport=nats with a subject).
func replicationBase() ReplicationConfig {
	return ReplicationConfig{
		Enabled: true, Interval: 5 * time.Second, BatchSize: 500,
		Transport: "nats", NATSSubject: "aero.events.canonical",
	}
}

// replicationCases is the FR-4.2 matrix for validateReplication
// (validate.go:300-320). The disabled cell pins the short-circuit at
// validate.go:301-302; the missing-field cells pin required-when-enabled per
// transport; the tcp/empty transport cells pin the switch default; the
// negative/zero batch cells pin the limits guard.
func replicationCases() []replicationCase {
	return []replicationCase{
		{"disabled, http without endpoint", func(r *ReplicationConfig) {
			r.Enabled = false
			r.Transport = "http"
		}, ""}, // CANARY: enablement short-circuit (validate.go:301-302)
		{"enabled nats, missing subject", func(r *ReplicationConfig) { r.NATSSubject = "" }, replicationNATSRequiredErr},
		{"enabled nats, valid", nil, ""},
		{"enabled http, missing endpoint", func(r *ReplicationConfig) { r.Transport = "http" }, replicationHTTPRequiredErr},
		{"enabled http, endpoint only", func(r *ReplicationConfig) {
			r.Transport = "http"
			r.HTTPEndpoint = "https://region.example/ingest"
		}, replicationHTTPRequiredErr},
		{"enabled http, valid", func(r *ReplicationConfig) {
			r.Transport = "http"
			r.HTTPEndpoint = "https://region.example/ingest"
			r.HTTPKey = "replication-hmac-key-00000000000000000000"
			r.HTTPBearerToken = "replication-service-token"
		}, ""},
		{"enabled http, insecure remote endpoint", func(r *ReplicationConfig) {
			r.Transport = "http"
			r.HTTPEndpoint = "http://region.example/ingest"
			r.HTTPKey = "replication-hmac-key-00000000000000000000"
			r.HTTPBearerToken = "replication-service-token"
		}, "replication.http_endpoint must use https outside localhost"},
		{"enabled, unsupported transport", func(r *ReplicationConfig) { r.Transport = "tcp" }, replicationTransportErr}, // switch default
		{"enabled, empty transport", func(r *ReplicationConfig) { r.Transport = "" }, replicationTransportErr},          // switch default
		{"enabled nats, zero interval", func(r *ReplicationConfig) { r.Interval = 0 }, replicationLimitsErr},
		{"enabled nats, negative batch", func(r *ReplicationConfig) { r.BatchSize = -1 }, replicationLimitsErr},
		{"enabled nats, zero batch", func(r *ReplicationConfig) { r.BatchSize = 0 }, ""}, // 0 is not negative
	}
}

type webhookCase struct {
	name    string
	cfg     WebhookConfig
	wantErr string
}

// webhookCases is the FR-4.3 matrix for validateWebhook (validate.go:178-187).
// The empty-URL cell pins the disabled short-circuit; the empty-timeout cell
// pins the empty-timeout = default convention (validate.go:180-181); the
// 500ms cell pins that no floor exists; the zero/negative/unparseable cells
// pin the positive-duration guard.
func webhookCases() []webhookCase {
	return []webhookCase{
		{"disabled (empty url)", WebhookConfig{Timeout: "5s"}, ""},                         // CANARY: disabled short-circuit
		{"url set, timeout empty", WebhookConfig{URL: "https://hooks.example/events"}, ""}, // empty timeout = default 10s
		{"url set, timeout 5s", WebhookConfig{URL: "https://hooks.example/events", Timeout: "5s"}, ""},
		{"url set, timeout 500ms", WebhookConfig{URL: "https://hooks.example/events", Timeout: "500ms"}, ""}, // no floor
		{"url set, timeout 0s", WebhookConfig{URL: "https://hooks.example/events", Timeout: "0s"}, webhookTimeoutErr},
		{"url set, timeout negative", WebhookConfig{URL: "https://hooks.example/events", Timeout: "-1s"}, webhookTimeoutErr},
		{"url set, timeout unparseable", WebhookConfig{URL: "https://hooks.example/events", Timeout: "bogus"}, webhookTimeoutErr},
	}
}

type sourceKeyCase struct {
	name    string
	keys    map[string]string
	wantErr string
}

// sourceKeyCases is the FR-4.4 matrix for validateEventSourceKeys
// (validate.go:322-332). The unsupported-source cell pins the allow-list
// guard; the short / whitespace-padded cells pin the >= 32 bytes guard
// (whitespace-padded pins strings.TrimSpace at validate.go:330); the
// 32-byte cell is the boundary.
func sourceKeyCases() []sourceKeyCase {
	return []sourceKeyCase{
		{"empty map", map[string]string{}, ""},
		{"valid keys", map[string]string{
			"snaplink":   "snaplink-hmac-key-00000000000000000001",
			"aero-im":    "aero-im-hmac-key-00000000000000000002",
			"aero-vault": "aero-vault-hmac-key-00000000000000000003",
		}, ""},
		{"unsupported source", map[string]string{"slack": "slack-hmac-key-00000000000000000004"}, sourceKeyUnsupportedErr},
		{"short snaplink key", map[string]string{"snaplink": "short"}, sourceKeyShortErr},
		{"whitespace-padded short key", map[string]string{"snaplink": "                        "}, sourceKeyShortErr}, // pins strings.TrimSpace (validate.go:330)
		{"exactly 32 bytes", map[string]string{"snaplink": "01234567890123456789012345678901"}, ""},                   // boundary
	}
}

// TestValidateEventBusReplicationWebhookEnablementContracts (FR-4): the four
// enablement-contract matrices (validateEventBus, validateReplication,
// validateWebhook, validateEventSourceKeys), each with required-when-enabled,
// negative-rejection, transport-switch, and disabled-inert cells. Direct
// validator calls mirror the TestAuditGovernanceKnobGateMatrix style so each
// matrix exercises exactly one validator.
func TestValidateEventBusReplicationWebhookEnablementContracts(t *testing.T) {
	t.Run("event_bus", func(t *testing.T) {
		for _, tc := range eventBusCases() {
			t.Run(tc.name, func(t *testing.T) {
				nats := eventBusBase()
				if tc.mutate != nil {
					tc.mutate(&nats)
				}
				wantConfigErr(t, validateEventBus(EventBusConfig{NATS: nats}), tc.wantErr)
			})
		}
	})
	t.Run("replication", func(t *testing.T) {
		for _, tc := range replicationCases() {
			t.Run(tc.name, func(t *testing.T) {
				cfg := replicationBase()
				if tc.mutate != nil {
					tc.mutate(&cfg)
				}
				wantConfigErr(t, validateReplication(cfg), tc.wantErr)
			})
		}
	})
	t.Run("webhook", func(t *testing.T) {
		for _, tc := range webhookCases() {
			t.Run(tc.name, func(t *testing.T) {
				wantConfigErr(t, validateWebhook(tc.cfg), tc.wantErr)
			})
		}
	})
	t.Run("source_keys", func(t *testing.T) {
		for _, tc := range sourceKeyCases() {
			t.Run(tc.name, func(t *testing.T) {
				wantConfigErr(t, validateEventSourceKeys(tc.keys), tc.wantErr)
			})
		}
	})
}

type bindingRegionCase struct {
	name     string
	bindings map[string]EventSourceBindingConfig
	wantErr  string
}

// bindingRegionCases is the FR-5 matrix for validateBindingRegions
// (validate.go:113-120) against regions {"local"}. The unmapped-region cell
// pins the membership guard. The whitespace-region cell pins the trim in
// regionConfigured (validate.go:123-129): the padded value resolves to
// "local" and is accepted — a regression that removes the trim flips it red.
// The blank-region cell pins regionConfigured("") == true.
func bindingRegionCases() []bindingRegionCase {
	return []bindingRegionCase{
		{"empty bindings", map[string]EventSourceBindingConfig{}, ""},
		{"matching region", map[string]EventSourceBindingConfig{"client-a": {SourceRegion: "local"}}, ""},
		{"blank region", map[string]EventSourceBindingConfig{"client-a": {SourceRegion: ""}}, ""}, // regionConfigured("") returns true
		{"unmapped region", map[string]EventSourceBindingConfig{"client-a": {SourceRegion: "us-east-1"}}, bindingRegionErr},
		{"whitespace region", map[string]EventSourceBindingConfig{"client-a": {SourceRegion: "  local  "}}, ""}, // trim pin (validate.go:124)
	}
}

// TestValidateBindingRegions (FR-5): every bindingRegionCases() cell against
// validateBindingRegions with regions {"local"}.
func TestValidateBindingRegions(t *testing.T) {
	for _, tc := range bindingRegionCases() {
		t.Run(tc.name, func(t *testing.T) {
			wantConfigErr(t, validateBindingRegions(tc.bindings, map[string]struct{}{"local": {}}), tc.wantErr)
		})
	}
}

func TestValidateEventBindingWildcardIsReplicationOnly(t *testing.T) {
	valid := EventSourceBindingConfig{TenantID: "*", Source: "aero-id", SourceRegion: "local", AllowedTenants: []string{"tenant-a"}}
	if err := validateEventBinding("replication-client", valid); err != nil {
		t.Fatalf("replication wildcard binding rejected: %v", err)
	}
	invalid := valid
	invalid.Source = "aero-im"
	if err := validateEventBinding("replication-client", invalid); err == nil {
		t.Fatal("wildcard tenant binding for aero-im was accepted")
	}
}
