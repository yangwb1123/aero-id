package domain

import (
	"context"
	"testing"
)

func TestCanReadFile_public(t *testing.T) {
	pub := &File{OwnerUID: "owner", Visibility: VisibilityPublic}
	if !CanReadFile("stranger", pub) {
		t.Error("public file should be readable by anyone")
	}
}

func TestCanReadFile_private(t *testing.T) {
	priv := &File{OwnerUID: "owner", Visibility: VisibilityPrivate}
	if CanReadFile("stranger", priv) {
		t.Error("private file should not be readable by a stranger")
	}
	if !CanReadFile("owner", priv) {
		t.Error("owner should read its own private file")
	}
}

func TestCanReadMessage(t *testing.T) {
	m := &Message{FromUID: "a", ToUID: "b"}
	if !CanReadMessage("a", m) || !CanReadMessage("b", m) {
		t.Error("both participants should read the message")
	}
	if CanReadMessage("c", m) {
		t.Error("non-participant should not read the message")
	}
}

func TestErrNotFound(t *testing.T) {
	if ErrNotFound == nil {
		t.Fatal("ErrNotFound must not be nil")
	}
}

func TestErrDuplicate(t *testing.T) {
	if ErrDuplicate == nil {
		t.Fatal("ErrDuplicate must not be nil")
	}
}

func TestAuditLoggerInterface(t *testing.T) {
	var _ AuditLogger = (*mockAuditor)(nil)
}

type mockAuditor struct{}

func (m *mockAuditor) Log(_ context.Context, _ *AuditEvent) error { return nil }
