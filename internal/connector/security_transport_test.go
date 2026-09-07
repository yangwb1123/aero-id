package connector

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

func TestSourceClientHardensInjectedTransport(t *testing.T) {
	var proxyCalls, dialCalls atomic.Int32
	baseTransport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			proxyCalls.Add(1)
			return nil, errors.New("proxy must not be called")
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCalls.Add(1)
			return nil, errors.New("dialer must not be called")
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	base := &http.Client{Transport: baseTransport, Jar: &testCookieJar{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	client, err := NewJSONSummaryConnector(SourceSnaplink, HTTPConfig{
		Endpoint: "https://source.example", BearerToken: "source-token", Client: base,
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	transport, ok := client.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.client.Transport)
	}
	if transport == baseTransport || transport.Proxy != nil || transport.DialContext != nil || transport.TLSNextProto != nil {
		t.Fatalf("injected transport was not hardened: %#v", transport)
	}
	if client.client.Jar != nil {
		t.Fatal("cookie jar was retained")
	}
	if err := client.client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v, want ErrUseLastResponse", err)
	}
	if proxyCalls.Load() != 0 || dialCalls.Load() != 0 {
		t.Fatalf("custom routing callbacks ran during construction: proxy=%d dial=%d", proxyCalls.Load(), dialCalls.Load())
	}
}

type testCookieJar struct{}

func (*testCookieJar) SetCookies(*url.URL, []*http.Cookie) {}
func (*testCookieJar) Cookies(*url.URL) []*http.Cookie     { return nil }

func TestSourceClientRejectsUnsafeInjectedTLS(t *testing.T) {
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // deliberate rejection fixture
	_, err := NewJSONSummaryConnector(SourceSnaplink, HTTPConfig{Endpoint: "https://source.example", BearerToken: "source-token", Client: client})
	if err == nil || !strings.Contains(err.Error(), "standard TLS verification") {
		t.Fatalf("constructor error = %v, want unsafe TLS rejection", err)
	}
}

func TestSourceSummaryDoesNotFollowRedirect(t *testing.T) {
	var sinkCalls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { sinkCalls.Add(1) }))
	defer sink.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, sink.URL, http.StatusFound)
	}))
	defer source.Close()
	client := newTestAeroIMConnector(t, source.URL, HTTPConfig{MaxAttempts: 1})
	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{"aero-im.profile"}})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != "source.http_302" {
		t.Fatalf("summary error = %v, want source.http_302", err)
	}
	if sinkCalls.Load() != 0 {
		t.Fatalf("redirect target calls = %d, want 0", sinkCalls.Load())
	}
}

func TestOAuthDoesNotFollowRedirect(t *testing.T) {
	var sinkCalls, sourceCalls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { sinkCalls.Add(1) }))
	defer sink.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			http.Redirect(w, r, sink.URL, http.StatusFound)
			return
		}
		sourceCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer source.Close()
	client := newTestAeroIMConnector(t, source.URL, HTTPConfig{MaxAttempts: 1, OAuth: OAuthClientCredentialsConfig{
		TokenURL: source.URL + "/token", ClientID: "client", ClientSecret: "oauth-secret", Scope: "summary:read", Resource: "aero-im",
	}})
	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{"aero-im.profile"}})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != "source.oauth_token_unavailable" {
		t.Fatalf("summary error = %v, want OAuth failure", err)
	}
	if sinkCalls.Load() != 0 || sourceCalls.Load() != 0 {
		t.Fatalf("redirect/source calls = %d/%d, want 0/0", sinkCalls.Load(), sourceCalls.Load())
	}
}

func TestDirectOAuthDefaultClientIsHardened(t *testing.T) {
	var sinkCalls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { sinkCalls.Add(1) }))
	defer sink.Close()
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, sink.URL, http.StatusFound)
	}))
	defer token.Close()
	tokens, err := newOAuthClientCredentials(OAuthClientCredentialsConfig{
		TokenURL: token.URL, ClientID: "client", ClientSecret: "oauth-secret", Scope: "summary:read", Resource: "aero-im",
		Environment: "test", AllowLoopback: true,
	}, nil, time.Second)
	if err != nil {
		t.Fatalf("OAuth constructor: %v", err)
	}
	_, err = tokens.accessToken(t.Context())
	if err == nil || strings.Contains(err.Error(), sink.URL) {
		t.Fatalf("OAuth error = %v, want bounded redirect failure", err)
	}
	if sinkCalls.Load() != 0 {
		t.Fatalf("redirect target calls = %d, want 0", sinkCalls.Load())
	}
}

func TestRedactedRemoteCauseNeverRetainsTransportDetail(t *testing.T) {
	const sentinel = "secret transport detail https://user:password@example.invalid:8443"
	cause := redactedRemoteCause(errors.New(sentinel))
	if cause == nil || strings.Contains(cause.Error(), sentinel) || strings.Contains(cause.Error(), "example.invalid") {
		t.Fatalf("redacted cause = %v, contains transport detail", cause)
	}
	if !errors.Is(redactedRemoteCause(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("deadline cancellation was not preserved")
	}
	if !errors.Is(redactedRemoteCause(context.Canceled), context.Canceled) {
		t.Fatal("cancellation was not preserved")
	}
}

func TestAppendSourcePathRetainsCanonicalEscapes(t *testing.T) {
	endpoint, err := url.Parse("https://source.example/%20base")
	if err != nil {
		t.Fatal(err)
	}
	if !appendSourcePath(endpoint, "/internal/account-summary") {
		t.Fatal("appendSourcePath rejected a valid path")
	}
	if got, want := endpoint.String(), "https://source.example/%20base/internal/account-summary"; got != want {
		t.Fatalf("joined URL = %q, want %q", got, want)
	}
}

func TestSummaryRequestConstructionErrorsAreRedacted(t *testing.T) {
	client := &JSONSummaryConnector{source: SourceSnaplink, cfg: HTTPConfig{Endpoint: "https://source.example"}}
	_, err := client.newSummaryRequest(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{"snaplink.identity"}, RequestID: "request"})
	if err != nil && strings.Contains(err.Error(), "source.example") {
		t.Fatalf("request construction error disclosed endpoint: %v", err)
	}
}
