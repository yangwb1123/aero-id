package idgen

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewID_uniqueAndTimeOrdered(t *testing.T) {
	prev := NewID()
	for i := 0; i < 100; i++ {
		time.Sleep(time.Millisecond)
		curr := NewID()
		if curr == prev {
			t.Fatal("two consecutive IDs collided")
		}
		if curr.String() < prev.String() {
			t.Fatalf("expected time-ordered, got %s < %s", curr, prev)
		}
		prev = curr
	}
}

func TestParse_roundtrip(t *testing.T) {
	id := NewID()
	parsed, err := Parse(id.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != id {
		t.Fatalf("parsed=%s want=%s", parsed, id)
	}
}

// Golden fixture IDs from the derivation contract (v2 shapes, §3 of
// task-1-design.md): TENANT = "tenant-alpha", OP/ST/JB as below.
const (
	goldenTenant = "tenant-alpha"
	goldenOP     = "0195b2c6-9f40-7c01-8000-000000000001"
	goldenST     = "0195b2c6-9f40-7c02-8000-000000000001"
	goldenJB     = "0195b2c6-9f40-7c03-8000-000000000001"
)

// TestAnchor_StableAcrossCalls (P-1): Anchor is a pure function of its parts.
func TestAnchor_StableAcrossCalls(t *testing.T) {
	parts := []string{"operation.step.finished", goldenTenant, goldenOP, goldenST, "succeeded"}
	first := Anchor(parts...)
	for i := 0; i < 5; i++ {
		if got := Anchor(parts...); got != first {
			t.Fatalf("Anchor not stable: call %d = %s, want %s", i, got, first)
		}
	}
	equalParts := Anchor("a", "b")
	if Anchor("a", "b") != equalParts {
		t.Fatal("equal parts must produce equal IDs")
	}
	if Anchor("a", "b") == Anchor("a", "c") {
		t.Fatal("different parts must produce different IDs")
	}
}

// TestAnchor_GoldenConstants (P-1/P-2): full 128-bit goldens from the contract
// part lists (NUL-joined, namespace 38248e3a-32f0-4929-9b56-b2dbd75aa849),
// cross-verified against an independent RFC 4122 v5 implementation. These lock
// the derivation contract against drift: any change to the namespace,
// separator, part order, or part content breaks the match.
func TestAnchor_GoldenConstants(t *testing.T) {
	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{"G1 operation.started", []string{"operation.started", goldenTenant, goldenOP, "", "running"},
			"d0551f9f-d7e9-5773-aa06-fe022460ba93"},
		{"G2 operation.step.finished unknown", []string{"operation.step.finished", goldenTenant, goldenOP, goldenST, "unknown"},
			"0a0ccd8c-5984-5a83-8d15-f2438cfc4516"},
		{"G2b operation.step.finished succeeded", []string{"operation.step.finished", goldenTenant, goldenOP, goldenST, "succeeded"},
			"6aba9a00-8639-5474-9bf6-fa430aa22db0"},
		{"G3 operation.reconciled", []string{"operation.reconciled", goldenTenant, goldenOP, "", "unknown"},
			"73564c08-3ffc-5aee-8d6b-de886cb93737"},
		{"G4 operation.compensating", []string{"operation.compensating", goldenTenant, goldenOP, "", "compensating"},
			"b6c7bfca-ce35-5331-af9b-7642617ab781"},
		{"G5 operation.compensation.finished", []string{"operation.compensation.finished", goldenTenant, goldenOP, goldenST, "succeeded"},
			"f918e350-c761-538d-b1e8-f5917ce6ec68"},
		{"G6 operation.compensation.reconciled", []string{"operation.compensation.reconciled", goldenTenant, goldenOP, goldenST, "unknown"},
			"3ddf8a44-1486-5513-8b47-76943e8d1738"},
		{"G7 operation.finished", []string{"operation.finished", goldenTenant, goldenOP, "", "completed"},
			"d8fff40e-b4ef-56ac-bbea-3c2d1c353405"},
		{"G8 sync.job.finished", []string{"sync.job.finished", goldenTenant, goldenJB, "completed"},
			"5d165a06-322e-5b95-beb1-07ea5df9db47"},
		{"G9 audit_hold.set", []string{"audit_hold.set", goldenTenant, "account", "acct-1024", "litigation hold", "2026-12-31T23:59:59.123456Z"},
			"411ae5c5-efc1-5f16-a381-fd3afd445816"},
		{"G9b audit_hold.set empty", []string{"audit_hold.set", goldenTenant, "account", "acct-1024", "", ""},
			"ee512dbf-6ea3-52d5-902a-669e5219fc79"},
		{"G10 audit_hold.removed", []string{"audit_hold.removed", goldenTenant, "account", "acct-1024"},
			"5d349dda-a820-5abf-a9f0-efe421a58f0f"},
		{"G11 legacy.cutover.set legacy->shadow", []string{"legacy.cutover.set", goldenTenant, "shadow", "legacy"},
			"55090245-2231-5fc5-9671-5db846c5dd82"},
		{"G12 legacy.cutover.set shadow->shadow", []string{"legacy.cutover.set", goldenTenant, "shadow", "shadow"},
			"67acd7a2-5791-54bf-891b-03cea4311827"},
		{"G13 legacy.cutover.set rolled_back->legacy", []string{"legacy.cutover.set", goldenTenant, "legacy", "rolled_back"},
			"3c540abf-4417-56c2-b726-f26a89b0b157"},
		{"S1 separator pair a", []string{"ab", "c"},
			"251d9788-4b10-57f0-a935-e4ca289d517e"},
		{"S2 separator pair b", []string{"a", "bc"},
			"c9004912-9fc0-5f72-9689-670c12d50dcb"},
		// Assertions the fixtures lock: G9b != G10 (action discriminator),
		// G11 != G12 != G13 (transition identity), G2 != G2b (status in
		// anchor), G1/G3/G4/G7 carry a status slot despite being step-less.
	}
	seen := map[ID]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Anchor(tc.parts...)
			if got.String() != tc.want {
				t.Fatalf("Anchor(%q) = %s, want %s", tc.parts, got, tc.want)
			}
			if prev, dup := seen[got]; dup {
				t.Fatalf("Anchor(%q) collides with %s", tc.parts, prev)
			}
			seen[got] = tc.name
		})
	}
	if seen[Anchor("audit_hold.set", goldenTenant, "account", "acct-1024", "", "")] == seen[Anchor("audit_hold.removed", goldenTenant, "account", "acct-1024")] {
		t.Fatal("put(\"\",nil) must not collide with removed (G9b != G10)")
	}
}

