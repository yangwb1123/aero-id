package service

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

type callbackNetworkTransport struct {
	respond func(*http.Request) (*http.Response, error)
	calls   atomic.Int32
	closes  atomic.Int32
}

func (t *callbackNetworkTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	defer request.Body.Close()
	return t.respond(request)
}

func (t *callbackNetworkTransport) CloseIdleConnections() { t.closes.Add(1) }

func callbackBudgetResolver(t *testing.T, observed *time.Time) func(context.Context, string, string) ([]net.IP, error) {
	t.Helper()
	return func(ctx context.Context, network, _ string) ([]net.IP, error) {
		deadline, ok := ctx.Deadline()
		if !ok || network != "ip" {
			t.Error("callback DNS lacks a bounded IP-only lookup context")
		}
		if observed.IsZero() {
			*observed = deadline
		} else if !deadline.Equal(*observed) {
			t.Error("callback network phase renewed its original deadline")
		}
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
}

func TestCallbackNetworkRegistrationSharesDNSChallengeBudgetAndClosesClient(t *testing.T) {
	repo := newCallbackFakeRepo()
	transport := &callbackNetworkTransport{}
	svc := newCallbackTestService(repo, CallbackConfig{RequestTimeout: time.Second}, transport)
	var deadline time.Time
	svc.network.lookupIP = callbackBudgetResolver(t, &deadline)
	transport.respond = func(request *http.Request) (*http.Response, error) {
		got, ok := request.Context().Deadline()
		if !ok || !got.Equal(deadline) {
			t.Error("challenge request renewed DNS validation's original budget")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(request.Header.Get("X-Aero-Callback-Challenge")))}, nil
	}
	sub, secret, err := svc.Register(replayIdentityContext(), CallbackInput{Endpoint: callbackTestEndpoint})
	if err != nil || sub == nil || secret == "" || len(repo.subs) != 1 || len(repo.facts) != 1 {
		t.Fatalf("registration lost its existing audited contract: error=%v subs=%d facts=%d", err, len(repo.subs), len(repo.facts))
	}
	if transport.calls.Load() != 1 || transport.closes.Load() != 1 || deadline.IsZero() {
		t.Fatalf("registration client was not owned/closed: calls=%d closes=%d", transport.calls.Load(), transport.closes.Load())
	}
}

func TestCallbackNetworkDeliverySharesLeaseDNSAndTransportBudget(t *testing.T) {
	delivery := callbackDelivery(callbackTestEndpoint, 7)
	repo := &callbackSendProbeRepo{callbackFakeRepo: newCallbackFakeRepo(delivery)}
	transport := &callbackNetworkTransport{}
	svc := newCallbackTestService(repo, CallbackConfig{RequestTimeout: time.Second}, transport)
	var deadline time.Time
	repo.check = func(ctx context.Context, _ *domain.CallbackDelivery, _ int) error {
		got, ok := ctx.Deadline()
		if !ok {
			t.Error("lease check has no deadline")
		}
		if deadline.IsZero() {
			deadline = got
		} else if !deadline.Equal(got) {
			t.Error("lease check renewed the delivery's original budget")
		}
		return nil
	}
	svc.network.lookupIP = callbackBudgetResolver(t, &deadline)
	transport.respond = func(request *http.Request) (*http.Response, error) {
		got, ok := request.Context().Deadline()
		if !ok || !got.Equal(deadline) {
			t.Error("transport renewed the delivery's original budget")
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("accepted"))}, nil
	}
	svc.drain(t.Context())
	if repo.count() != 2 || len(repo.marked) != 1 || repo.marked[0].status != "success" || transport.closes.Load() != 1 {
		t.Fatalf("guarded send/cleanup failed: checks=%d marks=%v closes=%d", repo.count(), repo.marked, transport.closes.Load())
	}
}

func TestCallbackNetworkDNSDeadlineReturnsWithoutAdmittingTransport(t *testing.T) {
	for _, registration := range []bool{false, true} {
		delivery := callbackDelivery(callbackTestEndpoint, 7)
		repo := newCallbackFakeRepo(delivery)
		transport := &callbackNetworkTransport{respond: func(*http.Request) (*http.Response, error) {
			t.Error("expired DNS admitted HTTP transport")
			return nil, context.DeadlineExceeded
		}}
		svc := newCallbackTestService(repo, CallbackConfig{RequestTimeout: 20 * time.Millisecond}, transport)
		svc.network.lookupIP = func(ctx context.Context, _, _ string) ([]net.IP, error) {
			<-ctx.Done()
			return []net.IP{net.ParseIP("8.8.8.8")}, nil // A late answer must not be adopted.
		}
		if registration {
			sub, secret, err := svc.Register(replayIdentityContext(), CallbackInput{Endpoint: callbackTestEndpoint})
			if err == nil || sub != nil || secret != "" || len(repo.facts) != 0 || len(repo.subs) != 0 {
				t.Fatal("late DNS registered a subscription or exposed a secret")
			}
		} else {
			svc.drain(t.Context())
			// Preserve the existing network-failure retry policy. This is not
			// an explicit lease-check refusal, nor proof about older effects.
			if len(repo.marked) != 1 || repo.marked[0].status != "dead_letter" {
				t.Fatalf("DNS failure changed the existing bounded retry contract: %v", repo.marked)
			}
		}
		if transport.calls.Load() != 0 {
			t.Fatalf("expired DNS sent %d requests", transport.calls.Load())
		}
	}
}

type callbackDeadlineBody struct {
	ctx    context.Context
	reader *strings.Reader
	closes atomic.Int32
}

func (b *callbackDeadlineBody) Read(value []byte) (int, error) {
	<-b.ctx.Done()
	return b.reader.Read(value)
}

func (b *callbackDeadlineBody) Close() error { b.closes.Add(1); return nil }

func TestCallbackNetworkRejectsLateChallengeAndDeliveryBodyAndClosesBoth(t *testing.T) {
	for _, registration := range []bool{false, true} {
		delivery := callbackDelivery(callbackTestEndpoint, 7)
		repo := newCallbackFakeRepo(delivery)
		transport := &callbackNetworkTransport{}
		svc := newCallbackTestService(repo, CallbackConfig{RequestTimeout: 20 * time.Millisecond, MaxAttempts: 9}, transport)
		var deadline time.Time
		svc.network.lookupIP = callbackBudgetResolver(t, &deadline)
		var body *callbackDeadlineBody
		transport.respond = func(request *http.Request) (*http.Response, error) {
			value := "accepted"
			if registration {
				value = request.Header.Get("X-Aero-Callback-Challenge")
			}
			body = &callbackDeadlineBody{ctx: request.Context(), reader: strings.NewReader(value)}
			return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
		}
		if registration {
			sub, secret, err := svc.Register(replayIdentityContext(), CallbackInput{Endpoint: callbackTestEndpoint})
			if err == nil || sub != nil || secret != "" || len(repo.facts) != 0 || len(repo.subs) != 0 {
				t.Fatal("late valid challenge granted registration")
			}
		} else {
			svc.drain(t.Context())
			if len(repo.marked) != 1 || repo.marked[0].status != "pending" {
				t.Fatalf("late 2xx body falsely settled success: %v", repo.marked)
			}
		}
		if body == nil || body.closes.Load() != 1 || transport.closes.Load() != 1 || transport.calls.Load() != 1 {
			t.Fatal("deadline response/client was not closed exactly once")
		}
	}
}
