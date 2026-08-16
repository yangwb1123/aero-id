package fixture

// R-3 positive self-test fixture for check-anchored-event-ids.sh: an
// idgen.Anchor call whose leading literal run ("shape.not.pinned") has no
// golden pin in the R-3 manifest. Lives under scripts/testdata (never
// compiled, never scanned by the live-tree gate); it contains ONLY this
// unpinned anchor so the R-3 reddening is attributable to R-3 itself, not to
// an R-1/R-2 pattern.

func unpinned() {
	_ = idgen.Anchor("shape.not.pinned", "tenant", "x") // R-3 shape: no golden pin
}
