package sourceauth

import (
	"strings"
	"testing"
)

func TestEvaluateAeroIMAuthenticationMatrix(t *testing.T) {
	cases := []struct {
		name        string
		endpoint    string
		environment string
		allow       bool
		bearer      string
		cert        string
		key         string
		wantMode    Mode
		wantErr     string
	}{
		{name: "missing auth", endpoint: "https://im.example", wantErr: "service authentication is required"},
		{name: "loopback opt in", endpoint: "http://localhost", environment: "test", allow: true, wantMode: ModeLoopback},
		{name: "loopback opt in ipv4", endpoint: "http://127.0.0.1:8080", environment: "dev", allow: true, wantMode: ModeLoopback},
		{name: "loopback opt in ipv6", endpoint: "http://[::1]:8080", environment: "test", allow: true, wantMode: ModeLoopback},
		{name: "loopback flag missing", endpoint: "http://127.0.0.1", wantErr: "service authentication is required"},
		{name: "non loopback flag", endpoint: "https://im.example", environment: "test", allow: true, wantErr: "exact loopback"},
		{name: "production flag", endpoint: "http://localhost", environment: "staging", allow: true, wantErr: "not allowed"},
		{name: "static bearer", endpoint: "https://im.example", bearer: "  static-token  ", wantMode: ModeStaticBearer},
		{name: "blank bearer", endpoint: "https://im.example", bearer: "   ", wantErr: "service authentication is required"},
		{name: "control bearer", endpoint: "https://im.example", bearer: "static\ntoken", wantErr: "bearer token is invalid"},
		{name: "mtls", endpoint: "https://im.example", cert: "/cert", key: "/key", wantMode: ModeMTLS},
		{name: "mtls http", endpoint: "http://localhost", cert: "/cert", key: "/key", wantErr: "mTLS requires an HTTPS endpoint"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := Evaluate(Config{Source: aeroIMSource, Endpoint: tc.endpoint, Environment: tc.environment,
				AllowInsecureLoopback: tc.allow, BearerToken: tc.bearer,
				TLSCertFile: tc.cert, TLSKeyFile: tc.key})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if decision.Mode != tc.wantMode {
				t.Fatalf("mode = %q, want %q", decision.Mode, tc.wantMode)
			}
		})
	}
}

func TestEvaluateDoesNotApplyAeroIMPolicyToOtherSources(t *testing.T) {
	decision, err := Evaluate(Config{Source: "snaplink", Endpoint: "http://source.example"})
	if err != nil {
		t.Fatalf("non-Aero IM source was rejected: %v", err)
	}
	if decision.Mode != ModeDisabled {
		t.Fatalf("mode = %q, want disabled pass-through", decision.Mode)
	}
}
