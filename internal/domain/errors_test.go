package domain

import (
	stderrors "errors"
	"testing"
)

func TestErrNotFoundIsError(t *testing.T) {
	if ErrNotFound.Error() != "domain: not found" {
		t.Errorf("ErrNotFound = %q", ErrNotFound.Error())
	}
	if !stderrors.Is(ErrNotFound, ErrNotFound) {
		t.Error("errors.Is should match itself")
	}
}

func TestErrDuplicateIsError(t *testing.T) {
	if ErrDuplicate.Error() != "domain: duplicate" {
		t.Errorf("ErrDuplicate = %q", ErrDuplicate.Error())
	}
	if !stderrors.Is(ErrDuplicate, ErrDuplicate) {
		t.Error("errors.Is should match itself")
	}
}
