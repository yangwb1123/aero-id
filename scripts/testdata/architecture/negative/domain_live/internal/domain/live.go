package domain

// Rule (f) negative fixture: every exported symbol below is consumed by a
// qualified domain.X reference in internal/service/live.go. SnapshotFresh is
// a const-block member; Trap's NotificationType field is the field-name trap
// that pins qualified matching — the label must never count as a reference
// to a domain symbol. The package imports nothing, so rule (b) stays clean.
type Account struct{}

type SnapshotStatus string

const (
	SnapshotFresh SnapshotStatus = "fresh"
)

type Trap struct {
	NotificationType string
}
