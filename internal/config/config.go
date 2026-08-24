package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	App             AppConfig             `mapstructure:"app"`
	Account         AccountConfig         `mapstructure:"account"`
	Sources         SourcesConfig         `mapstructure:"sources"`
	Server          ServerConfig          `mapstructure:"server"`
	Database        DatabaseConfig        `mapstructure:"database"`
	Redis           RedisConfig           `mapstructure:"redis"`
	SSO             SSOConfig             `mapstructure:"sso"`
	Storage         StorageConfig         `mapstructure:"storage"`
	Tracing         TracingConfig         `mapstructure:"tracing"`
	RateLimit       RateLimitConfig       `mapstructure:"rate_limit"`
	Outbox          OutboxConfig          `mapstructure:"outbox"`
	EventIngest     EventIngestConfig     `mapstructure:"event_ingest"`
	EventBus        EventBusConfig        `mapstructure:"event_bus"`
	Replication     ReplicationConfig     `mapstructure:"replication"`
	AuditLedger     AuditLedgerConfig     `mapstructure:"audit_ledger"`
	AuditGovernance AuditGovernanceConfig `mapstructure:"audit_governance"`
	Billing         BillingConfig         `mapstructure:"billing"`
	Callback        CallbackConfig        `mapstructure:"callback"`
	Export          ExportConfig          `mapstructure:"export"`
	SigningKey      string                `mapstructure:"signing_key"`
	Log             LogConfig             `mapstructure:"log"`
	DataRetention   DataRetentionConfig   `mapstructure:"data_retention"`
}

// RateLimitConfig configures the per-actor limiter applied on both transports.
// It is OPT-IN: disabled by default so it never surprises dev/CI. With Redis
// configured, the shared budget is enforced across instances by a
// request-anchored fixed-window counter (window = max(1s, burst/rate)); when
// Redis is unavailable the middleware and gRPC interceptor fall back to the
// local per-instance token bucket, whose smooth refill shape and per-process
// scope differ from the distributed fixed window.
type RateLimitConfig struct {
	Enabled      bool          `mapstructure:"enabled"`       // master switch; false = limiter never installed
	RPS          float64       `mapstructure:"rps"`           // sustained requests/second per key
	Burst        int           `mapstructure:"burst"`         // bucket capacity (max instantaneous burst)
	IdleTTL      time.Duration `mapstructure:"idle_ttl"`      // evict buckets untouched this long (bounds memory)
	CleanupEvery time.Duration `mapstructure:"cleanup_every"` // how often the idle-bucket sweep runs
}

// OutboxConfig configures the transactional outbox relay and its downstream handlers.
type OutboxConfig struct {
	PollInterval time.Duration `mapstructure:"poll_interval"` // how often the relay drains (0 => default 5s)
	MaxRetries   int           `mapstructure:"max_retries"`   // per-event retry ceiling before dead-lettering (0 => DefaultMaxRetries)
	Webhook      WebhookConfig `mapstructure:"webhook"`       // optional webhook delivery; empty URL => disabled
}

// WebhookConfig configures delivery of outbox events to an external HTTP endpoint.
type WebhookConfig struct {
	URL        string            `mapstructure:"url"`         // target endpoint (empty => disabled)
	SigningKey string            `mapstructure:"signing_key"` // HMAC-SHA256 key for X-Event-Signature
	Timeout    string            `mapstructure:"timeout"`     // e.g. "5s"; 0 => default 10s
	EventTypes []string          `mapstructure:"event_types"` // empty => deliver all; non-empty => filter
	Headers    map[string]string `mapstructure:"headers"`     // additional HTTP headers per request
}

