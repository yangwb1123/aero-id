package migrate

import (
	"strings"
	"testing"
)

func TestSchemaVersionMessageIsActionable(t *testing.T) {
	message := schemaVersionMessage(68, MinOutboxRetryEligibilityVersion)
	for _, want := range []string{"000068", "000069", "migrate up", "make migrate-up"} {
		if !strings.Contains(message, want) {
			t.Fatalf("schema version message %q does not contain %q", message, want)
		}
	}
}
