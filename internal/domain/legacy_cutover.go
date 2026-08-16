package domain

// LegacyCutoverStatus is the per-tenant cutover state machine (P8-04).
type LegacyCutoverStatus string

const (
	LegacyCutoverStatusLegacy     LegacyCutoverStatus = "legacy"      // default: compatibility surface live
	legacyCutoverStatusShadow     LegacyCutoverStatus = "shadow"      // dual-read in progress; legacy still serves
	LegacyCutoverStatusCutOver    LegacyCutoverStatus = "cut_over"    // facade answers "moved" for this tenant
	legacyCutoverStatusRolledBack LegacyCutoverStatus = "rolled_back" // explicit rollback; legacy serves again
)

// ValidCutoverTransition reports whether from -> to is allowed.
func ValidCutoverTransition(from, to LegacyCutoverStatus) bool {
	switch to {
	case legacyCutoverStatusShadow:
		return from == LegacyCutoverStatusLegacy || from == legacyCutoverStatusRolledBack
	case LegacyCutoverStatusCutOver:
		return from == legacyCutoverStatusShadow || from == LegacyCutoverStatusCutOver
	case legacyCutoverStatusRolledBack:
		return from == LegacyCutoverStatusCutOver
	case LegacyCutoverStatusLegacy:
		return from == LegacyCutoverStatusLegacy || from == legacyCutoverStatusRolledBack
	default:
		return false
	}
}
