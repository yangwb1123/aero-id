package fixture

import "github.com/aero/aero-id/internal/file"

// Rule (a) positive fixture: any .go file importing a quarantined dead tree
// (internal/{file,message,notification,audit}) must be flagged, wherever it
// lives — including *_test.go files ("no package anywhere").