// EventIngestConfig protects source event ingestion with per-source HMAC keys
// for HTTP and verified Snaplink client bindings for gRPC.
type EventIngestConfig struct {
	SigningKey       string                              `mapstructure:"signing_key"`       // compatibility shared HMAC key
	SourceKeys       map[string]string                   `mapstructure:"source_keys"`       // source -> HMAC key
	RequireTimestamp bool                                `mapstructure:"require_timestamp"` // bind HTTP signature to timestamp
	MaxBodyBytes     int64                               `mapstructure:"max_body_bytes"`
	ClockSkew        time.Duration                       `mapstructure:"clock_skew"`
	AllowedRegions   []string                            `mapstructure:"allowed_regions"` // residency allow-list (P6-04); empty = accept any source region
	Bindings         map[string]EventSourceBindingConfig `mapstructure:"bindings"`        // Snaplink client_id -> source binding
}

type EventSourceBindingConfig struct {
	TenantID     string `mapstructure:"tenant_id"`
	Source       string `mapstructure:"source"`
	SourceRegion string `mapstructure:"source_region"`
}

// EventBusConfig configures the canonical-event consumer transport (P3-04).
// NATS JetStream is the initial adapter; the EventBus port allows others.
type EventBusConfig struct {
	NATS NATSBusConfig `mapstructure:"nats"`
}

type NATSBusConfig struct {
	Enabled           bool          `mapstructure:"enabled"`
	URL               string        `mapstructure:"url"`
	Stream            string        `mapstructure:"stream"`
	Subject           string        `mapstructure:"subject"`
	DeadLetterSubject string        `mapstructure:"dead_letter_subject"`
	Durable           string        `mapstructure:"durable"`
	MaxRedeliveries   int           `mapstructure:"max_redeliveries"`
	AckWait           time.Duration `mapstructure:"ack_wait"`
}

// ReplicationConfig configures cross-region projection replication (P6-03).
// Transport is one of "nats" (subject) or "http" (remote ingest endpoint with
// the same HMAC scheme the ingest handler verifies).
type ReplicationConfig struct {
	Enabled         bool          `mapstructure:"enabled"`
	Interval        time.Duration `mapstructure:"interval"`
	Lease           time.Duration `mapstructure:"lease"` // worker lease; 0 = service default
	BatchSize       int           `mapstructure:"batch_size"`
	Transport       string        `mapstructure:"transport"`         // nats | http
	NATSSubject     string        `mapstructure:"nats_subject"`      // transport=nats
	HTTPEndpoint    string        `mapstructure:"http_endpoint"`     // transport=http
	HTTPKey         string        `mapstructure:"http_key"`          // transport=http; HMAC key for the remote ingest
	HTTPBearerToken string        `mapstructure:"http_bearer_token"` // transport=http; service identity token, secret provider/env only
}

type AuditLedgerConfig struct {
	SigningKey string `mapstructure:"signing_key"`
}

type CallbackConfig struct {
	MasterKey          string        `mapstructure:"master_key"`
	RequestTimeout     time.Duration `mapstructure:"request_timeout"`
	MaxResponseBytes   int64         `mapstructure:"max_response_bytes"`
	MaxAttempts        int           `mapstructure:"max_attempts"`
	RetryBase          time.Duration `mapstructure:"retry_base"`
	AllowPrivateTarget bool          `mapstructure:"allow_private_target"`
	WorkerInterval     time.Duration `mapstructure:"worker_interval"`
	WorkerConcurrency  int           `mapstructure:"worker_concurrency"` // in-flight deliveries/publications (P6-06); 0 = serial
}

type ExportConfig struct {
	EncryptionKey string        `mapstructure:"encryption_key"`
	MaxBytes      int64         `mapstructure:"max_bytes"`
	ArtifactTTL   time.Duration `mapstructure:"artifact_ttl"`
}

// TracingConfig configures OpenTelemetry tracing. Tracing is opt-in: unless Enabled
// is true (or an Endpoint is set) a no-op tracer is installed so dev/tests/CI need
// no collector. Endpoint is an OTLP/gRPC collector address (host:port).
type TracingConfig struct {
	Enabled     bool          `mapstructure:"enabled"`
	Endpoint    string        `mapstructure:"endpoint"`     // OTLP/gRPC collector, e.g. "localhost:4317"
	Insecure    bool          `mapstructure:"insecure"`     // dial the collector without TLS
	Timeout     time.Duration `mapstructure:"timeout"`      // exporter timeout; <=0 falls back to a default
	SampleRatio float64       `mapstructure:"sample_ratio"` // 0..1 parent-based ratio sampler; <=0 => always-on
}

