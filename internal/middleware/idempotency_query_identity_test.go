package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/idempotency"
)

// idemTriple is the command identity as the middleware states it: the actor,
// the server-built method+path+query endpoint, and the client key header.
// It is a struct (never a joined string) so the fake store cannot reproduce —
// or hide — any delimiter ambiguity of the real Redis encoding.
type idemTriple struct {
	uid      string
	endpoint string
	key      string
}

// recordingTripleStore is an idempotency.Store that records every reservation,
// completion and release under the exact triple it was handed.
type recordingTripleStore struct {
	mu       sync.Mutex
	records  map[idemTriple]*idempotency.Record
	reserved []idemTriple
	complete []idemTriple
	released []idemTriple
}

func newRecordingTripleStore() *recordingTripleStore {
	return &recordingTripleStore{records: map[idemTriple]*idempotency.Record{}}
}

func (s *recordingTripleStore) Reserve(_ context.Context, uid, endpoint, key string) (*idempotency.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	triple := idemTriple{uid, endpoint, key}
	if rec, ok := s.records[triple]; ok {
		return rec, false, nil
	}
	s.records[triple] = &idempotency.Record{}
	s.reserved = append(s.reserved, triple)
	return nil, true, nil
}

func (s *recordingTripleStore) Complete(_ context.Context, uid, endpoint, key string,
	rec idempotency.Record, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	triple := idemTriple{uid, endpoint, key}
	rec.Completed = true
	s.records[triple] = &rec
	s.complete = append(s.complete, triple)
	return nil
}

func (s *recordingTripleStore) Release(_ context.Context, uid, endpoint, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	triple := idemTriple{uid, endpoint, key}
	delete(s.records, triple)
	s.released = append(s.released, triple)
	return nil
}

func (s *recordingTripleStore) bodies() map[idemTriple]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[idemTriple]string, len(s.records))
	for triple, rec := range s.records {
		out[triple] = string(rec.Body)
	}
	return out
}

// TestIdempotency_QueryStringPartOfCommandIdentity is the middleware half of the
// contract the store now relies on: the query string is part of the command
// identity, so two keyed POSTs whose colons could shift the endpoint|key
// delimiter must get two independent reservations — never one replayed body and
// never a 409 for an in-flight request that does not exist.
func TestIdempotency_QueryStringPartOfCommandIdentity(t *testing.T) {
	store := newRecordingTripleStore()
	calls := 0
	e := setupIdemRouter(store, nil, &calls)

	// Same client key shape, colon moved from the query into the header.
	first := postWithPathAndKey(e, "/v1/messages?x=a:b", "c")
	second := postWithPathAndKey(e, "/v1/messages?x=a", "b:c")

	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2 (distinct commands must both execute)", calls)
	}
	store.mu.Lock()
	reserved := append([]idemTriple(nil), store.reserved...)
	completed := append([]idemTriple(nil), store.complete...)
	store.mu.Unlock()

	if len(reserved) != 2 {
		t.Fatalf("reservations = %v, want exactly 2", reserved)
	}
	if reserved[0] == reserved[1] {
		t.Fatalf("both requests collapsed onto one reservation: %+v", reserved[0])
	}
	wantEndpoints := map[string]bool{"POST /v1/messages?x=a:b": true, "POST /v1/messages?x=a": true}
	for _, triple := range reserved {
		if !wantEndpoints[triple.endpoint] {
			t.Fatalf("unexpected reserved endpoint %q", triple.endpoint)
		}
		if triple.uid != "u1" {
			t.Fatalf("reserved triple uid = %q, want u1", triple.uid)
		}
	}
	if len(completed) != 2 {
		t.Fatalf("completions = %v, want exactly 2", completed)
	}

	for i, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code != http.StatusCreated {
			t.Fatalf("response %d status = %d, want 201", i, response.Code)
		}
		if replayed := response.Header().Get("Idempotent-Replayed"); replayed != "" {
			t.Fatalf("response %d was replayed (%s); a distinct command must never borrow another response",
				i, replayed)
		}
	}
	if first.Body.String() == second.Body.String() {
		t.Fatalf("both requests returned the same body %q; no cross-request disclosure is allowed",
			first.Body.String())
	}

	bodies := store.bodies()
	if len(bodies) != 2 {
		t.Fatalf("stored receipts = %d, want 2", len(bodies))
	}
	firstBody, secondBody := "", ""
	for triple, body := range bodies {
		if triple.key == "c" {
			firstBody = body
		} else {
			secondBody = body
		}
	}
	if firstBody == secondBody || firstBody == "" || secondBody == "" {
		t.Fatalf("stored receipts did not diverge: %q / %q", firstBody, secondBody)
	}
}

// TestIdempotency_SameKeyAndSameQueryStillReplays is the negative control for
// the test above: an identical retry must still reserve once and replay. It
// keeps a fix for the ambiguity from over-correcting into a key so specific
// that legitimate retries stop replaying.
func TestIdempotency_SameKeyAndSameQueryStillReplays(t *testing.T) {
	store := newRecordingTripleStore()
	calls := 0
	e := setupIdemRouter(store, nil, &calls)

	first := postWithPathAndKey(e, "/v1/messages?x=a", "c")
	second := postWithPathAndKey(e, "/v1/messages?x=a", "c")

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1 (identical retry must not re-execute)", calls)
	}
	store.mu.Lock()
	reserved := len(store.reserved)
	store.mu.Unlock()
	if reserved != 1 {
		t.Fatalf("reservations = %d, want 1", reserved)
	}
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("statuses = %d/%d, want 201/201", first.Code, second.Code)
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry was not replayed: headers = %v", second.Header())
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replayed body = %q, want %q", second.Body.String(), first.Body.String())
	}
}

// compile-time proof the fake still satisfies the store contract the middleware
// type-asserts against.
var _ idempotency.Store = (*recordingTripleStore)(nil)
