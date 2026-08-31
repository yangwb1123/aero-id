package billing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenSourceLeaderCancellationDoesNotCancelFollower(t *testing.T) {
	const scope = ScopeEntitlementRead
	requestArrived := make(chan struct{})
	checkRequestContext := make(chan struct{})
	requestActive := make(chan struct{})
	requestCanceled := make(chan struct{})
	releaseRequest := make(chan struct{})
	var requestOnce, activeOnce, canceledOnce, releaseOnce sync.Once
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/token" {
			http.NotFound(writer, request)
			return
		}
		if tokenCalls.Add(1) != 1 {
			t.Errorf("unexpected token request")
		}
		requestOnce.Do(func() { close(requestArrived) })
		<-checkRequestContext
		select {
		case <-request.Context().Done():
			canceledOnce.Do(func() { close(requestCanceled) })
		default:
			activeOnce.Do(func() { close(requestActive) })
		}
		<-releaseRequest
		writeJSON(t, writer, http.StatusOK, billingTokenResponse("shared-token"))
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(releaseRequest) })

	source := newBillingOAuthTestSource(t, server.URL+"/token", time.Second, server.Client())
	leaderBase, cancelLeader := context.WithCancel(t.Context())
	defer cancelLeader()
	leaderWait := make(chan struct{}, 1)
	leaderResult := make(chan billingCancellationSnapshotResult, 1)
	go func() {
		snapshot, err := source.accessTokenSnapshot(newBillingCancellationWaiterContext(leaderBase, leaderWait), scope)
		leaderResult <- billingCancellationSnapshotResult{snapshot: snapshot, err: err}
	}()
	waitBillingSignal(t, leaderWait, "leader waiter")
	waitBillingSignal(t, requestArrived, "leader token request")

	followerWait := make(chan struct{}, 1)
	followerResult := make(chan billingCancellationSnapshotResult, 1)
	go func() {
		snapshot, err := source.accessTokenSnapshot(newBillingCancellationWaiterContext(t.Context(), followerWait), scope)
		followerResult <- billingCancellationSnapshotResult{snapshot: snapshot, err: err}
	}()
	waitBillingSignal(t, followerWait, "follower waiter")

	cancelLeader()
	select {
	case result := <-leaderResult:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("leader error = %v, want context.Canceled", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled leader")
	}
	close(checkRequestContext)
	select {
	case <-requestActive:
	case <-requestCanceled:
		t.Fatal("leader cancellation canceled the shared token request")
	case <-time.After(time.Second):
		t.Fatal("timed out checking shared token request context")
	}
	releaseOnce.Do(func() { close(releaseRequest) })
	select {
	case result := <-followerResult:
		if result.err != nil || result.snapshot.value != "shared-token" {
			t.Fatalf("follower result = %q, err=%v, want shared-token", result.snapshot.value, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for follower")
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
}

func TestTokenSourceCanceledWaiterDoesNotForgetFlight(t *testing.T) {
	const scope = ScopeEntitlementRead
	requestArrived := make(chan struct{})
	releaseRequest := make(chan struct{})
	var requestOnce, releaseOnce sync.Once
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/token" {
			http.NotFound(writer, request)
			return
		}
		if tokenCalls.Add(1) == 1 {
			requestOnce.Do(func() { close(requestArrived) })
		}
		<-releaseRequest
		writeJSON(t, writer, http.StatusOK, billingTokenResponse("shared-token"))
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(releaseRequest) })

	source := newBillingOAuthTestSource(t, server.URL+"/token", time.Second, server.Client())
	leaderWait := make(chan struct{}, 1)
	leaderResult := make(chan billingCancellationSnapshotResult, 1)
	go func() {
		snapshot, err := source.accessTokenSnapshot(newBillingCancellationWaiterContext(t.Context(), leaderWait), scope)
		leaderResult <- billingCancellationSnapshotResult{snapshot: snapshot, err: err}
	}()
	waitBillingSignal(t, leaderWait, "leader waiter")
	waitBillingSignal(t, requestArrived, "leader token request")

	followerBase, cancelFollower := context.WithCancel(t.Context())
	followerWait := make(chan struct{}, 1)
	followerResult := make(chan billingCancellationSnapshotResult, 1)
	go func() {
		snapshot, err := source.accessTokenSnapshot(newBillingCancellationWaiterContext(followerBase, followerWait), scope)
		followerResult <- billingCancellationSnapshotResult{snapshot: snapshot, err: err}
	}()
	waitBillingSignal(t, followerWait, "canceling waiter")
	cancelFollower()
	select {
	case result := <-followerResult:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled waiter error = %v, want context.Canceled", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled waiter")
	}

	lateWait := make(chan struct{}, 1)
	lateResult := make(chan billingCancellationSnapshotResult, 1)
	go func() {
		snapshot, err := source.accessTokenSnapshot(newBillingCancellationWaiterContext(t.Context(), lateWait), scope)
		lateResult <- billingCancellationSnapshotResult{snapshot: snapshot, err: err}
	}()
	waitBillingSignal(t, lateWait, "late waiter")
	releaseOnce.Do(func() { close(releaseRequest) })

	for name, resultChannel := range map[string]<-chan billingCancellationSnapshotResult{
		"leader":      leaderResult,
		"late waiter": lateResult,
	} {
		select {
		case result := <-resultChannel:
			if result.err != nil || result.snapshot.value != "shared-token" {
				t.Fatalf("%s result = %q, err=%v, want shared-token", name, result.snapshot.value, result.err)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", name)
		}
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1 after canceled waiter", got)
	}
}

