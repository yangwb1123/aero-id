package domain

// LegacyCutoverStatus is the per-tenant cutover state machine (P8-04).
type LegacyCutoverStatus string

const (
	LegacyCutoverStatusLegacy     LegacyCutoverStatus = "legacy"      // default: compatibility surface live
	LegacyCutoverStatusShadow     LegacyCutoverStatus = "shadow"      // dual-read in progress; legacy still serves
	LegacyCutoverStatusCutOver    LegacyCutoverStatus = "cut_over"    // facade answers "moved" for this tenant
	LegacyCutoverStatusRolledBack LegacyCutoverStatus = "rolled_back" // explicit rollback; legacy serves again
)

// ValidCutoverTransition reports whether from -> to is allowed.
func ValidCutoverTransition(from, to LegacyCutoverStatus) bool {
	switch to {
	case LegacyCutoverStatusShadow:
		return from == LegacyCutoverStatusLegacy || from == LegacyCutoverStatusRolledBack
	case LegacyCutoverStatusCutOver:
		return from == LegacyCutoverStatusShadow || from == LegacyCutoverStatusCutOver
	case LegacyCutoverStatusRolledBack:
		return from == LegacyCutoverStatusCutOver
	case LegacyCutoverStatusLegacy:
		return from == LegacyCutoverStatusLegacy || from == LegacyCutoverStatusRolledBack
	default:
		return false
	}
}