type AppConfig struct {
	Name    string `mapstructure:"name"`
	Env     string `mapstructure:"env"`
	Version string `mapstructure:"version"`
}

// AccountConfig identifies the local control-plane cell and the external
// identity namespace used when mapping Snaplink subjects to aero-id accounts.
type AccountConfig struct {
	Issuer            string   `mapstructure:"issuer"`
	Region            string   `mapstructure:"region"`
	HomeRegionVersion string   `mapstructure:"home_region_version"`
	Regions           []string `mapstructure:"regions"`
}

type SourcesConfig struct {
	Snaplink  SourceEndpointConfig `mapstructure:"snaplink"`
	AeroIM    SourceEndpointConfig `mapstructure:"aero_im"`
	AeroVault SourceEndpointConfig `mapstructure:"aero_vault"`
}

type SourceEndpointConfig struct {
	Endpoint       string        `mapstructure:"endpoint"`
	SummaryPath    string        `mapstructure:"summary_path"`
	ErasePath      string        `mapstructure:"erase_path"`
	StatusPath     string        `mapstructure:"status_path"`
	Region         string        `mapstructure:"region"`
	BearerToken    string        `mapstructure:"bearer_token"` // compatibility only; prefer OAuth client credentials
	OAuthTokenURL  string        `mapstructure:"oauth_token_url"`
	OAuthClientID  string        `mapstructure:"oauth_client_id"`
	OAuthSecret    string        `mapstructure:"oauth_client_secret"` // secret provider/env only
	OAuthScope     string        `mapstructure:"oauth_scope"`
	OAuthResource  string        `mapstructure:"oauth_resource"`
	Timeout        time.Duration `mapstructure:"timeout"`
	MaxBodyBytes   int64         `mapstructure:"max_body_bytes"`
	MaxConcurrency int           `mapstructure:"max_concurrency"`
	MaxAttempts    int           `mapstructure:"max_attempts"`
	RetryBackoff   time.Duration `mapstructure:"retry_backoff"`
	TLSCAFile      string        `mapstructure:"tls_ca_file"`
	TLSCertFile    string        `mapstructure:"tls_cert_file"`
	TLSKeyFile     string        `mapstructure:"tls_key_file"`
	TLSServerName  string        `mapstructure:"tls_server_name"`
}

type ServerConfig struct {
	HTTP HTTPConfig `mapstructure:"http"`
	GRPC GRPCConfig `mapstructure:"grpc"`
}

type HTTPConfig struct {
	Host              string        `mapstructure:"host"`
	Port              int           `mapstructure:"port"`
	CORSOrigins       []string      `mapstructure:"cors_origins"` // exact browser origins; empty keeps the development wildcard
	ReadTimeout       time.Duration `mapstructure:"read_timeout"`
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"` // prevents Slowloris; >= 1s
	WriteTimeout      time.Duration `mapstructure:"write_timeout"`
	ShutdownTimeout   time.Duration `mapstructure:"shutdown_timeout"`
	IdempotencyTTL    time.Duration `mapstructure:"idempotency_ttl"` // how long a stored idempotent response is replayable
	RequestTimeout    time.Duration `mapstructure:"request_timeout"` // per-request context deadline (0 = disabled)
	LegacySunset      string        `mapstructure:"legacy_sunset"`   // RFC 8594 Sunset date for legacy compat routes (ADR-011); empty = headers only
}

