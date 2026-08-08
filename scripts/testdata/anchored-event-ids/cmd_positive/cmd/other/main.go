package fixture

// I6 cmd-path fixture for check-anchored-event-ids.sh: a random audit
// EventID minted under a root named cmd/ that is NOT the C2 allow-listed
// file. Scanned with allow=1 in the self-test: the gate must still flag it —
// proving cmd roots are actually scanned and the C2 alternation exempts only
// the exact allow-listed files, not every file under cmd/.

func replay() {
	_ = domain.AuditFact{EventID: idgen.NewID()} // R-1 shape outside cmd/audit-replay/main.go
}
