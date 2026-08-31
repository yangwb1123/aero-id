package auditgovernance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const tokenFlightWatchdog = 5 * time.Second

type waitSelectContext struct {
	context.Context
	reached chan<- struct{}
	once    sync.Once
}

func (c *waitSelectContext) Done() <-chan struct{} {
	c.once.Do(func() { c.reached <- struct{}{} })
	return c.Context.Done()
}

func awaitTokenFlightSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(tokenFlightWatchdog):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func TestAccessTokenLeaderCancellationDoesNotCancelSharedFlight(t *testing.T) {
	const tenantID = "tenant-a"
	var tokenPosts atomic.Int32
	requestCanceled := make(chan bool, 1)
	tokenArrived := make(chan struct{})
	var arriveOnce sync.Once
	release := make(chan struct{})
	var releaseOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		tokenPosts.Add(1)
		arriveOnce.Do(func() { close(tokenArrived) })
		select {
		case <-release:
			requestCanceled <- false
		case <-r.Context().Done():
			requestCanceled <- true
			<-release
			return
		}
		tokenHandler(w, "service-token", "")
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })

	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}

	leaderBase, cancelLeader := context.WithCancel(t.Context())
	leaderWait := make(chan struct{}, 1)
	leaderCtx := &waitSelectContext{Context: leaderBase, reached: leaderWait}
	leaderResult := make(chan error, 1)
	go func() {
		_, leaderErr := tokens.AccessToken(leaderCtx, binding)
		leaderResult <- leaderErr
	}()
	awaitTokenFlightSignal(t, tokenArrived, "leader token request")
	awaitTokenFlightSignal(t, leaderWait, "leader wait select")

	followerWait := make(chan struct{}, 1)
	followerCtx := &waitSelectContext{Context: t.Context(), reached: followerWait}
	followerResult := make(chan error, 1)
	go func() {
		_, followerErr := tokens.AccessToken(followerCtx, binding)
		followerResult <- followerErr
	}()
	awaitTokenFlightSignal(t, followerWait, "follower wait select")

	cancelLeader()
	select {
	case leaderErr := <-leaderResult:
		if !errors.Is(leaderErr, context.Canceled) {
			t.Fatalf("leader error = %v, want context.Canceled", leaderErr)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("leader did not observe its cancellation")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case followerErr := <-followerResult:
		if followerErr != nil {
			t.Fatalf("follower error = %v, want nil", followerErr)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("shared flight did not complete for follower")
	}
	select {
	case canceled := <-requestCanceled:
		if canceled {
			t.Fatal("leader cancellation canceled the shared token request")
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("token handler did not report request state")
	}
	if got := tokenPosts.Load(); got != 1 {
		t.Fatalf("token posts = %d, want 1", got)
	}
	if token, ok := tokens.bindings[tenantID].cachedToken(); !ok || token != "service-token" {
		t.Fatalf("shared cache = %q/%v, want service-token", token, ok)
	}
}

func TestAccessTokenCanceledWaiterDoesNotForgetSharedFlight(t *testing.T) {
	const tenantID = "tenant-a"
	var tokenPosts atomic.Int32
	tokenArrived := make(chan struct{})
	var arriveOnce sync.Once
	release := make(chan struct{})
	var releaseOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if tokenPosts.Add(1) == 1 {
			arriveOnce.Do(func() { close(tokenArrived) })
		}
		<-release
		tokenHandler(w, "service-token", "")
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })

	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}

	leaderWait := make(chan struct{}, 1)
	leaderCtx := &waitSelectContext{Context: t.Context(), reached: leaderWait}
	leaderResult := make(chan error, 1)
	go func() {
		_, leaderErr := tokens.AccessToken(leaderCtx, binding)
		leaderResult <- leaderErr
	}()
	awaitTokenFlightSignal(t, tokenArrived, "leader token request")
	awaitTokenFlightSignal(t, leaderWait, "leader wait select")

	followerBase, cancelFollower := context.WithCancel(t.Context())
	followerWait := make(chan struct{}, 1)
	followerCtx := &waitSelectContext{Context: followerBase, reached: followerWait}
	followerResult := make(chan error, 1)
	go func() {
		_, followerErr := tokens.AccessToken(followerCtx, binding)
		followerResult <- followerErr
	}()
	awaitTokenFlightSignal(t, followerWait, "canceled follower wait select")
	cancelFollower()
	select {
	case followerErr := <-followerResult:
		if !errors.Is(followerErr, context.Canceled) {
			t.Fatalf("follower error = %v, want context.Canceled", followerErr)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("canceled follower did not return")
	}

	lateWait := make(chan struct{}, 1)
	lateCtx := &waitSelectContext{Context: t.Context(), reached: lateWait}
	lateResult := make(chan struct {
		token string
		err   error
	}, 1)
	go func() {
		token, lateErr := tokens.AccessToken(lateCtx, binding)
		lateResult <- struct {
			token string
			err   error
		}{token: token, err: lateErr}
	}()
	awaitTokenFlightSignal(t, lateWait, "late caller wait select")

	releaseOnce.Do(func() { close(release) })
	select {
	case leaderErr := <-leaderResult:
		if leaderErr != nil {
			t.Fatalf("leader error = %v, want nil", leaderErr)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("leader did not receive the shared result")
	}
	select {
	case late := <-lateResult:
		if late.err != nil || late.token != "service-token" {
			t.Fatalf("late caller result = %q, %v; want shared token", late.token, late.err)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("late caller did not receive the shared result")
	}
	if got := tokenPosts.Load(); got != 1 {
		t.Fatalf("token posts = %d, want 1 after a canceled waiter", got)
	}
}

func TestEpochKeyAllowsConcurrentReentryBeforeStaleFlightCompletes(t *testing.T) {
	const tenantID = "tenant-a"
	var tokenPosts atomic.Int32
	firstArrived := make(chan struct{})
	secondArrived := make(chan struct{})
	var firstOnce, secondOnce sync.Once
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		switch tokenPosts.Add(1) {
		case 1:
			firstOnce.Do(func() { close(firstArrived) })
			<-releaseFirst
			tokenHandler(w, "service-token-stale", "")
		case 2:
			secondOnce.Do(func() { close(secondArrived) })
			tokenHandler(w, "service-token-2", "")
		}
	}))
	defer server.Close()
	defer releaseFirstOnce.Do(func() { close(releaseFirst) })

	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}
	firstResult := make(chan struct {
		token string
		err   error
	}, 1)
	go func() {
		token, callErr := tokens.AccessToken(t.Context(), binding)
		firstResult <- struct {
			token string
			err   error
		}{token: token, err: callErr}
	}()
	awaitTokenFlightSignal(t, firstArrived, "epoch-zero token request")

	if err := tokens.Invalidate(binding); err != nil {
		t.Fatal(err)
	}
	secondResult := make(chan struct {
		token string
		err   error
	}, 1)
	go func() {
		token, callErr := tokens.AccessToken(t.Context(), binding)
		secondResult <- struct {
			token string
			err   error
		}{token: token, err: callErr}
	}()
	awaitTokenFlightSignal(t, secondArrived, "epoch-one token request")
	select {
	case result := <-secondResult:
		if result.err != nil || result.token != "service-token-2" {
			t.Fatalf("epoch-one result = %q, %v; want service-token-2", result.token, result.err)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("epoch-one flight did not complete")
	}

	releaseFirstOnce.Do(func() { close(releaseFirst) })
	select {
	case result := <-firstResult:
		if result.err != nil || result.token != "service-token-2" {
			t.Fatalf("stale caller result = %q, %v; want current token", result.token, result.err)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("stale caller did not re-enter after epoch discard")
	}
	if got := tokenPosts.Load(); got != 2 {
		t.Fatalf("token posts = %d, want two independent epoch flights", got)
	}
}

func TestAccessTokenAllCallersCancelFlightCachesToken(t *testing.T) {
	const tenantID = "tenant-a"
	var tokenPosts atomic.Int32
	tokenArrived := make(chan struct{})
	var arriveOnce sync.Once
	release := make(chan struct{})
	var releaseOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if tokenPosts.Add(1) == 1 {
			arriveOnce.Do(func() { close(tokenArrived) })
		}
		<-release
		tokenHandler(w, "service-token", "")
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })

	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}

	firstBase, cancelFirst := context.WithCancel(t.Context())
	secondBase, cancelSecond := context.WithCancel(t.Context())
	firstWait := make(chan struct{}, 1)
	secondWait := make(chan struct{}, 1)
	firstCtx := &waitSelectContext{Context: firstBase, reached: firstWait}
	secondCtx := &waitSelectContext{Context: secondBase, reached: secondWait}
	results := make(chan error, 2)
	go func() { _, callErr := tokens.AccessToken(firstCtx, binding); results <- callErr }()
	awaitTokenFlightSignal(t, tokenArrived, "first token request")
	awaitTokenFlightSignal(t, firstWait, "first wait select")
	go func() { _, callErr := tokens.AccessToken(secondCtx, binding); results <- callErr }()
	awaitTokenFlightSignal(t, secondWait, "second wait select")

	cancelFirst()
	cancelSecond()
	for range 2 {
		select {
		case callErr := <-results:
			if !errors.Is(callErr, context.Canceled) {
				t.Fatalf("canceled caller error = %v, want context.Canceled", callErr)
			}
		case <-time.After(tokenFlightWatchdog):
			t.Fatal("canceled caller did not return")
		}
	}

	releaseOnce.Do(func() { close(release) })
	lateToken, lateErr := tokens.AccessToken(t.Context(), binding)
	if lateErr != nil || lateToken != "service-token" {
		t.Fatalf("late token = %q, %v; want cached shared token", lateToken, lateErr)
	}
	if got := tokenPosts.Load(); got != 1 {
		t.Fatalf("token posts = %d, want 1 after all callers canceled", got)
	}
}

