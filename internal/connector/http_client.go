package connector

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
)

func sourceClient(cfg HTTPConfig) (*http.Client, error) {
	if cfg.Client == nil {
		return newSourceHTTPClient(cfg)
	}
	if !sourceTLSConfigured(cfg) {
		return cfg.Client, nil
	}
	transport, err := cloneSourceTransport(cfg.Client)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := sourceTLSConfig(transport.TLSClientConfig, cfg)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsConfig
	client := *cfg.Client
	client.Transport = transport
	return &client, nil
}

func sourceTLSConfigured(cfg HTTPConfig) bool {
	return cfg.TLSCAFile != "" || cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" || cfg.TLSServerName != ""
}

func cloneSourceTransport(client *http.Client) (*http.Transport, error) {
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("source TLS configuration requires a standard HTTP transport")
	}
	if transport.DialTLS != nil || transport.DialTLSContext != nil { //nolint:staticcheck // reject custom TLS dialers that bypass the configured certificate
		return nil, fmt.Errorf("source TLS configuration cannot enforce its settings with a custom TLS dialer")
	}
	if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify { //nolint:gosec // reject an unsafe injected transport
		return nil, fmt.Errorf("source TLS configuration requires server certificate verification")
	}
	return transport.Clone(), nil
}

func newSourceHTTPClient(cfg HTTPConfig) (*http.Client, error) {
	tlsConfig, err := sourceTLSConfig(nil, cfg)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, Proxy: http.ProxyFromEnvironment,
		MaxIdleConns: cfg.MaxConcurrency, MaxIdleConnsPerHost: cfg.MaxConcurrency}
	return &http.Client{Timeout: cfg.Timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func sourceTLSConfig(existing *tls.Config, cfg HTTPConfig) (*tls.Config, error) {
	var tlsConfig *tls.Config
	if existing == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		tlsConfig = existing.Clone()
	}
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	if cfg.TLSCAFile != "" {
		pool, err := sourceCertPool(cfg.TLSCAFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.RootCAs = pool
	}
	if cfg.TLSServerName != "" {
		tlsConfig.ServerName = cfg.TLSServerName
	}
	if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
		cert, err := sourceClientCertificate(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.GetClientCertificate = nil
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	return tlsConfig, nil
}

func sourceCertPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("source CA file could not be read")
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("source CA file contains no certificates")
	}
	return pool, nil
}

func sourceClientCertificate(certFile, keyFile string) (tls.Certificate, error) {
	if certFile == "" || keyFile == "" {
		return tls.Certificate{}, fmt.Errorf("source mTLS requires both client certificate and key")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("source mTLS client certificate could not be loaded")
	}
	return cert, nil
}
