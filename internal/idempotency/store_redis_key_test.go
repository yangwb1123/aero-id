package idempotency

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRedisKey_UnambiguousAcrossEndpointKeyColonBoundary is the defect the
// length framing exists to remove: the endpoint|key delimiter used to be
// client-movable through RawQuery, so two different commands shared one Redis
// key (and therefore one reservation, one replayed body, one spurious 409).
func TestRedisKey_UnambiguousAcrossEndpointKeyColonBoundary(t *testing.T) {
	colonInQuery := redisKey("u1", "POST /v1/me/sync?x=a:b", "c")
	colonInHeader := redisKey("u1", "POST /v1/me/sync?x=a", "b:c")
	if colonInQuery == colonInHeader {
		t.Fatalf("redisKey aliased two distinct command triples onto one key: %q", colonInQuery)
	}
	// Guard the fixture: the pre-v1 encoding must still reproduce the aliasing,
	// otherwise this test would pass for the wrong reason.
	if legacyRedisKey("u1", "POST /v1/me/sync?x=a:b", "c") != legacyRedisKey("u1", "POST /v1/me/sync?x=a", "b:c") {
		t.Fatal("legacy fixture does not reproduce the colon collision it is meant to model")
	}
}

// TestRedisKey_StableForIdenticalTriple proves the encoding is deterministic:
// an identical triple must always name one key, or retries would re-execute.
func TestRedisKey_StableForIdenticalTriple(t *testing.T) {
	first := redisKey("u1", "POST /v1/messages", "key-1")
	for i := 0; i < 3; i++ {
		if got := redisKey("u1", "POST /v1/messages", "key-1"); got != first {
			t.Fatalf("redisKey is not stable: got %q, want %q", got, first)
		}
	}
}

// TestRedisKey_FormatVersionMarkerPinned is the golden for the wire format. Any
// framing or prefix edit fails here and must be paired with a deliberate
// version bump in redisKeyPrefix (plus retirement of legacyRedisKey), never an
// in-place edit that silently orphans live reservations.
func TestRedisKey_FormatVersionMarkerPinned(t *testing.T) {
	cases := []struct {
		name     string
		uid      string
		endpoint string
		key      string
		want     string
	}{
		{
			name:     "colon inside RawQuery",
			uid:      "u1",
			endpoint: "POST /v1/me/sync?x=a:b",
			key:      "c",
			want:     "aero-id:idem:v1:2:u122:POST /v1/me/sync?x=a:b1:c",
		},
		{
			name:     "colon inside Idempotency-Key",
			uid:      "u1",
			endpoint: "POST /v1/me/sync?x=a",
			key:      "b:c",
			want:     "aero-id:idem:v1:2:u120:POST /v1/me/sync?x=a3:b:c",
		},
		{
			name:     "multi-byte endpoint counts bytes",
			uid:      "u1",
			endpoint: "POST /v1/files?q=日本",
			key:      "k",
			want:     "aero-id:idem:v1:2:u123:POST /v1/files?q=日本1:k",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redisKey(tc.uid, tc.endpoint, tc.key); got != tc.want {
				t.Fatalf("redisKey(%q, %q, %q) = %q, want %q (bump the version marker instead of editing the format)",
					tc.uid, tc.endpoint, tc.key, got, tc.want)
			}
			if legacy := legacyRedisKey(tc.uid, tc.endpoint, tc.key); legacy == tc.want {
				t.Fatalf("v1 keyspace and legacy keyspace are not disjoint for %q", tc.want)
			}
		})
	}
}

// TestRedisKey_NoCollisionForEqualConcatenations walks the colon-shifting
// triples that all flattened to the same pre-v1 key and requires each one to
// keep its own v1 key. Every group is first asserted to be a genuine collision
// group under legacyRedisKey, so the fixture cannot pass vacuously.
func TestRedisKey_NoCollisionForEqualConcatenations(t *testing.T) {
	groups := []struct {
		name    string
		triples [][3]string
	}{
		{
			name: "endpoint|key boundary is client-movable via RawQuery",
			triples: [][3]string{
				{"u1", "POST /v1/me/sync?x=a:b", "c:d"},
				{"u1", "POST /v1/me/sync?x=a", "b:c:d"},
				{"u1:POST /v1/me/sync?x=a:b", "c", "d"},
			},
		},
		{
			name: "uid|endpoint boundary",
			triples: [][3]string{
				{"u1:POST", "/v1/me/sync", "k"},
				{"u1", "POST:/v1/me/sync", "k"},
			},
		},
		{
			name: "empty parts and colons only",
			triples: [][3]string{
				{"", ":", ":"},
				{":", "", ":"},
				{":", ":", ""},
			},
		},
		{
			name: "colon-heavy query",
			triples: [][3]string{
				{"u1", "POST /v1/x?a::b::c", "k"},
				{"u1", "POST /v1/x?a", ":b::c:k"},
			},
		},
	}
	for _, group := range groups {
		t.Run(group.name, func(t *testing.T) {
			legacy := group.triples[0]
			flat := legacyRedisKey(legacy[0], legacy[1], legacy[2])
			seen := make(map[string][3]string, len(group.triples))
			for _, triple := range group.triples {
				if got := legacyRedisKey(triple[0], triple[1], triple[2]); got != flat {
					t.Fatalf("fixture %v does not collide under the legacy format: %q != %q", triple, got, flat)
				}
				key := redisKey(triple[0], triple[1], triple[2])
				if prior, ok := seen[key]; ok {
					t.Fatalf("redisKey aliased %v and %v onto %q", prior, triple, key)
				}
				seen[key] = triple
			}
		})
	}
}