type tokenFlightTransport struct {
	requests    atomic.Int32
	readStarted chan struct{}
	bodyClosed  chan struct{}
	deadline    chan time.Time
}

func (t *tokenFlightTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.requests.Add(1) == 1 {
		if deadline, ok := request.Context().Deadline(); ok {
			t.deadline <- deadline
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: &contextBlockingBody{
				ctx: request.Context(), started: t.readStarted, closed: t.bodyClosed,
			},
			Request: request,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"access_token":"service-token-2","token_type":"Bearer","expires_in":300}`)),
		Request:    request,
	}, nil
}

type contextBlockingBody struct {
	ctx     context.Context
	started chan struct{}
	closed  chan struct{}
	start   sync.Once
	close   sync.Once
}

func (b *contextBlockingBody) Read([]byte) (int, error) {
	b.start.Do(func() { close(b.started) })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *contextBlockingBody) Close() error {
	b.close.Do(func() { close(b.closed) })
	return nil
}

type delayedScopeBody struct {
	ctx     context.Context
	payload []byte
	started chan struct{}
	closed  chan struct{}
	start   sync.Once
	close   sync.Once
	sent    bool
}

func (b *delayedScopeBody) Read(p []byte) (int, error) {
	b.start.Do(func() { close(b.started) })
	if !b.sent {
		<-b.ctx.Done()
		b.sent = true
		return copy(p, b.payload), nil
	}
	return 0, io.EOF
}

func (b *delayedScopeBody) Close() error {
	b.close.Do(func() { close(b.closed) })
	return nil
}

type scopeTimeoutTransport struct {
	requests  atomic.Int32
	bodyReady chan struct{}
	body      *delayedScopeBody
}

func (t *scopeTimeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.requests.Add(1) != 1 {
		return nil, errors.New("unexpected second token request")
	}
	t.body = &delayedScopeBody{
		ctx: request.Context(), payload: []byte(`{"error":"invalid_scope"}`),
		started: make(chan struct{}), closed: make(chan struct{}),
	}
	close(t.bodyReady)
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       t.body, Request: request,
	}, nil
}

func TestAccessTokenTimeoutPrecedesTokenScopeClassification(t *testing.T) {
	const tenantID = "tenant-a"
	var hookCalls atomic.Int32
	transport := &scopeTimeoutTransport{bodyReady: make(chan struct{})}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", Timeout: 50 * time.Millisecond,
		Bindings: map[string]OAuthBindingConfig{tenantID: {
			TokenURL: "https://oauth.example.test/token", ClientID: "client", ClientSecret: "secret",
			Scope: auditScope, Resources: []string{auditResource},
		}},
		OnScopeRejected: func(string) { hookCalls.Add(1) },
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	binding := SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}
	result := make(chan error, 1)
	go func() {
		_, callErr := tokens.AccessToken(t.Context(), binding)
		result <- callErr
	}()
	awaitTokenFlightSignal(t, transport.bodyReady, "scope response body")
	awaitTokenFlightSignal(t, transport.body.started, "scope response body read")
	select {
	case callErr := <-result:
		if !errors.Is(callErr, ErrTokenUnavailable) || errors.Is(callErr, ErrTokenScopeMissing) ||
			errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
			t.Fatalf("timed-out scope response error = %v, want sanitized ErrTokenUnavailable", callErr)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("timed-out scope response did not complete")
	}
	awaitTokenFlightSignal(t, transport.body.closed, "scope response body close")
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("scope hook calls = %d, want 0 after flight timeout", got)
	}
}

func TestAccessTokenFlightUsesOAuthTimeout(t *testing.T) {
	const tenantID = "tenant-a"
	flightTimeout := 100 * time.Millisecond
	transport := &tokenFlightTransport{
		readStarted: make(chan struct{}), bodyClosed: make(chan struct{}), deadline: make(chan time.Time, 1),
	}
	client := &http.Client{Transport: transport, Timeout: 10 * flightTimeout}
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", Timeout: flightTimeout,
		Bindings: map[string]OAuthBindingConfig{tenantID: {
			TokenURL: "https://oauth.example.test/token", ClientID: "client", ClientSecret: "secret",
			Scope: auditScope, Resources: []string{auditResource},
		}},
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	binding := SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}
	result := make(chan error, 1)
	go func() {
		_, callErr := tokens.AccessToken(t.Context(), binding)
		result <- callErr
	}()
	awaitTokenFlightSignal(t, transport.readStarted, "blocked token body read")
	select {
	case deadline := <-transport.deadline:
		if deadline.IsZero() || time.Until(deadline) > flightTimeout {
			t.Fatalf("request deadline = %v, exceeds OAuth flight timeout", deadline)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("token request deadline was not recorded")
	}
	select {
	case callErr := <-result:
		if !errors.Is(callErr, ErrTokenUnavailable) || errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
			t.Fatalf("timed-out token error = %v, want sanitized ErrTokenUnavailable", callErr)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("OAuth flight did not terminate at its configured deadline")
	}
	awaitTokenFlightSignal(t, transport.bodyClosed, "timed-out token body close")
	if token, ok := tokens.bindings[tenantID].cachedToken(); ok || token != "" {
		t.Fatalf("timed-out flight cached token %q/%v", token, ok)
	}

	token, callErr := tokens.AccessToken(t.Context(), binding)
	if callErr != nil || token != "service-token-2" {
		t.Fatalf("retry token = %q, %v; want second request token", token, callErr)
	}
	if got := transport.requests.Load(); got != 2 {
		t.Fatalf("token requests = %d, want 2 after timeout", got)
	}
}

type tokenRoundTripFunc func(*http.Request) (*http.Response, error)

func (f tokenRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newFlightSource(t *testing.T, tokenURL string, timeout time.Duration, client *http.Client, hook func(string)) (*OAuthTokenSource, *tenantTokenSource) {
	t.Helper()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", Timeout: timeout, AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{"tenant-a": {
			TokenURL: tokenURL, ClientID: "client", ClientSecret: "secret",
			Scope: auditScope, Resources: []string{auditResource},
		}},
		OnScopeRejected: hook,
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	return tokens, tokens.bindings["tenant-a"]
}

type cancelAfterReadBody struct {
	reader *bytes.Reader
	cancel context.CancelFunc
}

func (b *cancelAfterReadBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if n > 0 {
		b.cancel()
	}
	return n, err
}

func (b *cancelAfterReadBody) Close() error { return nil }

func TestTokenFlightBodyCancellationPrecedesScopeClassification(t *testing.T) {
	var hookCalls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: tokenRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: &cancelAfterReadBody{
				reader: bytes.NewReader([]byte(`{"error":"invalid_scope"}`)), cancel: cancel,
			}, Request: request,
		}, nil
	})}
	_, source := newFlightSource(t, "http://127.0.0.1/token", time.Second, client, func(string) {
		hookCalls.Add(1)
	})

	_, err := source.fetchToken(ctx, 0)
	if !errors.Is(err, errTokenFlightExpired) {
		t.Fatalf("fetch error = %v, want errTokenFlightExpired", err)
	}
	if errors.Is(err, ErrTokenScopeMissing) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("body cancellation was misclassified: %v", err)
	}
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("scope hook calls = %d, want 0 after canceled body read", got)
	}
}

type failingTokenBody struct {
	reader *bytes.Reader
	err    error
}

func (b *failingTokenBody) Read(p []byte) (int, error) {
	n, _ := b.reader.Read(p)
	if n > 0 {
		return n, b.err
	}
	return 0, io.EOF
}

func (b *failingTokenBody) Close() error { return nil }

func TestTokenFlightBodyReadErrorDoesNotInvokeScopeHook(t *testing.T) {
	const marker = "body-read-secret"
	var hookCalls atomic.Int32
	client := &http.Client{Transport: tokenRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body: &failingTokenBody{
				reader: bytes.NewReader([]byte(`{"error":"invalid_scope"}`)),
				err:    errors.New(marker),
			}, Request: request,
		}, nil
	})}
	_, source := newFlightSource(t, "http://127.0.0.1/token", time.Second, client, func(string) {
		hookCalls.Add(1)
	})

	_, err := source.fetchToken(context.Background(), 0)
	if !errors.Is(err, ErrTokenUnavailable) || errors.Is(err, ErrTokenScopeMissing) {
		t.Fatalf("body-read error = %v, want only ErrTokenUnavailable", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("body-read error leaked marker: %v", err)
	}
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("scope hook calls = %d, want 0 after body-read error", got)
	}
}

func TestTokenFlightCancellationDuringScopeHookWins(t *testing.T) {
	var hookCalls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: tokenRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"access_token":"service-token","token_type":"Bearer","expires_in":300,"scope":"audit:event:read"}`,
			)), Request: request,
		}, nil
	})}
	_, source := newFlightSource(t, "http://127.0.0.1/token", time.Second, client, func(string) {
		hookCalls.Add(1)
		cancel()
	})

	_, err := source.fetchToken(ctx, 0)
	if !errors.Is(err, errTokenFlightExpired) {
		t.Fatalf("hook cancellation error = %v, want errTokenFlightExpired", err)
	}
	if errors.Is(err, ErrTokenScopeMissing) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hook cancellation was misclassified: %v", err)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("scope hook calls = %d, want 1", got)
	}
	if source.token != "" || !source.refreshAt.IsZero() {
		t.Fatalf("hook cancellation populated cache: token=%q refreshAt=%v", source.token, source.refreshAt)
	}
}

