package idgen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
		// G14/G14b/G14c region.promote: 6 parts incl. the CAS fencing epoch as
		// a decimal string (strconv.FormatInt). A retry of the same attempt
		// re-derives the same ID (ledger dedup), while each successive epoch
		// derives a distinct ID — distinct promotion attempts never collide.
		{"G14 region.promote", []string{"audit", "region.promote", goldenTenant, goldenOP, "us-east", "0"},
			"0c9b9631-6da3-5c20-813d-4ec76e5dc961"},
		{"G14b region.promote wrong epoch", []string{"audit", "region.promote", goldenTenant, goldenOP, "us-east", "1"},
			"efd6375b-4c86-57b1-ac7b-0e821537ce47"},
		{"G14c region.promote epoch 2", []string{"audit", "region.promote", goldenTenant, goldenOP, "us-east", "2"},
			"6b15c036-15c8-581b-b9c4-03c6b0f2d01b"},
		// G15 preference.set: 5 parts incl. the sha256 payload digest slot
		// (fixed 64-hex-char literal standing in for preferenceSetDigest
		// output). A payload change derives a distinct ID — a later
		// legitimate Set by the same user is never deduped away — while a
		// retry of the same payload re-derives the same ID.
		{"G15 preference.set", []string{"audit", "preference.set", goldenTenant, "u-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			"52d8beb8-b5fb-55ba-ad4b-e3ff011c37e5"},
		{"G16 projection.replication.quarantine", []string{"projection.replication.quarantine.v1", "local", "fake:test", goldenOP},
			"55ce71a3-3668-5ff9-8ca6-7cff018444e0"},
		{"G17 event.quarantine", []string{"event.quarantine.v1", "example-fingerprint"},
			"68c8ca5a-323b-53c0-ab6b-af5876dad94a"},
		// G18-G23 operation step/compensation facts: 6 parts — the first five
		// slots are byte-identical to the operation family above, plus the
		// step's durable claim counter as a decimal string (strconv.Itoa), the
		// same per-attempt discriminator shape as the G14 fencing epoch. A
		// retry at the same attempt re-derives the same ID (ledger dedup);
		// each successive attempt of a step whose remote outcome stays
		// unknown derives a distinct ID, so no reconcile pass folds into an
		// earlier pass's row. The step-less G1/G3/G4/G7 shapes above keep
		// arity 5 and must never be re-derived with this sixth slot.
		{"G18 operation.step.finished unknown attempt 1", []string{"operation.step.finished", goldenTenant, goldenOP, goldenST, "unknown", "1"},
			"4674e436-85bf-59a2-af06-b75d564c6cd4"},
		{"G19 operation.step.finished unknown attempt 2", []string{"operation.step.finished", goldenTenant, goldenOP, goldenST, "unknown", "2"},
			"41d7c885-6a29-5b19-a25b-a99b2ff9d31f"},
		{"G20 operation.step.finished succeeded attempt 1", []string{"operation.step.finished", goldenTenant, goldenOP, goldenST, "succeeded", "1"},
			"c3f2623b-0201-5c84-890c-19559a50b061"},
		{"G21 operation.step.finished succeeded attempt 2", []string{"operation.step.finished", goldenTenant, goldenOP, goldenST, "succeeded", "2"},
			"62337cbc-021e-5d1b-a4a2-9552d613463c"},
		{"G22 operation.compensation.finished succeeded attempt 1", []string{"operation.compensation.finished", goldenTenant, goldenOP, goldenST, "succeeded", "1"},
			"6aa41161-2b25-59e7-8b8e-2cc821f36894"},
		{"G23 operation.compensation.finished succeeded attempt 2", []string{"operation.compensation.finished", goldenTenant, goldenOP, goldenST, "succeeded", "2"},
			"919d2391-ff57-5090-a30d-f995dc3e1888"},
		{"G24 operation.compensation.reconciled unknown attempt 1", []string{"operation.compensation.reconciled", goldenTenant, goldenOP, goldenST, "unknown", "1"},
			"1d924a85-7d33-5947-b30a-9c12d3abf2bb"},
		{"G25 operation.compensation.reconciled unknown attempt 2", []string{"operation.compensation.reconciled", goldenTenant, goldenOP, goldenST, "unknown", "2"},
			"5ac8e8e3-f8cd-5624-8243-7f87cb10f65a"},
		// Block facts are execution-scoped by the new durable fact ID, not by
		// the resource or denial code. Expiry facts use the durable attempt.
		{"G26 operation.execution.blocked execution 1", []string{"operation.execution.blocked", goldenTenant, goldenOP, "operation.authorization_denied", goldenJB},
			"0e91fef7-6cb0-5e2b-a8d8-d46eb8d311b9"},
		{"G27 operation.execution.blocked execution 2", []string{"operation.execution.blocked", goldenTenant, goldenOP, "operation.authorization_denied", goldenST},
			"dc67f5df-e8bc-5412-ac25-f0069b6aefeb"},
		{"G28 operation.step.expired attempt 1", []string{"operation.step.expired", goldenTenant, goldenOP, goldenST, "unknown", "1"},
			"1263636b-4055-53da-84f7-bf62f580e9bc"},
		{"G29 operation.step.expired attempt 2", []string{"operation.step.expired", goldenTenant, goldenOP, goldenST, "unknown", "2"},
			"028d0a2c-5d97-5736-873e-98e92bd504c8"},
		{"G30 operation.compensation.expired attempt 1", []string{"operation.compensation.expired", goldenTenant, goldenOP, goldenST, "unknown", "1"},
			"5bc1a18a-da7c-53f1-8652-4efc3e4583bc"},
		{"G31 operation.compensation.expired attempt 2", []string{"operation.compensation.expired", goldenTenant, goldenOP, goldenST, "unknown", "2"},
			"70fbb1fc-1d07-5c5e-9ed6-a070924380c4"},
		{"G32 operation.compensation.deferred attempt 1", []string{"operation.compensation.deferred", goldenTenant, goldenOP, goldenST, "pending", "1"},
			"c51edcf0-89d1-59d3-a371-c6386f7819d6"},
		{"G33 sync.execution.blocked execution 1", []string{"sync.execution.blocked", goldenTenant, goldenJB, "sync.authorization_denied", goldenOP},
			"2353ecd9-b2b6-5cb0-a0fb-7e5e3dbbbb2e"},
		{"G34 sync.execution.blocked execution 2", []string{"sync.execution.blocked", goldenTenant, goldenJB, "sync.authorization_denied", goldenST},
			"fd201397-2b2c-5b8a-918d-77a40e0ef059"},
		{"G35 sync.execution.expired attempt 1", []string{"sync.execution.expired", goldenTenant, goldenJB, "1"},
			"69e7b75d-17b7-581b-8e1f-affc3cd2fd3d"},
		{"G36 sync.execution.expired attempt 2", []string{"sync.execution.expired", goldenTenant, goldenJB, "2"},
			"9358a592-c9b3-58f0-ac1c-1f35f20c5ffe"},
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
	// CAS-per-attempt dedup: the fencing epoch is part of the anchor, so two
	// distinct promotion attempts (epoch 0 -> 1 -> 2) never collide at the
	// ledger, while the per-case 128-bit pins prove a retry of the same
	// epoch re-derives the same ID.
	if Anchor("audit", "region.promote", goldenTenant, goldenOP, "us-east", "0") ==
		Anchor("audit", "region.promote", goldenTenant, goldenOP, "us-east", "1") {
		t.Fatal("successive fencing epochs must derive distinct EventIDs (G14 != G14b)")
	}
	if Anchor("audit", "region.promote", goldenTenant, goldenOP, "us-east", "1") ==
		Anchor("audit", "region.promote", goldenTenant, goldenOP, "us-east", "2") {
		t.Fatal("successive fencing epochs must derive distinct EventIDs (G14b != G14c)")
	}
	// Attempt-scoped step facts: the durable claim counter occupies the sixth
	// slot, so successive reconcile attempts of the same step never collapse
	// onto one ledger row (G18 != G19, G20 != G21, G22 != G23, G24 != G25).
	for _, parts := range [][]string{
		{"operation.step.finished", goldenTenant, goldenOP, goldenST, "unknown"},
		{"operation.step.finished", goldenTenant, goldenOP, goldenST, "succeeded"},
		{"operation.compensation.finished", goldenTenant, goldenOP, goldenST, "succeeded"},
		{"operation.compensation.reconciled", goldenTenant, goldenOP, goldenST, "unknown"},
	} {
		one := append(append([]string(nil), parts...), "1")
		two := append(append([]string(nil), parts...), "2")
		if Anchor(one...) == Anchor(two...) {
			t.Fatalf("successive attempt slots must derive distinct EventIDs for %q", parts[0])
		}
		// Arity change alone changes the ID, so appending the slot can never
		// silently re-derive the pre-attempt 5-tuple fact for the same pass.
		if Anchor(parts...) == Anchor(one...) {
			t.Fatalf("the attempt slot must participate in the encoding for %q", parts[0])
		}
	}
}

// TestAnchor_WellFormedUUIDv5 (P-3): the result is an RFC 4122 UUIDv5 —
// version nibble 0101 in byte 6 and variant bits 10 in byte 8.
func TestAnchor_WellFormedUUIDv5(t *testing.T) {
	for _, parts := range [][]string{
		{"a", "b"},
		{"operation.reconciled", goldenTenant, goldenOP, "", "unknown"},
		// Empty parts stay legal (G1/G3/G4/G7 carry an empty status/step slot);
		// only arity is floored, so this case is written at the floor arity.
		{"", ""},
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
		{"a", "\x00"},
		{"a", "\x00", "b"},
	} {
		msg, ok := panicText(func() { Anchor(parts...) })
		if !ok {
			t.Fatalf("Anchor(%q) must panic (NUL byte in part)", parts)
		}
		if !strings.Contains(msg, "NUL byte") {
			t.Fatalf("Anchor(%q) panicked with %q, want the NUL-byte guard; an arity panic here means the fixture no longer tests what it claims", parts, msg)
		}
	}
	if msg, ok := panicText(func() { Anchor("ok\x00", "tail") }); !ok || !strings.Contains(msg, "NUL byte") {
		t.Fatalf(`Anchor("ok\x00","tail") = (%q, panicked=%v), want the NUL-byte panic`, msg, ok)
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
	if Anchor("x", "y") == Anchor("x", "y", "") {
		t.Fatal("arity must be part of the encoding: Anchor(\"x\",\"y\") == Anchor(\"x\",\"y\",\"\")")
	}
}

// TestAnchor_MinimumArity (D-1 delta): a site whose parts were dropped must
// fail fast rather than derive. Arity is part of the encoding, so a short
// anchor yields a valid, perfectly stable UUIDv5 that the audit ledger then
// dedups on (tenant_id, source, event_id) — folding a later execution of that
// truncated shape onto the first row with no error and no log line. The
// enforced floor is minAnchorArity (2), occupied live by the
// event.quarantine.v1 + wire-fingerprint surrogate.
func TestAnchor_MinimumArity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []string
	}{
		{"zero_parts", nil},
		{"one_part", []string{"x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := panicText(func() { Anchor(tc.parts...) })
			if !ok {
				t.Fatalf("Anchor(%q): %d parts is below the enforced floor and must panic", tc.parts, len(tc.parts))
			}
			if !strings.Contains(msg, "minAnchorArity") {
				t.Fatalf("panic %q must name the enforced floor minAnchorArity", msg)
			}
		})
	}

	// The arity guard runs before the NUL guard: an under-arity call whose part
	// also carries U+0000 must report the arity error, not the NUL error.
	if msg, ok := panicText(func() { Anchor("x\x00") }); !ok || !strings.Contains(msg, "minAnchorArity") {
		t.Fatalf(`Anchor("x\x00") = (%q, panicked=%v), want the minAnchorArity panic`, msg, ok)
	}

	// The floor itself derives: arity 2 is a legal anchor, not a degenerate
	// one, and must stay byte-identical for existing callers.
	id := Anchor("a", "b")
	if id.Version() != 5 || id.Variant() != uuid.RFC4122 {
		t.Fatalf("arity-2 anchor %s is not a well-formed RFC4122 v5", id)
	}
	if id != Anchor("a", "b") {
		t.Fatalf("arity-2 anchor is not deterministic: %s != %s", id, Anchor("a", "b"))
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

func TestCanonicalEqual_AcceptedWireForms(t *testing.T) {
	// The provenance defect: 0195B2C6-9F40-7C01-8000-000000000001 is the same
	// 128-bit identity as the canonical lowercase rendering, so a source that
	// declares it must never be recorded as having a derived identity.
	const idText = "0195b2c6-9f40-7c01-8000-000000000001"
	id := uuid.MustParse(idText)
	truncated := safeWireIDTruncation(strings.Repeat("a", 512))

	cases := []struct {
		name string
		id   ID
		text string
		want bool
	}{
		{"canonical hyphenated", id, idText, true},
		{"upper-case hyphenated", id, "0195B2C6-9F40-7C01-8000-000000000001", true},
		{"mixed-case hyphenated", id, "0195b2C6-9F40-7c01-8000-000000000001", true},
		{"urn prefix upper-case", id, "urn:uuid:0195B2C6-9F40-7C01-8000-000000000001", true},
		{"urn prefix lower-case", id, "URN:UUID:0195b2c6-9f40-7c01-8000-000000000001", true},
		{"32 hex lower-case", id, "0195b2c69f407c018000000000000001", true},
		{"32 hex upper-case", id, "0195B2C69F407C018000000000000001", true},
		{"braced", id, "{0195b2c6-9f40-7c01-8000-000000000001}", true},
		{"leading and trailing whitespace", id, "  " + idText + "\n", true},
		{"surrounding tabs", id, "\t" + idText + "\t", true},

		{"different valid UUID", id, "0195b2c6-9f40-7c01-8000-000000000002", false},
		{"different value in another rendering", id, "urn:uuid:0195b2c6-9f40-7c01-8000-000000000002", false},
		{"different value in mixed case", id, "0195b2C6-9F40-7c01-8000-00000000000a", false},
		{"zero UUID text against parsed id", id, "00000000-0000-0000-0000-000000000000", false},
		{"zero id argument", ID{}, idText, false},
		{"empty", id, "", false},
		{"whitespace only", id, "   \t\n", false},
		{"unparseable", id, "not-a-uuid", false},
		{"control characters", id, idText[:1] + "\x00\x1f" + idText[1:], false},
		{"512 byte non-uuid", id, strings.Repeat("a", 512), false},
		{"safeWireID truncated rendering", id, truncated, false},
		{"truncated 29 hex form", id, "0195b2c69f407c018000000000001", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanonicalEqual(tc.id, tc.text); got != tc.want {
				t.Fatalf("CanonicalEqual(%s, %q) = %v, want %v", tc.id, tc.text, got, tc.want)
			}
		})
	}
}

// TestCanonicalEqual_ZeroIDNeverMatchesAnyText pins the ErrZeroID half of the
// contract: because CanonicalEqual delegates to Parse, no text at all — not
// even the zero UUID's own rendering — can equal a zero id argument.
func TestCanonicalEqual_ZeroIDNeverMatchesAnyText(t *testing.T) {
	for _, text := range []string{"", "00000000-0000-0000-0000-000000000000",
		"{00000000-0000-0000-0000-000000000000}", "urn:uuid:00000000-0000-0000-0000-000000000000"} {
		if CanonicalEqual(ID{}, text) {
			t.Fatalf("CanonicalEqual(zero, %q) = true, want false", text)
		}
	}
}

// panicText runs f and reports the recovered panic value as text so a test can
// assert *which* guard fired. Since Anchor has two independent panic guards
// (minimum arity, NUL byte), "it panicked" alone can no longer distinguish an
// under-arity rejection from a NUL rejection — without the message, a fixture
// that accidentally trips the arity guard would pass vacuously.
func panicText(f func()) (msg string, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			msg, ok = fmt.Sprint(r), true
		}
	}()
	f()
	return "", false
}

// safeWireIDTruncation reproduces the ingest boundary's over-long wire-id
// bounding (grpc/event_ingest.go safeWireID) so the test can pin that a
// truncated wire id — which is not a UUID at all — never compares equal.
func safeWireIDTruncation(value string) string {
	if len(value) <= 256 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return value[:239] + "#" + hex.EncodeToString(sum[:])[:16]
}
