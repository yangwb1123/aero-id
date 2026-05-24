package logger

import (
	"testing"
)

func TestNew_invalidLevel(t *testing.T) {
	_, err := New("nonsense", "console")
	if err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestNew_validLevels(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error"} {
		t.Run(lvl, func(t *testing.T) {
			l, err := New(lvl, "json")
			if err != nil {
				t.Fatalf("New(%q): %v", lvl, err)
			}
			if l == nil {
				t.Fatal("nil logger")
			}
		})
	}
}
