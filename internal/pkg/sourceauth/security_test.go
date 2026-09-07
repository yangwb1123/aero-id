package sourceauth

import (
	"net"
	"strings"
	"testing"
)

func TestParseEndpointRejectsAmbiguousAuthorityAndPath(t *testing.T) {
	for _, value := range []string{
		"https://source.example?", "https://source.example#", "https://source.example:",
		"https://source.example:0", "https://source.example:65536", "https://user:pass@source.example",
		"https://source.example/%2e%2e/private", "https://source.example//private",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := ParseEndpoint(value)
			if err == nil {
				t.Fatalf("ParseEndpoint(%q) accepted an ambiguous endpoint", value)
			}
			if strings.Contains(err.Error(), value) {
				t.Fatalf("ParseEndpoint(%q) disclosed the raw endpoint: %v", value, err)
			}
		})
	}
}

func TestValidAbsolutePathRejectsURLSyntaxAndTraversal(t *testing.T) {
	for _, value := range []string{"/summary?x=1", "/summary#fragment", "/summary?", "/summary#",
		"//other-host/summary", "/../summary", "/%2e%2e/summary", "/summary\r\n"} {
		if ValidAbsolutePath(value) {
			t.Errorf("ValidAbsolutePath(%q) = true, want false", value)
		}
	}
	for _, value := range []string{"/summary", "/api/%3Fvalue", "/api/%2Fvalue"} {
		if !ValidAbsolutePath(value) {
			t.Errorf("ValidAbsolutePath(%q) = false, want true", value)
		}
	}
}

func TestParseOAuthTokenEndpointRequiresApprovedAuthority(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		environment string
		allow       bool
		hosts       []string
		source      string
		want        bool
	}{
		{name: "approved HTTPS host", url: "https://token.example/token", hosts: []string{"token.example"}, source: "https://im.example", want: true},
		{name: "missing host allow list", url: "https://token.example/token", source: "https://im.example"},
		{name: "private IPv4", url: "https://10.0.0.8/token", hosts: []string{"10.0.0.8"}, source: "https://im.example"},
		{name: "link local IPv4", url: "https://169.254.169.254/token", hosts: []string{"169.254.169.254"}, source: "https://im.example"},
		{name: "metadata name", url: "https://metadata/token", hosts: []string{"metadata"}, source: "https://im.example"},
		{name: "mapped loopback", url: "https://[::ffff:127.0.0.1]/token", hosts: []string{"::ffff:127.0.0.1"}, source: "https://im.example"},
		{name: "numeric loopback alias", url: "https://2130706433/token", hosts: []string{"2130706433"}, source: "https://im.example"},
		{name: "HTTP loopback without source opt in", url: "http://127.0.0.1/token", source: "https://im.example"},
		{name: "HTTP loopback production", url: "http://127.0.0.1/token", environment: "production", allow: true, source: "http://127.0.0.1:8080"},
		{name: "HTTP loopback source mismatch", url: "http://127.0.0.1/token", environment: "test", allow: true, source: "https://im.example"},
		{name: "HTTP loopback explicit", url: "http://127.0.0.1:8080/token", environment: "test", allow: true, source: "http://127.0.0.1:8080", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseOAuthTokenEndpoint(tc.url, tc.environment, tc.allow, tc.hosts, tc.source)
			if (err == nil) != tc.want {
				t.Fatalf("ParseOAuthTokenEndpoint() error = %v, want success=%v", err, tc.want)
			}
			if err != nil && (strings.Contains(err.Error(), tc.url) || strings.Contains(err.Error(), "token"+tc.url)) {
				t.Fatalf("OAuth policy error disclosed URL: %v", err)
			}
		})
	}
}

func TestOAuthAllowListRejectsNonCanonicalHostValues(t *testing.T) {
	for _, host := range []string{" token.example", "token.example ", "token.example.", "token.example:",
		"token.example:0", "token.example:65536", "token/example", "token.example?x=1", "token.раy"} {
		if err := ValidateOAuthAllowedHosts([]string{host}); err == nil {
			t.Errorf("ValidateOAuthAllowedHosts(%q) accepted malformed host", host)
		}
	}
}

func TestOAuthLoopbackRequiresTheSameNormalizedOrigin(t *testing.T) {
	cases := []struct {
		name   string
		token  string
		source string
		want   bool
	}{
		{name: "different loopback host", token: "http://127.0.0.1:8080/token", source: "http://localhost:8080"},
		{name: "different port", token: "http://localhost:8080/token", source: "http://localhost:8081"},
		{name: "different scheme", token: "http://localhost:0443/token", source: "https://localhost"},
		{name: "same host and normalized port", token: "http://LOCALHOST:08080/token", source: "http://localhost:8080", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseOAuthTokenEndpoint(tc.token, "test", true, []string{tc.token[:strings.Index(tc.token, "/token")]}, tc.source)
			if (err == nil) != tc.want {
				t.Fatalf("ParseOAuthTokenEndpoint() error = %v, want success=%v", err, tc.want)
			}
		})
	}
}

func TestOAuthDestinationRejectsUnsafeResolvedAddresses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		host  string
		ip    string
		allow bool
		want  bool
	}{
		{name: "public", host: "token.example", ip: "203.0.113.10", want: true},
		{name: "private", host: "token.example", ip: "10.0.0.10"},
		{name: "loopback without exception", host: "token.example", ip: "127.0.0.1"},
		{name: "exact loopback exception", host: "localhost", ip: "127.0.0.1", allow: true, want: true},
		{name: "loopback exception cannot become public", host: "localhost", ip: "203.0.113.10", allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := OAuthDestinationAllowed(tc.host, net.ParseIP(tc.ip), tc.allow); got != tc.want {
				t.Fatalf("OAuthDestinationAllowed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOAuthAllowListBindsNonDefaultPort(t *testing.T) {
	if err := ValidateOAuthAllowedHosts([]string{"token.example:8443"}); err != nil {
		t.Fatalf("explicit port rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		url  string
		host string
		want bool
	}{
		{name: "default port", url: "https://token.example/token", host: "token.example", want: true},
		{name: "wrong non-default port", url: "https://token.example:9443/token", host: "token.example", want: false},
		{name: "matching non-default port", url: "https://token.example:8443/token", host: "token.example:8443", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseOAuthTokenEndpoint(tc.url, "production", false, []string{tc.host}, "https://source.example"); (err == nil) != tc.want {
				t.Fatalf("ParseOAuthTokenEndpoint() error = %v, want success=%v", err, tc.want)
			}
		})
	}
}

func TestSourceAuthPolicyLabelDoesNotEchoUntrustedLabel(t *testing.T) {
	_, err := Evaluate(Config{Source: "snaplink", Label: "https://label-secret.example/token", Endpoint: "https://source.example"})
	if err == nil || strings.Contains(err.Error(), "label-secret.example") {
		t.Fatalf("policy error = %v, want safe label and no URL", err)
	}
}
