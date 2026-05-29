package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	App        AppConfig      `mapstructure:"app"`
	Server     ServerConfig   `mapstructure:"server"`
	Database   DatabaseConfig `mapstructure:"database"`
	SSO        SSOConfig      `mapstructure:"sso"`
	Storage    StorageConfig  `mapstructure:"storage"`
	SigningKey string         `mapstructure:"signing_key"`
	Log        LogConfig      `mapstructure:"log"`
}

type AppConfig struct {
	Name    string `mapstructure:"name"`
	Env     string `mapstructure:"env"`
	Version string `mapstructure:"version"`
}

type ServerConfig struct {
	HTTP HTTPConfig `mapstructure:"http"`
	GRPC GRPCConfig `mapstructure:"grpc"`
}

type HTTPConfig struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	IdempotencyTTL  time.Duration `mapstructure:"idempotency_ttl"` // how long a stored idempotent response is replayable
}

type GRPCConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
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
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=UTC",
		d.Host, d.Port, d.User, d.Password, d.DBName, d.SSLMode,
	)
}

type SSOConfig struct {
	Endpoint          string        `mapstructure:"endpoint"`
	Timeout           time.Duration `mapstructure:"timeout"`
	Insecure          bool          `mapstructure:"insecure"`
	ReadinessCritical bool          `mapstructure:"readiness_critical"`
	TokenCacheTTL     time.Duration `mapstructure:"token_cache_ttl"`
	StaleTTL          time.Duration `mapstructure:"stale_ttl"` // serve last-known identity past expiry when SSO is down
	UserCacheTTL      time.Duration `mapstructure:"user_cache_ttl"`
	BreakerThreshold  int           `mapstructure:"breaker_threshold"` // consecutive availability failures that trip the breaker (0 = off)
	BreakerCooldown   time.Duration `mapstructure:"breaker_cooldown"`  // how long the breaker fast-fails before probing SSO again
	MaxAttempts       int           `mapstructure:"max_attempts"`      // upstream tries per call within the timeout budget (1 = no retry)
	RetryBackoff      time.Duration `mapstructure:"retry_backoff"`     // base exponential backoff between availability-error retries
	KeepaliveTime     time.Duration `mapstructure:"keepalive_time"`    // gRPC keepalive ping interval
	KeepaliveTimeout  time.Duration `mapstructure:"keepalive_timeout"` // gRPC keepalive ping ack timeout
	// mTLS / TLS transport security (used only when insecure=false).
	TLSCAFile     string `mapstructure:"tls_ca_file"`     // CA bundle to verify the SSO server (empty = system pool)
	TLSCertFile   string `mapstructure:"tls_cert_file"`   // client cert for mTLS (with tls_key_file)
	TLSKeyFile    string `mapstructure:"tls_key_file"`    // client key for mTLS (with tls_cert_file)
	TLSServerName string `mapstructure:"tls_server_name"` // override the verified server hostname
}

type StorageConfig struct {
	Type             string             `mapstructure:"type"`
	MaxUploadBytes   int64              `mapstructure:"max_upload_bytes"`   // 0 = unlimited
	AllowedMimeTypes []string           `mapstructure:"allowed_mime_types"` // empty = allow any
	Local            LocalStorageConfig `mapstructure:"local"`
	S3               S3StorageConfig    `mapstructure:"s3"`
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
	Level    string `mapstructure:"level"`
	Encoding string `mapstructure:"encoding"`
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
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.SigningKey == "" {
		return fmt.Errorf("signing_key is required")
	}
	if c.Database.DBName == "" {
		return fmt.Errorf("database.dbname is required")
	}
	if c.SSO.Endpoint == "" {
		return fmt.Errorf("sso.endpoint is required")
	}
	switch c.Storage.Type {
	case "local", "s3":
	default:
		return fmt.Errorf("storage.type must be 'local' or 's3'")
	}
	return nil
}
