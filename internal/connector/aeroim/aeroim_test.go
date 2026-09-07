package aeroim

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aero/aero-id/internal/connector"
)

type aeroIMNoCallTransport struct {
	calls atomic.Int32
}

func (t *aeroIMNoCallTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, io.ErrUnexpectedEOF
}

func TestNewPropagatesTargetAssertionConstructionError(t *testing.T) {
	transport := &aeroIMNoCallTransport{}
	secret := strings.Repeat("s", 32)
	_, err := New(connector.HTTPConfig{
		Endpoint: "https://im.example", BearerToken: secret,
		Client: &http.Client{Transport: transport},
	})
	if err == nil || !strings.Contains(err.Error(), "target assertion") {
		t.Fatalf("New error = %v, want target assertion construction error", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "https://im.example") {
		t.Fatalf("New error disclosed sensitive configuration: %v", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("remote calls during failed New = %d, want 0", transport.calls.Load())
	}
}

func TestNewPreservesExplicitLoopbackCompatibility(t *testing.T) {
	item, err := New(connector.HTTPConfig{
		Endpoint: "http://127.0.0.1:8080", AllowInsecureLoopback: true, Environment: "test",
	})
	if err != nil {
		t.Fatalf("New loopback error = %v", err)
	}
	if item.Source() != connector.SourceAeroIM {
		t.Fatalf("connector source = %q, want %q", item.Source(), connector.SourceAeroIM)
	}
}
