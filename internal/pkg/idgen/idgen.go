package idgen

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ID = uuid.UUID

// anchorNamespace is the authoritative UUIDv5 namespace for derived
// deterministic IDs (derivation contract: derive-stable-deterministic-event-id-from-an-anc).
// It is a domain separator, not a secret, and it is constant forever: changing
// it silently invalidates every historical derived event_id and stops ledger
// dedup from firing across a rollout. Never rotate it and never reuse it for a
// derivation site with a different part contract.
//
// It is deliberately unexported: Anchor derives from this package-level value,
// so no code outside this package can name it, assign it, or redirect
// derivation. The never-rotate contract is enforced where IDs are minted, not
// only by review or by grep. AnchorNamespace below is a print-only mirror.
var anchorNamespace = uuid.MustParse("38248e3a-32f0-4929-9b56-b2dbd75aa849")

// minAnchorArity is the enforced floor on Anchor's part count and the single
// source of truth for it: the guard, the panic message and Anchor's doc below
// all read this constant, so the number cannot drift between them.
//
// Two is the largest floor every live anchor site honors. The poison/
// quarantine surrogate mints Anchor("event.quarantine.v1", <wire fingerprint>)
// at internal/handler/grpc/event_ingest.go and internal/service/event_ingest.go
// — a discriminator plus one identity part; every other site passes 4-6 parts.
// A floor above two would panic a shipped path, so it cannot be raised without
// first changing those sites.
//
// A site below the floor is almost always broken rather than legitimately
// small — a dropped tenant, subject, step, attempt or fencing-epoch slot — and
// arity is part of the encoding, so the dropped slot would never surface as an
// error: it would derive a different but valid, perfectly stable UUIDv5, the
// fact would commit, and the ledger's (tenant_id, source, event_id) dedup guard
// would then fold every later execution of that truncated shape onto the first
// row, silently losing a multi-occurrence fact with no error and no log line.
// Panic instead of deriving.
const minAnchorArity = 2

// anchorArityPanic carries the enforced floor in the panic value so a reader
// can tell a broken site from a floor that was raised later. It is computed
// once from minAnchorArity (never at call time) and is used only by the guard
// below; the message text is not a stable contract, so tests assert only on
// the minAnchorArity substring.
var anchorArityPanic = "idgen.Anchor: arity below minAnchorArity=" + strconv.Itoa(minAnchorArity) +
	" (the live floor shape is Anchor(\"event.quarantine.v1\", <wire fingerprint>)); " +
	"a dropped identity part is a programming error, not a smaller anchor"

// AnchorNamespace is a non-authoritative, read-only mirror of the unexported
// anchorNamespace, kept only so parity tests and documentation can print the
// namespace the scheme contract names.
//
// Anchor never reads this identifier, so assigning it — from a test, an init,
// or a config-derived value — cannot change any derived ID: the derivation
// source is unassignable outside this package. Reads are the only supported
// use; treat a write here as inert and as a drift signal.
var AnchorNamespace = anchorNamespace

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
// never reachable from persisted values at the call sites.
//
// Arity below minAnchorArity is likewise a programming error and is rejected
// with a panic before hashing, for the same fail-fast reason as the NUL rule
// above: arity is part of the encoding (Anchor("a","b") != Anchor("a","b","")),
// so a site that dropped an identity slot would otherwise derive a
// valid-looking, ledger-dedupable ID instead of failing loudly. The enforced
// minimum is minAnchorArity (2), occupied live by the event.quarantine.v1 +
// wire-fingerprint surrogate; every other site anchors a discriminator plus its
// identity parts (4-6). Meeting the floor must never be done by padding parts
// — padding changes the join and therefore the derived ID, silently re-keying
// ledger rows that are already stored.
func Anchor(parts ...string) ID {
	if len(parts) < minAnchorArity {
		panic(anchorArityPanic)
	}
	for _, p := range parts {
		if strings.IndexByte(p, 0) >= 0 {
			panic("idgen.Anchor: part contains a NUL byte (U+0000), ambiguous under NUL separation")
		}
	}
	return uuid.NewSHA1(anchorNamespace, []byte(strings.Join(parts, "\x00")))
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

var ErrZeroID = errors.New("zero UUID is not a valid identifier")

func Parse(s string) (ID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return ID{}, err
	}
	if id == (ID{}) {
		return ID{}, ErrZeroID
	}
	return id, nil
}

// CanonicalEqual reports whether wireText denotes exactly the same 128-bit
// identity as id, using the accepted wire grammar of Parse (hyphenated in any
// hex case, "urn:uuid:" prefix in any case, 32-hex, "{...}") with leading and
// trailing whitespace trimmed the way the ingest boundary trims it, so the
// result does not depend on which of those ran first.
//
// It exists to protect one invariant: every text form Parse accepts is the same
// identity. Comparing a rendered event id (id.String()) against raw wire text
// misclassifies provenance — it records a source-supplied identity as derived
// for a legal alternative rendering, and it can never detect a locally anchored
// surrogate, whose text matches no wire rendering.
//
// It never panics and never errors: empty, whitespace-only, unparseable,
// length-capped, control-character and arbitrarily long inputs are simply not
// the same identity. Over-long input fails to parse; no length cap is imposed
// here because idgen must not depend on the event package's MaxIDBytes.
func CanonicalEqual(id ID, wireText string) bool {
	text := strings.TrimSpace(wireText)
	if text == "" {
		return false
	}
	// Parse, not uuid.Parse: the accepted wire grammar must stay identical to
	// the ingest grammar, and the zero UUID must never compare equal.
	parsed, err := Parse(text)
	return err == nil && parsed == id
}