// TestRedisKey_ArityAndOrderArePartOfTheEncoding decodes the framed tail back
// to the exact triple: a fixed arity of 3 frames in a fixed order means
// ("a","b","c") cannot decode to any other triple, which is the injectivity
// property the length framing buys (idgen.Anchor's "arity is part of the
// encoding" rule, with no rejection contract).
func TestRedisKey_ArityAndOrderArePartOfTheEncoding(t *testing.T) {
	corpus := [][3]string{
		{"", "", ""},
		{"u1", "POST /v1/messages", "key-1"},
		{"u1", "POST /v1/messages", ""},
		{"u1", "", "key-1"},
		{"u1", "POST /v1/messages", "key-2"},
		{"u1", "POST /v1/messages?x=a:b", "c"},
		{"u1", "POST /v1/messages?x=a", "b:c"},
		{"u2", "POST /v1/messages", "key-1"},
		{"u1:POST /v1/messages", "key-1", ""},
	}
	seen := make(map[string][3]string, len(corpus))
	for _, triple := range corpus {
		key := redisKey(triple[0], triple[1], triple[2])
		decoded := decodeRedisKey(t, key)
		if len(decoded) != 3 {
			t.Fatalf("redisKey(%q, %q, %q) decoded to %d frames, want 3", triple[0], triple[1], triple[2], len(decoded))
		}
		for i := range triple {
			if decoded[i] != triple[i] {
				t.Fatalf("redisKey(%q, %q, %q) frame %d = %q", triple[0], triple[1], triple[2], i, decoded[i])
			}
		}
		if prior, ok := seen[key]; ok {
			t.Fatalf("redisKey aliased %v and %v onto %q", prior, triple, key)
		}
		seen[key] = triple
	}
	// Arity is part of the encoding: a fourth, empty component cannot be
	// confused with a three-component triple, and the tail carries nothing that
	// could be read as an extra frame.
	if tail := strings.TrimPrefix(redisKey("a", "b", "c"), redisKeyPrefix); tail != "1:a1:b1:c" {
		t.Fatalf("tail = %q, want %q", tail, "1:a1:b1:c")
	}
}

// TestRedisKey_FrameLengthCountsBytes guards the byte-vs-rune trap: a length
// header that counted runes would frame a multi-byte part incorrectly and
// produce a key that decodes to the wrong command.
func TestRedisKey_FrameLengthCountsBytes(t *testing.T) {
	const (
		uid      = "ü1"
		endpoint = "POST /v1/files?q=日本"
		key      = "kéy"
	)
	framed := strconv.Itoa(len(endpoint)) + ":" + endpoint
	want := redisKeyPrefix + strconv.Itoa(len(uid)) + ":" + uid + framed + strconv.Itoa(len(key)) + ":" + key
	if got := redisKey(uid, endpoint, key); got != want {
		t.Fatalf("redisKey = %q, want %q", got, want)
	}
	if len(endpoint) == utf8.RuneCountInString(endpoint) {
		t.Fatalf("fixture is not multi-byte: %q", endpoint)
	}
	decoded := decodeRedisKey(t, want)
	if decoded[0] != uid || decoded[1] != endpoint || decoded[2] != key {
		t.Fatalf("multi-byte triple did not round-trip: %#v", decoded)
	}
}