func TestTokenFlightDeadlineExpiryDoesNotEscapeOrMutateCache(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	source := &tenantTokenSource{
		refreshSkew: 30 * time.Second,
		now:         func() time.Time { return time.Unix(100, 0).UTC() },
	}
	err := source.cacheToken(tokenResponse{AccessToken: "expired", TokenType: "Bearer", ExpiresIn: 300}, 0, ctx)
	if !errors.Is(err, errTokenFlightExpired) {
		t.Fatalf("deadline cache error = %v, want errTokenFlightExpired", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context deadline escaped cache classification: %v", err)
	}
	if source.token != "" || !source.refreshAt.IsZero() {
		t.Fatalf("deadline expiry mutated cache: token=%q refreshAt=%v", source.token, source.refreshAt)
	}
}

func TestTokenFetchTransportErrorsAreSanitized(t *testing.T) {
	const marker = "oauth-url https://oauth.example.test/token client-secret access-token response-body"
	client := &http.Client{Transport: tokenRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(marker)
	})}
	_, source := newFlightSource(t, "https://oauth.example.test/token", time.Second, client, nil)

	_, err := source.accessToken(context.Background())
	if !errors.Is(err, ErrTokenUnavailable) {
		t.Fatalf("transport error = %v, want ErrTokenUnavailable", err)
	}
	if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "oauth.example.test") || strings.Contains(err.Error(), "client-secret") {
		t.Fatalf("transport error leaked sensitive detail: %v", err)
	}
}

