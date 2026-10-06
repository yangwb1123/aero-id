package connector

import (
	"strings"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

// SameCorrelationID reports whether a source-echoed correlation identifier
// denotes the same operation identity as the identifier aero-id sent.
//
// aero-id mints correlation values as UUIDs (idgen.NewID), and a source may
// parse one and re-render it in any text form idgen.Parse accepts: upper- or
// mixed-case hex, a "urn:uuid:" prefix, 32 unhyphenated hex, braces, or
// surrounding whitespace. Comparing rendered strings therefore rejects a
// command the source actually correlated. When requested parses as a non-zero
// UUID the comparison is made by identity (idgen.CanonicalEqual); when it does
// not parse — a legacy or opaque non-UUID correlation value — it falls back to
// exact string equality, which is the only identity such a value has.
//
// Scope: aero-id-owned correlation ids only. Source-owned source_operation_id
// values are opaque strings minted by the source, are the reconciliation
// target, and must never pass through this helper; they stay exact-string
// compared.
//
// It never panics and never errors: unparseable, empty, whitespace-only,
// control-character and arbitrarily long inputs are simply not the same
// identity or fall back to exact equality.
func SameCorrelationID(requested, echoed string) bool {
	if id, err := idgen.Parse(strings.TrimSpace(requested)); err == nil {
		return idgen.CanonicalEqual(id, echoed)
	}
	return echoed == requested
}