func TestTokenSourceAllWaitersCancelCanCommitSuccessfulFlight(t *testing.T) {
	const scope = ScopeEntitlementRead
	releaseRequest := make(chan struct{})
	requestStarted := make(chan struct{})
	var releaseOnce sync.Once
	transport := &gatedTokenRoundTripper{release: releaseRequest, started: requestStarted}
	source := newBillingOAuthTestSource(t, "http://127.0.0.1/token", time.Second,
		&http.Client{Transport: transport, Timeout: 2 * time.Second})
	defer releaseOnce.Do(func() { close(releaseRequest) })
	firstBase, cancelFirst := context.WithCancel(t.Context())
	secondBase, cancelSecond := context.WithCancel(t.Context())
	firstWait := make(chan struct{}, 1)
	secondWait := make(chan struct{}, 1)
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	go func() {
		_, err := source.accessToken(newBillingCancellationWaiterContext(firstBase, firstWait), scope)
		firstResult <- err
	}()
	waitBillingSignal(t, firstWait, "first waiter")
	waitBillingSignal(t, requestStarted, "shared token request")
	go func() {
		_, err := source.accessToken(newBillingCancellationWaiterContext(secondBase, secondWait), scope)
		secondResult <- err
	}()
	waitBillingSignal(t, secondWait, "second waiter")

	cancelFirst()
	cancelSecond()
	for index, resultChannel := range []<-chan error{firstResult, secondResult} {
		select {
		case err := <-resultChannel:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("waiter %d error = %v, want context.Canceled", index, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for waiter %d", index)
		}
	}
	releaseOnce.Do(func() { close(releaseRequest) })
	if token, err := source.accessToken(t.Context(), scope); err != nil || token != "shared-token" {
		t.Fatalf("post-cancellation token = %q, err=%v, want shared-token", token, err)
	}
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1 after successful shared flight", got)
	}
	if token, ok := source.cached(scope); !ok || token != "shared-token" {
		t.Fatalf("successful flight cache = %q/%v, want shared-token/true", token, ok)
	}
}