func TestOAuthAndPublisherPreserveClientTimeoutSemantics(t *testing.T) {
	const tokenURL = "https://oauth.example.test/token" //nolint:gosec // documentation-only test endpoint
	longTimeout := 17 * time.Second
	flightTimeout := 100 * time.Millisecond
	client := &http.Client{Timeout: longTimeout}
	_, source := newFlightSource(t, tokenURL, flightTimeout, client, nil)
	if source.client.Timeout != longTimeout {
		t.Fatalf("OAuth client timeout = %s, want supplied %s", source.client.Timeout, longTimeout)
	}
	if source.flightTimeout != flightTimeout {
		t.Fatalf("OAuth flight timeout = %s, want configured %s", source.flightTimeout, flightTimeout)
	}

	publisher, err := NewPublisher(Config{
		Endpoint: "https://events.example.test", SourcePrefix: "aero-id", Timeout: flightTimeout,
	}, fixedPublisherTokenSource{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if publisher.client.Timeout != longTimeout {
		t.Fatalf("Publisher client timeout = %s, want supplied %s", publisher.client.Timeout, longTimeout)
	}

	shortClient := &http.Client{Timeout: 25 * time.Millisecond}
	_, shortSource := newFlightSource(t, tokenURL, time.Second, shortClient, nil)
	if shortSource.client.Timeout != shortClient.Timeout {
		t.Fatalf("short OAuth client timeout = %s, want supplied %s", shortSource.client.Timeout, shortClient.Timeout)
	}
}

func TestPublisherRetainsLegacyContextErrorCollapse(t *testing.T) {
	publisher, err := NewPublisher(Config{
		Endpoint: "https://events.example.test", SourcePrefix: "aero-id",
	}, fixedPublisherTokenSource{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = publisher.Publish(ctx, scopeFact("tenant-a"))
	if !errors.Is(err, ErrTokenUnavailable) || errors.Is(err, context.Canceled) {
		t.Fatalf("Publisher canceled request error = %v, want only ErrTokenUnavailable", err)
	}
}

func TestPublisherConfigTimeoutDoesNotOverridePositiveClientTimeout(t *testing.T) {
	fact := scopeFact("tenant-a")
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events" {
			http.NotFound(w, r)
			return
		}
		close(started)
		<-release
		acceptedReceiptHandler(w, fact)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	publisher, err := NewPublisher(Config{
		Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Timeout: time.Nanosecond,
	}, fixedPublisherTokenSource{}, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- publisher.Publish(t.Context(), fact) }()
	awaitTokenFlightSignal(t, started, "Publisher event request")
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Publisher error = %v, want nil with positive client timeout", err)
		}
	case <-time.After(tokenFlightWatchdog):
		t.Fatal("Publisher did not complete after event response release")
	}
}

func TestOAuthTimeoutDefaultsForZeroAndNegativeValues(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		if got := defaultOAuthConfig(OAuthConfig{Timeout: timeout}).Timeout; got != 5*time.Second {
			t.Fatalf("Timeout %s normalized to %s, want 5s", timeout, got)
		}
	}
}

