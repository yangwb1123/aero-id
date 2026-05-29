package sso

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// TLSConfig holds the transport-security knobs for dialing SSO. When Insecure is
// true the connection uses no credentials (dev/bufconn only). Otherwise proper TLS
// is required: CAFile pins the server's CA (empty => system pool); CertFile+KeyFile
// enable mTLS; ServerName overrides the verified hostname (e.g. behind a proxy).
type TLSConfig struct {
	Insecure   bool
	CAFile     string
	CertFile   string
	KeyFile    string
	ServerName string
}

// dialCredentials selects gRPC transport credentials from a TLSConfig. It is the
// single, testable place that decides insecure vs TLS/mTLS, so NewClient stays thin.
func dialCredentials(cfg TLSConfig) (credentials.TransportCredentials, error) {
	if cfg.Insecure {
		return insecure.NewCredentials(), nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.ServerName != "" {
		tlsCfg.ServerName = cfg.ServerName
	}

	// Verify the server against a custom CA when provided, else the system pool.
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read sso ca file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("sso ca file %q: no certificates parsed", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}

	// Present a client certificate for mTLS when both halves are configured.
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return nil, fmt.Errorf("sso mTLS requires both cert and key files")
		}
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load sso client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return credentials.NewTLS(tlsCfg), nil
}

// keepaliveParams returns the gRPC client keepalive policy. Time/Timeout fall back
// to production-safe defaults when non-positive; PermitWithoutStream stays false so
// idle connections don't ping (and risk upsetting strict server policies).
func keepaliveParams(pingTime, pingTimeout time.Duration) keepalive.ClientParameters {
	if pingTime <= 0 {
		pingTime = 30 * time.Second
	}
	if pingTimeout <= 0 {
		pingTimeout = 10 * time.Second
	}
	return keepalive.ClientParameters{
		Time:                pingTime,
		Timeout:             pingTimeout,
		PermitWithoutStream: false,
	}
}

// dialOptions assembles the transport-credential and keepalive dial options.
func dialOptions(tlsCfg TLSConfig, pingTime, pingTimeout time.Duration) ([]grpc.DialOption, error) {
	creds, err := dialCredentials(tlsCfg)
	if err != nil {
		return nil, err
	}
	return []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepaliveParams(pingTime, pingTimeout)),
	}, nil
}
