package domain

// Rule (f) positive fixture: exported symbols with no qualified consumer
// outside internal/domain. OrphanedExport is a bare func export;
// OrphanBlockConst is a const-block-only member (pins const-block
// enumeration — a scan that only handles type/var/func would miss it).
// The package imports nothing, so rule (b) stays clean.
func OrphanedExport() {}

const (
	OrphanBlockConst = "orphan"
)
