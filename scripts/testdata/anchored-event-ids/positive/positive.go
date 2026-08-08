package fixture

// Positive self-test fixture for check-anchored-event-ids.sh: every pattern
// the gate must flag. Lives under scripts/testdata (never compiled, never
// scanned by the live-tree gate); it only proves the gate fires.

func positive() {
	// R-1: random minting outside the C2 allowlist.
	_ = f
	f.EventID = idgen.NewID() // R-1 shape: "EventID: idgen.NewID()"

	// R-2: a second UUIDv5 namespace constant outside idgen + projection.
	var secondNamespace = uuid.MustParse("38248e3a-32f0-4929-9b56-b2dbd75aa849")

	// R-2: uuid.NewSHA1 / uuid.NewHash outside the allowed files.
	_ = uuid.NewSHA1(secondNamespace, []byte("parts"))
	_ = uuid.NewHash(secondNamespace, sha1.New(), []byte("parts"))

	// R-2: raw digest cast instead of the RFC 4122 v5 encoding.
	_ = uuid.UUID(digest[:16])

	// R-2: zero-part Anchor() call.
	_ = idgen.Anchor()
}
