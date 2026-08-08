package idgen

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

type ID = uuid.UUID

// AnchorNamespace is the fixed UUIDv5 namespace for derived deterministic IDs
// (derivation contract: derive-stable-deterministic-event-id-from-an-anc). It
// is a domain separator, not a secret, and it is constant forever: changing it
// silently invalidates every historical derived event_id and stops ledger dedup
// from firing across a rollout. Never rotate it and never reuse it for a
// derivation site with a different part contract.
var AnchorNamespace = uuid.MustParse("38248e3a-32f0-4929-9b56-b2dbd75aa849")

// Anchor derives a deterministic UUIDv5 from NUL-separated parts (precedent:
// projection_replication.go:134). Identical parts produce identical IDs; any
// part change produces a different ID; NUL separation prevents prefix
// collisions between ("ab","c") and ("a","bc"). The returned ID is an
// identifier, never a capability: ledger dedup depends on it being derivable
// from public identity fields.
//
// A part containing U+0000 is rejected with a panic before hashing: under NUL
// separation such input is ambiguous (Anchor("a\x00b","c") would equal
// Anchor("a","b\x00c")), so it is a programming error, not a valid anchor.
// The rejection is deterministic and mirrors the NewID() clock-failure panic
// precedent; PostgreSQL rejects U+0000 in text columns anyway, so the panic is
// never reachable from persisted values at the call sites. Zero parts is also
// a programming error: every anchor site has fixed arity >= 4, and arity is
// part of the encoding (Anchor("x") != Anchor("x","")).
func Anchor(parts ...string) ID {
	if len(parts) == 0 {
		panic("idgen.Anchor: zero parts — every anchor site has fixed arity >= 4; zero parts is a programming error")
	}
	for _, p := range parts {
		if strings.IndexByte(p, 0) >= 0 {
			panic("idgen.Anchor: part contains a NUL byte (U+0000), ambiguous under NUL separation")
		}
	}
	return uuid.NewSHA1(AnchorNamespace, []byte(strings.Join(parts, "\x00")))
}

// RenderAnchorTime renders a time part for anchoring (contract R7): always
// UTC, always exactly six fractional digits, always Z — byte-identical to the
// value PostgreSQL stores for the same instant (timestamptz is microsecond-
// precision UTC). Offset inputs normalize, sub-microsecond inputs truncate,
// nil renders as the empty part (indefinite hold), and a non-nil zero time
// renders as 0001-01-01T00:00:00.000000Z (distinct from nil, matching the
// distinct stored rows).
func RenderAnchorTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
}

func NewID() ID {
	id, err := uuid.NewV7()
	if err != nil {
		// V7 only fails if the system clock is broken; panicking here is acceptable.
		panic("uuid v7: " + err.Error())
	}
	return id
}

func Parse(s string) (ID, error) {
	return uuid.Parse(s)
}