func TestTokenSourceAllWaitersCancelCleansUpFlight(t *testing.T) {
	const (
		scope         = ScopeEntitlementRead
		flightTimeout = 100 * time.Millisecond
	)
	requestStarted := make(chan struct{})
	requestStopped := make(chan struct{})
	transport := &allWaitersTokenRoundTripper{requestStarted: requestStarted, requestStopped: requestStopped}
	source := newBillingOAuthTestSource(t, "http://127.0.0.1/token", flightTimeout,
		&http.Client{Transport: transport, Timeout: time.Second})
	firstBase, cancelFirst := context.WithCancel(t.Context())
	secondBase, cancelSecond := context.WithCancel(t.Context())
	firstWait := make(chan struct{}, 1)
	secondWait := make(chan struct{}, 1)
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	go func() {
		_, err := source.accessToken(newBillingCancellationWaiterContext(firstBase, firstWait), scope)
		firstResult <- err
	}()
	waitBillingSignal(t, firstWait, "first waiter")
	waitBillingSignal(t, requestStarted, "shared token request")
	go func() {
		_, err := source.accessToken(newBillingCancellationWaiterContext(secondBase, secondWait), scope)
		secondResult <- err
	}()
	waitBillingSignal(t, secondWait, "second waiter")

	cancelFirst()
	cancelSecond()
	for index, resultChannel := range []<-chan error{firstResult, secondResult} {
		select {
		case err := <-resultChannel:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("waiter %d error = %v, want context.Canceled", index, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for waiter %d", index)
		}
	}
	lateWait := make(chan struct{}, 1)
	lateResult := make(chan error, 1)
	go func() {
		_, err := source.accessToken(newBillingCancellationWaiterContext(t.Context(), lateWait), scope)
		lateResult <- err
	}()
	waitBillingSignal(t, lateWait, "late waiter after cancellation")
	waitBillingSignal(t, requestStopped, "detached flight timeout")
	select {
	case err := <-lateResult:
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("late waiter error = %v, want sanitized ErrUnavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for late waiter")
	}
	if _, ok := source.cached(scope); ok {
		t.Fatal("timed-out shared request populated the cache")
	}

	// The post-cancellation call can race singleflight's key removal. If it
	// joined the completed error, its retry necessarily starts a new flight.
	_, err := source.accessToken(t.Context(), scope)
	if err != nil {
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("post-cancellation call error = %v, want ErrUnavailable", err)
		}
		if _, err = source.accessToken(t.Context(), scope); err != nil {
			t.Fatalf("replacement token call error = %v", err)
		}
	}
	if got := transport.calls.Load(); got != 2 {
		t.Fatalf("token requests = %d, want 2 after completed flight", got)
	}
	if token, ok := source.cached(scope); !ok || token != "replacement-token" {
		t.Fatalf("replacement cache = %q/%v, want replacement-token/true", token, ok)
	}
}

func TestTokenSourceFlightUsesConfiguredTimeout(t *testing.T) {
	const (
		scope         = ScopeEntitlementRead
		configuredCap = 50 * time.Millisecond
	)
	requestDeadline := make(chan time.Time, 1)
	bodyClosed := make(chan struct{})
	transport := &blockingTokenRoundTripper{requestDeadline: requestDeadline, bodyClosed: bodyClosed}
	source := newBillingOAuthTestSource(t, "http://127.0.0.1/token", configuredCap,
		&http.Client{Transport: transport, Timeout: time.Second})
	result := make(chan error, 1)
	go func() {
		_, err := source.accessToken(t.Context(), scope)
		result <- err
	}()
	select {
	case deadline := <-requestDeadline:
		if deadline.After(time.Now().Add(configuredCap)) {
			t.Fatalf("flight deadline %s exceeds configured cap", deadline)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for token request deadline")
	}
	waitBillingSignal(t, bodyClosed, "configured flight timeout")
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shared timeout error = %v, want sanitized ErrUnavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for configured flight timeout result")
	}
	if _, ok := source.cached(scope); ok {
		t.Fatal("timed-out token was cached")
	}
}

func TestTokenSourceTimeoutDuringResponseBodyDoesNotCache(t *testing.T) {
	const scope = ScopeEntitlementRead
	bodyStarted := make(chan struct{})
	bodyClosed := make(chan struct{})
	transport := &blockingTokenRoundTripper{bodyStarted: bodyStarted, bodyClosed: bodyClosed}
	source := newBillingOAuthTestSource(t, "http://127.0.0.1/token", 40*time.Millisecond,
		&http.Client{Transport: transport, Timeout: time.Second})
	result := make(chan error, 1)
	go func() {
		_, err := source.accessToken(t.Context(), scope)
		result <- err
	}()
	waitBillingSignal(t, bodyStarted, "token response body")
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("body timeout error = %v, want sanitized ErrUnavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for body timeout")
	}
	waitBillingSignal(t, bodyClosed, "timed-out token response body close")
	if _, ok := source.cached(scope); ok {
		t.Fatal("timed-out response body populated the cache")
	}
}

func TestTokenSourceProductionTransportCancelsBlockedRequest(t *testing.T) {
	const flightTimeout = 50 * time.Millisecond
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	var canceledOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/token" {
			http.NotFound(writer, request)
			return
		}
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			return
		}
		close(requestStarted)
		<-request.Context().Done()
		canceledOnce.Do(func() { close(requestCanceled) })
	}))
	t.Cleanup(server.Close)

	client := server.Client()
	client.Timeout = time.Second
	source := newBillingOAuthTestSource(t, server.URL+"/token", flightTimeout, client)
	if _, ok := source.client.Transport.(*http.Transport); !ok {
		t.Fatalf("production token transport = %T, want *http.Transport", source.client.Transport)
	}
	if source.client.Timeout != client.Timeout {
		t.Fatalf("production token client timeout = %s, want supplied %s", source.client.Timeout, client.Timeout)
	}
	result := make(chan error, 1)
	go func() {
		_, err := source.accessToken(t.Context(), ScopeEntitlementRead)
		result <- err
	}()
	waitBillingSignal(t, requestStarted, "production token request")
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("production request timeout error = %v, want sanitized ErrUnavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("production transport did not honor flight cancellation")
	}
	waitBillingSignal(t, requestCanceled, "production token request cancellation")
	if _, ok := source.cached(ScopeEntitlementRead); ok {
		t.Fatal("canceled production request populated the cache")
	}
}