// TestAnchor_WellFormedUUIDv5 (P-3): the result is an RFC 4122 UUIDv5 —
// version nibble 0101 in byte 6 and variant bits 10 in byte 8.
func TestAnchor_WellFormedUUIDv5(t *testing.T) {
	for _, parts := range [][]string{
		{"a", "b"},
		{"operation.reconciled", goldenTenant, goldenOP, "", "unknown"},
		{""},
	} {
		id := Anchor(parts...)
		if id.Version() != 5 {
			t.Fatalf("Anchor(%q).Version() = %d, want 5", parts, id.Version())
		}
		if id.Variant() != uuid.RFC4122 {
			t.Fatalf("Anchor(%q).Variant() = %v, want RFC4122", parts, id.Variant())
		}
		if id[6]&0xF0 != 0x50 {
			t.Fatalf("Anchor(%q) byte 6 high nibble = %#x, want 0x50 (version 5)", parts, id[6]&0xF0)
		}
	}
}

// TestAnchor_NULSeparatedParts (P-4): NUL separation keeps ("ab","c") and
// ("a","bc") distinct (goldens above), and the NUL-injection fix rejects any
// part containing U+0000 before hashing — the ambiguity demonstrated by the
// contract attack (Anchor("a\x00b","c") == Anchor("a","b\x00c")) can no
// longer be constructed.
func TestAnchor_NULSeparatedParts(t *testing.T) {
	a := Anchor("ab", "c")
	b := Anchor("a", "bc")
	if a == b {
		t.Fatalf("NUL separation broken: Anchor(\"ab\",\"c\") == Anchor(\"a\",\"bc\") == %s", a)
	}

	// The joined byte strings ARE equal — that is exactly why raw NUL input is
	// ambiguous and must be rejected.
	if strings.Join([]string{"a\x00b", "c"}, "\x00") != strings.Join([]string{"a", "b\x00c"}, "\x00") {
		t.Fatal("test premise broken: injection pair is not byte-identical after joining")
	}
	for _, parts := range [][]string{
		{"a\x00b", "c"},
		{"a", "b\x00c"},
		{"\x00"},
		{"a", "\x00", "b"},
	} {
		if !panics(func() { Anchor(parts...) }) {
			t.Fatalf("Anchor(%q) must panic (NUL byte in part)", parts)
		}
	}
	if !panics(func() { Anchor("ok\x00") }) {
		t.Fatal("trailing NUL in part must panic")
	}

	// Sensitivity matrix: changing any single part changes the ID; arity is
	// part of the encoding (E1 != E2).
	base := []string{"operation.step.finished", goldenTenant, goldenOP, goldenST, "succeeded"}
	baseID := Anchor(base...)
	for i := range base {
		mutated := append([]string(nil), base...)
		mutated[i] += "-x"
		if got := Anchor(mutated...); got == baseID {
			t.Fatalf("changing part %d (%q) did not change the ID", i, base[i])
		}
	}
	if Anchor("x") == Anchor("x", "") {
		t.Fatal("arity must be part of the encoding: Anchor(\"x\") == Anchor(\"x\",\"\")")
	}
}

