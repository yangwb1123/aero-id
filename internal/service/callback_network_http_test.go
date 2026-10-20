package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func callbackLoopbackClient(t *testing.T, server *httptest.Server, ctx context.Context) *http.Client {
	t.Helper()
	client, err := newCallbackHTTPClient(ctx, server.URL, normalizeCallbackConfig(CallbackConfig{
		AllowPrivateTarget: true, RequestTimeout: time.Second,
	}), defaultCallbackNetwork())
	if err != nil {
		t.Fatal(err)
	}
	// Explicit local CA, never InsecureSkipVerify or production trust changes.
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := client.Transport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.MaxResponseHeaderBytes != maxCallbackResponseHeaderBytes {
		t.Fatal("callback transport lost bounded, proxy-free per-call ownership")
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func TestCallbackNetworkTLSKeepsHTTPSChallengeAndDoesNotFollowRedirect(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.TLS == nil || !r.Close {
			t.Error("callback did not use TLS and a single-call connection")
		}
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "/ok")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		if _, err := io.Copy(w, r.Body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	client := callbackLoopbackClient(t, server, ctx)
	for _, path := range []string{"/ok", "/redirect"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader("original-challenge"))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("challenge body/close: %v / %v", err, closeErr)
		}
		if path == "/ok" && (response.StatusCode != http.StatusOK || string(body) != "original-challenge") {
			t.Fatal("TLS challenge content changed")
		}
		if path == "/redirect" && response.StatusCode != http.StatusTemporaryRedirect {
			t.Fatal("callback followed a receiver redirect")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP requests = %d, want 2 without redirect/retry", calls.Load())
	}
}

func TestCallbackNetworkTLSRejectsOversizedResponseHeaders(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Oversized", strings.Repeat("h", int(maxCallbackResponseHeaderBytes)+1))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	client := callbackLoopbackClient(t, server, ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("probe"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if response != nil {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}
	if err == nil {
		t.Fatal("oversized HTTP response headers bypassed the callback budget")
	}
}

func TestCallbackNetworkTLSBodyReadEndsAtOriginalOwnerDeadline(t *testing.T) {
	stop := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusAccepted)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer server.Close()
	defer close(stop)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	client := callbackLoopbackClient(t, server, ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("probe"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _, readErr := callbackDeliveryResponse(ctx, response, 4096)
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Error(closeErr)
	}
	if readErr == nil {
		t.Fatal("stalled body escaped the original network deadline")
	}
}

func TestCallbackNetworkCancelsSocketDuringTLSHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	client, err := newCallbackHTTPClient(ctx, "https://"+listener.Addr().String(), normalizeCallbackConfig(CallbackConfig{
		AllowPrivateTarget: true, RequestTimeout: 3 * time.Second,
	}), defaultCallbackNetwork())
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	peerDone, hello := make(chan error, 1), make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			peerDone <- errors.Join(err, conn.Close())
			return
		}
		_, readErr := conn.Read(make([]byte, 4096)) // Observe the TLS ClientHello, never answer it.
		close(hello)
		_, copyErr := io.Copy(io.Discard, conn)
		peerDone <- errors.Join(readErr, copyErr, conn.Close())
	}()
	result := make(chan error, 1)
	go func() {
		request, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+listener.Addr().String(), strings.NewReader("probe"))
		if reqErr != nil {
			result <- reqErr
			return
		}
		response, sendErr := client.Do(request)
		if response != nil {
			sendErr = errors.Join(sendErr, response.Body.Close())
		}
		result <- sendErr
	}()
	select {
	case <-hello:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not start TLS handshake")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled TLS handshake succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TLS handshake outlived owning call")
	}
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("detached TLS socket remained open after caller cancellation")
	}
}