func TestTokenSourceProductionTransportCancelsResponseBody(t *testing.T) {
	const flightTimeout = 50 * time.Millisecond
	bodyStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/token" {
			http.NotFound(writer, request)
			return
		}
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(writer, `{"access_token":"partial`); err != nil {
			return
		}
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("token test server does not support flushing")
			return
		}
		flusher.Flush()
		startOnce.Do(func() { close(bodyStarted) })
		<-request.Context().Done()
		cancelOnce.Do(func() { close(requestCanceled) })
	}))
	t.Cleanup(server.Close)

	client := server.Client()
	client.Timeout = time.Second
	source := newBillingOAuthTestSource(t, server.URL+"/token", flightTimeout, client)
	if _, ok := source.client.Transport.(*http.Transport); !ok {
		t.Fatalf("production token transport = %T, want *http.Transport", source.client.Transport)
	}
	if source.client.Timeout != client.Timeout {
		t.Fatalf("production token client timeout = %s, want supplied %s", source.client.Timeout, client.Timeout)
	}
	result := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := source.accessToken(t.Context(), ScopeEntitlementRead)
		result <- err
	}()
	waitBillingSignal(t, bodyStarted, "production token response body")
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("production body timeout error = %v, want sanitized ErrUnavailable", err)
		}
		if elapsed := time.Since(started); elapsed > flightTimeout+time.Second {
			t.Fatalf("production body timeout took %s, want within configured deadline", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("production response body did not honor flight cancellation")
	}
	waitBillingSignal(t, requestCanceled, "production token request cancellation")
	if _, ok := source.cached(ScopeEntitlementRead); ok {
		t.Fatal("canceled production response populated the cache")
	}
}

func TestTokenSourceErrorBodyDrainChecksFlightExpiryAfterRead(t *testing.T) {
	const scope = ScopeEntitlementRead
	source := newBillingCacheTestSource()
	ctx := &billingExpireOnSecondErrorContext{Context: context.Background(), done: make(chan struct{})}
	response := &http.Response{StatusCode: http.StatusServiceUnavailable,
		Body: &staticTokenBody{value: []byte(`{"error":"temporarily unavailable"}`)}}
	_, err := source.decodeSnapshotAtEpoch(ctx, response, scope, 0)
	if !errors.Is(err, errTokenFlightExpired) {
		t.Fatalf("post-drain error = %v, want errTokenFlightExpired", err)
	}
}

func TestTokenSourceCacheCommitEpochCheckPrecedesFlightExpiry(t *testing.T) {
	const scope = ScopeEntitlementRead
	source := newBillingCacheTestSource()
	source.epochs[scope] = 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := source.cacheSnapshotAtEpoch(ctx, scope, validBillingTokenWire("stale"), 0)
	if !errors.Is(err, errEpochDiscarded) {
		t.Fatalf("epoch-mismatched expired commit error = %v, want errEpochDiscarded", err)
	}
	if _, ok := source.cached(scope); ok {
		t.Fatal("epoch-mismatched commit populated the cache")
	}
}

func TestTokenSourceCacheCommitRemovesExpiredAssignment(t *testing.T) {
	const scope = ScopeEntitlementRead
	source := newBillingCacheTestSource()
	ctx := &billingExpireOnSecondErrorContext{Context: context.Background(), done: make(chan struct{})}
	_, err := source.cacheSnapshotAtEpoch(ctx, scope, validBillingTokenWire("raced"), 0)
	if !errors.Is(err, errTokenFlightExpired) {
		t.Fatalf("raced cache commit error = %v, want errTokenFlightExpired", err)
	}
	if _, ok := source.cached(scope); ok {
		t.Fatal("expired assignment left a token in the cache")
	}
	if got := ctx.calls.Load(); got < 2 {
		t.Fatalf("context Err calls = %d, want pre- and post-assignment checks", got)
	}
}