func TestCacheTokenRejectsExpiredFlightWithoutMutation(t *testing.T) {
	source := &tenantTokenSource{
		refreshSkew: 30 * time.Second,
		now:         func() time.Time { return time.Unix(100, 0).UTC() },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := source.cacheToken(tokenResponse{AccessToken: "expired", TokenType: "Bearer", ExpiresIn: 300}, 0, ctx)
	if !errors.Is(err, errTokenFlightExpired) {
		t.Fatalf("cache error = %v, want errTokenFlightExpired", err)
	}
	if source.token != "" || !source.refreshAt.IsZero() {
		t.Fatalf("expired flight mutated cache: token=%q refreshAt=%v", source.token, source.refreshAt)
	}
}

func TestCacheTokenEpochFencePrecedesFlightExpiry(t *testing.T) {
	source := &tenantTokenSource{
		epoch:       1,
		refreshSkew: 30 * time.Second,
		now:         func() time.Time { return time.Unix(100, 0).UTC() },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := source.cacheToken(tokenResponse{AccessToken: "stale", TokenType: "Bearer", ExpiresIn: 300}, 0, ctx)
	if !errors.Is(err, errEpochDiscarded) {
		t.Fatalf("cache error = %v, want errEpochDiscarded", err)
	}
	if source.token != "" || !source.refreshAt.IsZero() {
		t.Fatalf("stale epoch mutated cache: token=%q refreshAt=%v", source.token, source.refreshAt)
	}
}

type cancelOnSecondErrorContext struct {
	context.Context
	done  chan struct{}
	once  sync.Once
	calls atomic.Int32
}

func (c *cancelOnSecondErrorContext) Done() <-chan struct{} { return c.done }

func (c *cancelOnSecondErrorContext) Err() error {
	if c.calls.Add(1) >= 2 {
		c.once.Do(func() { close(c.done) })
		return context.Canceled
	}
	return nil
}

func TestCacheTokenClearsAssignmentWhenFlightExpires(t *testing.T) {
	ctx := &cancelOnSecondErrorContext{Context: context.Background(), done: make(chan struct{})}
	source := &tenantTokenSource{
		refreshSkew: 30 * time.Second,
		now:         func() time.Time { return time.Unix(100, 0).UTC() },
	}
	err := source.cacheToken(tokenResponse{AccessToken: "raced", TokenType: "Bearer", ExpiresIn: 300}, 0, ctx)
	if !errors.Is(err, errTokenFlightExpired) {
		t.Fatalf("cache error = %v, want errTokenFlightExpired", err)
	}
	if source.token != "" || !source.refreshAt.IsZero() {
		t.Fatalf("raced expiry left cache populated: token=%q refreshAt=%v", source.token, source.refreshAt)
	}
}
