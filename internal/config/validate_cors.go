package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func validateCORSOrigins(origins []string) error {
	seen := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if err := validateCORSOrigin(origin); err != nil {
			return err
		}
		if _, exists := seen[origin]; exists {
			return fmt.Errorf("server.http.cors_origins contains duplicate origin %q", origin)
		}
		seen[origin] = struct{}{}
	}
	return nil
}

func validateCORSOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("server.http.cors_origins contains invalid origin %q", origin)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("server.http.cors_origins contains insecure origin %q", origin)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