// TestRedisKey_PrefixIsNotSynthesizableByAnyPart proves the keyspaces stay
// disjoint: the version marker sits outside the framed region, so no client
// value can push a key into another namespace or into the legacy format.
func TestRedisKey_PrefixIsNotSynthesizableByAnyPart(t *testing.T) {
	if strings.HasPrefix(redisKeyPrefix, "idempot:") {
		t.Fatalf("v1 prefix %q overlaps the legacy namespace", redisKeyPrefix)
	}
	adversarial := [][3]string{
		{redisKeyPrefix, "", ""},
		{"idempot:", "", ""},
		{"", redisKeyPrefix, ""},
		{"", "", redisKeyPrefix},
		{"aero-id:idem:v1:2:u11:b1:c", "", ""},
		{"aero-id:idem:v2:2:u1", "POST /v1/x", "k"},
	}
	for _, triple := range adversarial {
		key := redisKey(triple[0], triple[1], triple[2])
		if !strings.HasPrefix(key, redisKeyPrefix) {
			t.Fatalf("redisKey(%q, %q, %q) = %q lost the version marker", triple[0], triple[1], triple[2], key)
		}
		if rest := strings.TrimPrefix(key, redisKeyPrefix); strings.HasPrefix(rest, "aero-id:") {
			t.Fatalf("redisKey(%q, %q, %q) = %q let a part escape the framed region", triple[0], triple[1], triple[2], key)
		}
		if decoded := decodeRedisKey(t, key); len(decoded) != 3 ||
			decoded[0] != triple[0] || decoded[1] != triple[1] || decoded[2] != triple[2] {
			t.Fatalf("redisKey(%q, %q, %q) = %q did not round-trip: %#v", triple[0], triple[1], triple[2], key, decoded)
		}
		if legacy := legacyRedisKey(triple[0], triple[1], triple[2]); legacy == key {
			t.Fatalf("triple %v maps onto one key in both keyspaces: %q", triple, key)
		}
	}
}

// TestRedisKey_DocCommentRecordsColonHazard makes the encoding rationale
// mechanically enforced rather than aspirational: if the doc block stops naming
// RawQuery, the colon hazard, the version marker or the legacy retirement
// criterion, the reason a future edit could reintroduce ambiguity is gone.
func TestRedisKey_DocCommentRecordsColonHazard(t *testing.T) {
	src, err := os.ReadFile("store_redis.go")
	if err != nil {
		t.Fatalf("read store_redis.go: %v", err)
	}
	text := string(src)
	redisDoc := docBlockAbove(t, text, "func redisKey(uid, endpoint, key string) string")
	for _, required := range []string{"RawQuery", "idempot:", "idgen.Anchor", "UNIQUE (uid, endpoint, idem_key)", "bump"} {
		if !strings.Contains(redisDoc, required) {
			t.Fatalf("redisKey doc comment no longer records %q:\n%s", required, redisDoc)
		}
	}
	legacyDoc := docBlockAbove(t, text, "func legacyRedisKey(uid, endpoint, key string) string")
	for _, required := range []string{"idempot:", "READ-ONLY", "recordTTL + redisReservationTTL"} {
		if !strings.Contains(legacyDoc, required) {
			t.Fatalf("legacyRedisKey doc comment no longer records %q:\n%s", required, legacyDoc)
		}
	}
}

// decodeRedisKey reverses redisKey for a triple of parts, proving the framing
// is lossless. It fails the test rather than returning an error because every
// caller wants the same assertion.
func decodeRedisKey(t *testing.T, key string) []string {
	t.Helper()
	if !strings.HasPrefix(key, redisKeyPrefix) {
		t.Fatalf("key %q does not start with %q", key, redisKeyPrefix)
	}
	rest := key[len(redisKeyPrefix):]
	parts := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		sep := strings.IndexByte(rest, ':')
		if sep < 0 {
			t.Fatalf("key %q has fewer than 3 frames at frame %d: %q", key, i, rest)
		}
		size, err := strconv.Atoi(rest[:sep])
		if err != nil || size < 0 {
			t.Fatalf("key %q frame %d has invalid length %q: %v", key, i, rest[:sep], err)
		}
		rest = rest[sep+1:]
		if len(rest) < size {
			t.Fatalf("key %q frame %d is truncated: want %d bytes, have %d", key, i, size, len(rest))
		}
		parts = append(parts, rest[:size])
		rest = rest[size:]
	}
	if rest != "" {
		t.Fatalf("key %q has trailing bytes after 3 frames: %q", key, rest)
	}
	return parts
}

// docBlockAbove returns the contiguous // comment block immediately above decl.
func docBlockAbove(t *testing.T, text, decl string) string {
	t.Helper()
	idx := strings.Index(text, decl)
	if idx < 0 {
		t.Fatalf("declaration %q not found", decl)
	}
	lines := strings.Split(text[:idx], "\n")
	end := len(lines) - 1
	for end > 0 && strings.TrimSpace(lines[end]) == "" {
		end--
	}
	start := end
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "//") {
		start--
	}
	return strings.Join(lines[start:end+1], "\n")
}
