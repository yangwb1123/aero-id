package domain

import "testing"

func TestMessageKindIsValid(t *testing.T) {
	valid := []MessageKind{MessageKindText, MessageKindImage, MessageKindFile, MessageKindSystem}
	for _, k := range valid {
		if !k.IsValid() {
			t.Errorf("%d should be valid", k)
		}
	}
	if MessageKind(0).IsValid() {
		t.Error("0 should not be valid")
	}
	if MessageKind(99).IsValid() {
		t.Error("99 should not be valid")
	}
}

func TestMessageKindIsSystem(t *testing.T) {
	if !MessageKindSystem.IsSystem() {
		t.Error("MessageKindSystem should be system")
	}
	if MessageKindText.IsSystem() {
		t.Error("MessageKindText should not be system")
	}
}

func TestReportStatusIsTerminal(t *testing.T) {
	if !ReportStatusResolved.IsTerminal() {
		t.Error("Resolved should be terminal")
	}
	if !ReportStatusDismissed.IsTerminal() {
		t.Error("Dismissed should be terminal")
	}
	if ReportStatusOpen.IsTerminal() {
		t.Error("Open should not be terminal")
	}
}

func TestPageNormalizeDefaults(t *testing.T) {
	p := Page{Limit: 0, Cursor: "c"}
	p.Normalize()
	if p.Limit != DefaultPageLimit {
		t.Errorf("Limit = %d, want %d", p.Limit, DefaultPageLimit)
	}
	if p.Cursor != "c" {
		t.Errorf("Cursor changed to %q", p.Cursor)
	}
}

func TestPageNormalizeMax(t *testing.T) {
	p := Page{Limit: 999}
	p.Normalize()
	if p.Limit != MaxPageLimit {
		t.Errorf("Limit = %d, want %d", p.Limit, MaxPageLimit)
	}
}

func TestPageNormalizeNegative(t *testing.T) {
	p := Page{Limit: -5}
	p.Normalize()
	if p.Limit != DefaultPageLimit {
		t.Errorf("Limit = %d, want %d", p.Limit, DefaultPageLimit)
	}
}

func TestPageNormalizeOne(t *testing.T) {
	p := Page{Limit: 1}
	p.Normalize()
	if p.Limit != 1 {
		t.Errorf("Limit = %d, want 1", p.Limit)
	}
}