type billingCancellationSnapshotResult struct {
	snapshot billingTokenSnapshot
	err      error
}

type gatedTokenRoundTripper struct {
	release chan struct{}
	started chan struct{}
	calls   atomic.Int32
	once    sync.Once
}

func (transport *gatedTokenRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	if transport.calls.Add(1) == 1 {
		transport.once.Do(func() { close(transport.started) })
		<-transport.release
		return staticTokenResponse("shared-token"), nil
	}
	return staticTokenResponse("unexpected-token"), nil
}

type allWaitersTokenRoundTripper struct {
	calls          atomic.Int32
	requestStarted chan struct{}
	requestStopped chan struct{}
	startOnce      sync.Once
}

func (transport *allWaitersTokenRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.calls.Add(1) == 1 {
		transport.startOnce.Do(func() { close(transport.requestStarted) })
		return &http.Response{StatusCode: http.StatusOK, Header: billingTokenJSONHeader(),
			Body: &blockingTokenBody{done: request.Context().Done(), closed: transport.requestStopped}, Request: request}, nil
	}
	return staticTokenResponse("replacement-token"), nil
}

type blockingTokenRoundTripper struct {
	bodyStarted     chan struct{}
	bodyClosed      chan struct{}
	requestDeadline chan time.Time
}

func (transport *blockingTokenRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.bodyStarted != nil {
		close(transport.bodyStarted)
	}
	if transport.requestDeadline != nil {
		deadline, ok := request.Context().Deadline()
		if ok {
			transport.requestDeadline <- deadline
		}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: billingTokenJSONHeader(),
		Body: &blockingTokenBody{done: request.Context().Done(), closed: transport.bodyClosed}, Request: request}, nil
}

type blockingTokenBody struct {
	done   <-chan struct{}
	closed chan struct{}
	once   sync.Once
}

func (body *blockingTokenBody) Read([]byte) (int, error) {
	<-body.done
	return 0, context.DeadlineExceeded
}

func (body *blockingTokenBody) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

type billingExpireOnSecondErrorContext struct {
	context.Context
	done  chan struct{}
	calls atomic.Int32
	once  sync.Once
}

func (ctx *billingExpireOnSecondErrorContext) Err() error {
	if ctx.calls.Add(1) >= 2 {
		ctx.once.Do(func() { close(ctx.done) })
		return context.DeadlineExceeded
	}
	return nil
}

func (ctx *billingExpireOnSecondErrorContext) Done() <-chan struct{} { return ctx.done }

type billingCancellationWaiterContext struct {
	context.Context
	reached chan<- struct{}
	once    sync.Once
}

func (ctx *billingCancellationWaiterContext) Done() <-chan struct{} {
	ctx.once.Do(func() { ctx.reached <- struct{}{} })
	return ctx.Context.Done()
}

func newBillingCancellationWaiterContext(parent context.Context, reached chan<- struct{}) context.Context {
	return &billingCancellationWaiterContext{Context: parent, reached: reached}
}

func billingTokenJSONHeader() http.Header {
	return http.Header{"Content-Type": {"application/json"}}
}

func staticTokenResponse(token string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: billingTokenJSONHeader(),
		Body: &staticTokenBody{value: []byte(`{"access_token":"` + token + `","token_type":"Bearer","expires_in":300}`)},
	}
}

type staticTokenBody struct {
	value  []byte
	offset int
}

func (body *staticTokenBody) Read(buffer []byte) (int, error) {
	if body.offset == len(body.value) {
		return 0, io.EOF
	}
	count := copy(buffer, body.value[body.offset:])
	body.offset += count
	return count, nil
}

func (body *staticTokenBody) Close() error { return nil }

func newBillingOAuthTestSource(t *testing.T, endpoint string, timeout time.Duration, client *http.Client) *tokenSource {
	t.Helper()
	source, err := newTokenSource(oauthConfig{TokenURL: endpoint, ClientID: "client", ClientSecret: "secret",
		Scope: ScopeEntitlementRead + " " + ScopeMeteringWrite, Resources: []string{billingResource},
		Timeout: timeout, AllowHTTP: true}, client)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func newBillingCacheTestSource() *tokenSource {
	return &tokenSource{tokens: make(map[string]cachedToken), epochs: make(map[string]uint64),
		generations: make(map[string]uint64)}
}

func validBillingTokenWire(token string) tokenWire {
	return tokenWire{AccessToken: token, TokenType: "Bearer", ExpiresIn: 300}
}
