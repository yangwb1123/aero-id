package event

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProductionPublishersUseCanonicalSerializer(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate event package source")
	}
	for _, name := range []string{"http_publisher.go", "nats_publisher.go"} {
		source, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(source)
		if !strings.Contains(text, "marshalCanonicalEnvelope(envelope)") {
			t.Errorf("%s does not use the shared canonical serializer", name)
		}
	}
	for _, name := range []string{"http_publisher.go", "nats_publisher.go", "nats.go"} {
		source, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, forbidden := range []string{
			"must" + "MarshalEnvelope", "json." + "Marshal(envelope)",
			"json." + "Marshal(redactedEnvelope(envelope))",
		} {
			if strings.Contains(string(source), forbidden) {
				t.Errorf("%s contains forbidden direct serialization %q", name, forbidden)
			}
		}
	}
}
