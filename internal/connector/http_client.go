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
	client.Jar = nil
	client.CheckRedirect = rejectSourceRedirect
	// Every production request is created with a bounded context by the
	// connector. Preserve an injected client's timeout instead of adding a
	// second, racing http.Client timer; the latter can cancel a response body
	// just before the connector context and obscure a timeout as an
	// unavailable read error. The nil-client path below installs cfg.Timeout
	// directly on the hardened client.
	return &client, nil
}

func rejectSourceRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func unsafeTLSVerification(config *tls.Config) bool {
	return config.InsecureSkipVerify || config.VerifyPeerCertificate != nil || config.VerifyConnection != nil
}

func cloneSourceTransport(client *http.Client) (*http.Transport, error) {
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("source HTTP client requires a standard HTTP transport")
	}
	if transport.DialTLS != nil || transport.DialTLSContext != nil { //nolint:staticcheck // reject custom TLS dialers that bypass verification
		return nil, fmt.Errorf("source HTTP client cannot use a custom TLS dialer")
	}
	if tlsConfig := transport.TLSClientConfig; tlsConfig != nil &&
		(unsafeTLSVerification(tlsConfig) || tlsConfig.GetClientCertificate != nil) {
		return nil, fmt.Errorf("source HTTP client requires standard TLS verification")
	}
	clone := transport.Clone()
	// There is no configured source proxy policy. Clearing Proxy prevents both
	// HTTP_PROXY inheritance and a proxy supplied by a custom client. The clone
	// keeps the standard transport's connection pooling and TLS settings. A
	// caller-supplied dialer is deliberately discarded so it cannot redirect
	// either source or token traffic to an unexpected network.
	clone.Proxy = nil
	clone.Dial = nil //nolint:staticcheck // remove injected network routing
	clone.DialContext = nil
	clone.TLSNextProto = nil
	return clone, nil
}

func newSourceHTTPClient(cfg HTTPConfig) (*http.Client, error) {
	tlsConfig, err := sourceTLSConfig(nil, cfg)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil,
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
