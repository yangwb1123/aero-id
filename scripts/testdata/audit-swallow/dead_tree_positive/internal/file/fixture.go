// Package file is a deliberate, benign reintroduction of a quarantined dead
// tree, used by check-audit-swallow.sh's fixture-mode self-test to prove the
// FM-12 guard's totality: this package compiles and its audit call PROPAGATES
// its error (the fail-closed form the live layer requires — deliberately NOT
// a P1/P2 swallow shape), yet its mere existence must turn the gate red.
package file

// Writer is the minimal propagated-error audit surface.
type Writer interface {
	Log(e *Event) error
}

// Event is a minimal audit event shape.
type Event struct {
	Actor string
	Kind  string
}

// Audit writes one event, propagating the error (fail-closed).
func Audit(w Writer, e *Event) error {
	if err := w.Log(e); err != nil {
		return err
	}
	return nil
}