// GRPCConfig configures the gRPC server. Zero-valued optional fields use the
// gRPC defaults documented on grpc.ServerOption constants.
type GRPCConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
	// ReflectionEnabled registers the gRPC server reflection service. OFF by
	// default: reflection exposes the full RPC surface, which is handy for
	// internal ops (grpcurl) but should be opt-in on an untrusted edge.
	ReflectionEnabled bool `mapstructure:"reflection_enabled"`
	// KeepaliveMinPingInterval is the minimum permitted client ping interval.
	// Clients that ping more frequently receive a GOAWAY. 0 => default (10s).
	KeepaliveMinPingInterval time.Duration `mapstructure:"keepalive_min_ping_interval"`
	// MaxConcurrentStreams limits concurrent streams per connection.
	// 0 (or negative) means unlimited — the gRPC default.
	MaxConcurrentStreams uint32 `mapstructure:"max_concurrent_streams"`
	// MaxRecvMsgSize is the max message size in bytes the server can receive.
	// 0 => grpc.DefaultMaxRecvMsgSize (4 MiB).
	MaxRecvMsgSize int `mapstructure:"max_recv_msg_size"`
	// MaxSendMsgSize is the max message size in bytes the server can send.
	// 0 => grpc.DefaultMaxSendMsgSize (math.MaxInt32, effectively unlimited).
	MaxSendMsgSize int `mapstructure:"max_send_msg_size"`
	// InitialWindowSize sets the stream-level flow-control window. 0 => default (64 KiB).
	InitialWindowSize int32 `mapstructure:"initial_window_size"`
	// InitialConnWindowSize sets the connection-level flow-control window.
	// 0 => default (64 KiB).
	InitialConnWindowSize int32 `mapstructure:"initial_conn_window_size"`
}