// TestAnchor_ZeroPartsPanics (D-1 delta): every anchor site has fixed arity
// >= 4, so a zero-part anchor is a programming error and must be rejected.
func TestAnchor_ZeroPartsPanics(t *testing.T) {
	if !panics(func() { Anchor() }) {
		t.Fatal("Anchor() with zero parts must panic")
	}
}

// TestAnchor_V5VsV7Byte6Separation: UUIDv5 and UUIDv7 force disjoint version
// nibbles in byte 6 (0101 vs 0111), so no derived v5 event_id can ever equal
// a v7 ID (fact.ID, legacy rows, the C2 callback site) in any column.
func TestAnchor_V5VsV7Byte6Separation(t *testing.T) {
	const v5Nibble, v7Nibble = byte(0x50), byte(0x70)
	if v5Nibble == v7Nibble {
		t.Fatal("test premise broken: v5 and v7 nibbles must differ")
	}
	seen := map[ID]bool{}
	for i := 0; i < 200; i++ {
		anchor := Anchor("operation.step.finished", goldenTenant, goldenOP, goldenST, "step-"+string(rune('a'+i%26)))
		if anchor[6]&0xF0 != v5Nibble {
			t.Fatalf("v5 byte 6 high nibble = %#x, want %#x", anchor[6]&0xF0, v5Nibble)
		}
		if anchor.Version() != 5 || anchor.Variant() != uuid.RFC4122 {
			t.Fatalf("anchor %s is not a well-formed v5", anchor)
		}
		v7 := NewID()
		if v7[6]&0xF0 != v7Nibble {
			t.Fatalf("v7 byte 6 high nibble = %#x, want %#x", v7[6]&0xF0, v7Nibble)
		}
		if v7.Version() != 7 || v7.Variant() != uuid.RFC4122 {
			t.Fatalf("NewID() %s is not a well-formed v7", v7)
		}
		if anchor == v7 {
			t.Fatalf("v5/v7 nibble separation violated: anchor %s == v7 %s", anchor, v7)
		}
		if seen[v7] {
			t.Fatalf("NewID() repeated %s", v7)
		}
		seen[anchor], seen[v7] = true, true
	}
}

// TestRenderAnchorTime (R7 fixtures): canonical time rendering is always UTC,
// exactly six fractional digits, always Z. Offset inputs normalize (T1),
// sub-microsecond inputs truncate to the stored instant (T2 — byte-identical
// to G9's part), nil renders as the empty part (T3), and a non-nil zero time
// renders as 0001-01-01T00:00:00.000000Z (T4), distinct from nil.
func TestRenderAnchorTime(t *testing.T) {
	parse := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return &v
	}
	cases := []struct {
		name string
		in   *time.Time
		want string
	}{
		{"T1 offset normalizes", parse("2026-12-31T23:59:59.123456+01:00"), "2026-12-31T22:59:59.123456Z"},
		{"T2 sub-microsecond truncates", parse("2026-12-31T23:59:59.123456789Z"), "2026-12-31T23:59:59.123456Z"},
		{"T3 nil is empty", nil, ""},
		{"T4 zero time", new(time.Time), "0001-01-01T00:00:00.000000Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderAnchorTime(tc.in); got != tc.want {
				t.Fatalf("RenderAnchorTime(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	// T2's truncated rendering is byte-identical to G9's expiry part, so the
	// anchored event_id of a reformatted retry equals the original (dedup
	// instead of double-recording).
	if got := Anchor("audit_hold.set", goldenTenant, "account", "acct-1024", "litigation hold", RenderAnchorTime(parse("2026-12-31T23:59:59.123456789Z"))); got.String() != "411ae5c5-efc1-5f16-a381-fd3afd445816" {
		t.Fatalf("T2 anchor = %s, want G9 411ae5c5-efc1-5f16-a381-fd3afd445816", got)
	}
	if got := Anchor("audit_hold.set", goldenTenant, "account", "acct-1024", "litigation hold", RenderAnchorTime(new(time.Time))); got.String() != "6dcd6084-af36-5289-804c-e5295dd1771f" {
		t.Fatalf("T4 anchor = %s, want 6dcd6084-af36-5289-804c-e5295dd1771f", got)
	}
}

func panics(f func()) (did bool) {
	defer func() {
		if recover() != nil {
			did = true
		}
	}()
	f()
	return false
}