type DatabaseConfig struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	DBName          string        `mapstructure:"dbname"`
	SSLMode         string        `mapstructure:"sslmode"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	LogLevel        string        `mapstructure:"log_level"`
	// AutoMigrate, when true, applies all pending up-migrations during app
	// startup (before the servers begin serving). Default FALSE so production
	// stays explicit; convenient for dev/CI. See MigrationsDir.
	AutoMigrate bool `mapstructure:"auto_migrate"`
	// MigrationsDir is the directory of SQL migration files used by AutoMigrate.
	// Defaults to "migrations" when empty.
	MigrationsDir string `mapstructure:"migrations_dir"`
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=UTC",
		d.Host, d.Port, d.User, d.Password, d.DBName, d.SSLMode,
	)
}

type SSOConfig struct {
	Endpoint            string        `mapstructure:"endpoint"`
	JWKSURL             string        `mapstructure:"jwks_url"`
	Issuer              string        `mapstructure:"issuer"`
	Audience            string        `mapstructure:"audience"`
	AuthZClientID       string        `mapstructure:"authz_client_id"`
	AuthZRequired       bool          `mapstructure:"authz_required"`
	AuthZLegacyFallback bool          `mapstructure:"authz_legacy_fallback"`
	JWKSCacheTTL        time.Duration `mapstructure:"jwks_cache_ttl"`
	JWKSMaxBodyBytes    int64         `mapstructure:"jwks_max_body_bytes"`
	Timeout             time.Duration `mapstructure:"timeout"`
	Insecure            bool          `mapstructure:"insecure"`
	ReadinessCritical   bool          `mapstructure:"readiness_critical"`
	TokenCacheTTL       time.Duration `mapstructure:"token_cache_ttl"`
	StaleTTL            time.Duration `mapstructure:"stale_ttl"` // serve last-known identity past expiry when SSO is down
	UserCacheTTL        time.Duration `mapstructure:"user_cache_ttl"`
	BreakerThreshold    int           `mapstructure:"breaker_threshold"` // consecutive availability failures that trip the breaker (0 = off)
	BreakerCooldown     time.Duration `mapstructure:"breaker_cooldown"`  // how long the breaker fast-fails before probing SSO again
	MaxAttempts         int           `mapstructure:"max_attempts"`      // upstream tries per call within the timeout budget (1 = no retry)
	RetryBackoff        time.Duration `mapstructure:"retry_backoff"`     // base exponential backoff between availability-error retries
	MaxConcurrent       int           `mapstructure:"max_concurrent"`    // max concurrent in-flight SSO RPCs (validate/get_users/authorize; 0 = no cap)
	UserBatchSize       int           `mapstructure:"user_batch_size"`   // max UIDs per GetUsers RPC (0 = code default)
	KeepaliveTime       time.Duration `mapstructure:"keepalive_time"`    // gRPC keepalive ping interval
	KeepaliveTimeout    time.Duration `mapstructure:"keepalive_timeout"` // gRPC keepalive ping ack timeout
	// mTLS / TLS transport security (used only when insecure=false).
	TLSCAFile     string   `mapstructure:"tls_ca_file"`     // CA bundle to verify the SSO server (empty = system pool)
	TLSCertFile   string   `mapstructure:"tls_cert_file"`   // client cert for mTLS (with tls_key_file)
	TLSKeyFile    string   `mapstructure:"tls_key_file"`    // client key for mTLS (with tls_cert_file)
	TLSServerName string   `mapstructure:"tls_server_name"` // override the verified server hostname
	TLSCertPins   []string `mapstructure:"tls_cert_pins"`   // hex SHA-256 SPKI pins; leaf cert key must match one (empty = disabled)
}

type StorageConfig struct {
	Type             string             `mapstructure:"type"`
	MaxUploadBytes   int64              `mapstructure:"max_upload_bytes"`   // 0 = unlimited
	AllowedMimeTypes []string           `mapstructure:"allowed_mime_types"` // empty = allow any
	Local            LocalStorageConfig `mapstructure:"local"`
	S3               S3StorageConfig    `mapstructure:"s3"`
	GC               StorageGCConfig    `mapstructure:"gc"`
}

// StorageGCConfig configures the storage reconciliation sweep. The job is OPT-IN:
// disabled by default so it never surprises an operator. When Enabled, the sweep purges
// soft-deleted file rows older than Retention (deleting each unreferenced blob) and,
// when SweepOrphans is set and the backend can list objects, removes orphaned blobs
// older than MinOrphanAge.
type StorageGCConfig struct {
	Enabled      bool          `mapstructure:"enabled"`        // master switch; false = sweep never starts
	Interval     time.Duration `mapstructure:"interval"`       // how often a reconciliation pass runs
	Retention    time.Duration `mapstructure:"retention"`      // how long a soft-deleted row is kept before purge
	MinOrphanAge time.Duration `mapstructure:"min_orphan_age"` // orphan blobs younger than this are never swept
	PurgeBatch   int           `mapstructure:"purge_batch"`    // max rows purged per pass (<=0 => default)
	SweepOrphans bool          `mapstructure:"sweep_orphans"`  // enable orphan-blob detection (best-effort, backend-dependent)
	// PurgePaused suspends the destructive sweep phases (row purge + orphan
	// reclamation) so maintenance flows such as dead-letter replay cannot race
	// blob reclamation. Meaningful only when the sweep is enabled.
	PurgePaused bool `mapstructure:"purge_paused"`
}

type LocalStorageConfig struct {
	Root          string `mapstructure:"root"`
	PublicBaseURL string `mapstructure:"public_base_url"`
}

type S3StorageConfig struct {
	Region       string `mapstructure:"region"`
	Bucket       string `mapstructure:"bucket"`
	Endpoint     string `mapstructure:"endpoint"`
	AccessKey    string `mapstructure:"access_key"`
	SecretKey    string `mapstructure:"secret_key"`
	UsePathStyle bool   `mapstructure:"use_path_style"`
}

type LogConfig struct {
	Level       string  `mapstructure:"level"`
	Encoding    string  `mapstructure:"encoding"`
	SampleRatio float64 `mapstructure:"sample_ratio"` // 0..1 probability for info-level access log sampling; <=0 disables sampling
}

// RedisConfig configures a rueidis-powered Redis client for shared state
// (idempotency store, SSO cache, outbox leader election, rate limiter, etc.).
// All subsystems share one connection pool. When endpoint is empty every Redis
// feature falls back to its GORM / in-memory alternative.
//
// Zero-valued optional fields use rueidis defaults (see rueidis.ClientOption).
type RedisConfig struct {
	Endpoint            string        `mapstructure:"endpoint"`               // e.g. "127.0.0.1:6379"; empty = disabled
	Password            string        `mapstructure:"password"`               // AUTH password (env: AERO_ID_REDIS_PASSWORD)
	DB                  int           `mapstructure:"db"`                     // SELECT database number
	BlockingPoolSize    int           `mapstructure:"blocking_pool_size"`     // blocking connection pool size (0=default)
	BlockingPoolMinSize int           `mapstructure:"blocking_pool_min_size"` // min idle connections (0=default)
	BlockingPoolCleanup time.Duration `mapstructure:"blocking_pool_cleanup"`  // idle cleanup interval (0=disabled)
	ConnWriteTimeout    time.Duration `mapstructure:"conn_write_timeout"`     // per-connection deadline
	ConnLifetime        time.Duration `mapstructure:"conn_lifetime"`          // max connection lifetime
	DialTimeout         time.Duration `mapstructure:"dial_timeout"`           // TCP dial timeout (0 => 5s)
	KeepAlive           time.Duration `mapstructure:"keepalive"`              // TCP keepalive interval (0 => 1s)
}

// Enabled reports whether a Redis endpoint is configured.
func (r RedisConfig) Enabled() bool { return r.Endpoint != "" }

// DataRetentionConfig controls TTL-based cleanup of unbounded table growth.
// Each sub-config has its own retention period and cleanup interval; setting
// interval to 0 disables that particular cleaner.
type DataRetentionConfig struct {
	AuditRetention     time.Duration `mapstructure:"audit_retention"`      // how long audit events are kept before purge (e.g. 90d)
	AuditCleanInterval time.Duration `mapstructure:"audit_clean_interval"` // how often the audit cleaner runs (0 = disabled)
	// NotificationRetention controls cleanup of read notifications. Notifications
	// that have been read longer than this are purged. E.g. 30d.
	NotificationRetention time.Duration `mapstructure:"notification_retention"`
	// NotificationUnreadRetention controls cleanup of unread notifications that
	// are extremely old (user likely inactive). E.g. 90d.
	NotificationUnreadRetention time.Duration `mapstructure:"notification_unread_retention"`
	// NotificationCleanInterval is how often the notification cleaner runs.
	// 0 = disabled.
	NotificationCleanInterval time.Duration `mapstructure:"notification_clean_interval"`
	// OutboxDeadLetterRetention controls cleanup of dead-lettered outbox events.
	// E.g. 7d.
	OutboxDeadLetterRetention time.Duration `mapstructure:"outbox_dead_letter_retention"`
	// OutboxPublishedRetention controls cleanup of successfully published outbox
	// events. E.g. 30d.
	OutboxPublishedRetention time.Duration `mapstructure:"outbox_published_retention"`
	// OutboxCleanInterval is how often the outbox cleaner runs. 0 = disabled.
	OutboxCleanInterval time.Duration `mapstructure:"outbox_clean_interval"`
	// PurgePaused suspends the outbox retention cleaner (dead-letter and
	// published purges) so maintenance flows such as dead-letter replay cannot
	// race a purge. Meaningful only while the cleaner is enabled
	// (outbox_clean_interval > 0 and outbox_dead_letter_retention > 0); see
	// validateDataRetention. Static per boot (no hot reload).
	PurgePaused bool `mapstructure:"purge_paused"`
}

func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("AERO_ID")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	// Reject unknown file keys at Load rather than silently falling back to a
	// default. AutomaticEnv remains lazy, so unrelated ambient environment
	// variables do not become phantom configuration fields.
	if err := v.UnmarshalExact(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
