package http_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// openAPISpec is parsed from the committed account-console contract. This test
// guards the contract surface: every path the independent client relies on must
// be present and well-formed, so the spec cannot silently drift from the API
// (P7 client contract hygiene).
type openAPISpec struct {
	Paths      map[string]map[string]openAPIOperation `yaml:"paths"`
	Components struct {
		Schemas map[string]any `yaml:"schemas"`
	} `yaml:"components"`
}

type openAPIOperation struct {
	OperationID string `yaml:"operationId"`
	Responses   map[string]any
}

func loadOpenAPISpec(t *testing.T) *openAPISpec {
	t.Helper()
	// Locate the repo root robustly regardless of the test working directory.
	root := "."
	var specPath string
	for depth := 0; depth < 6; depth++ {
		candidate := filepath.Join(root, "api", "openapi", "aero-id-account.yaml")
		if _, err := os.Stat(candidate); err == nil {
			specPath = candidate
			break
		}
		root = filepath.Join(root, "..")
	}
	if specPath == "" {
		t.Fatal("openapi spec not found; run tests from the repository root")
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read openapi spec: %v", err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse openapi spec: %v", err)
	}
	return &spec
}

// TestOpenAPISpec_CoversClientContract guards the contract surface: every
// route in routeManifest (the single source of truth defined in
// openapi_reconcile_test.go) must be present and well-formed in the spec, so
// the spec cannot silently drift from the API (P7 client contract hygiene).
// The manifest replaces the former one-directional requiredClientPaths
// hardcoded subset, which pinned the unserved callback replay path.
func TestOpenAPISpec_CoversClientContract(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for _, r := range routeManifest {
		operations, ok := spec.Paths[r.path]
		if !ok {
			t.Errorf("missing path %s in openapi spec", r.path)
			continue
		}
		op, ok := operations[lowerMethod(r.method)]
		if !ok {
			t.Errorf("missing method %s on path %s", r.method, r.path)
			continue
		}
		if op.OperationID == "" {
			t.Errorf("path %s %s has no operationId", r.method, r.path)
		}
		if len(op.Responses) == 0 {
			t.Errorf("path %s %s has no responses", r.method, r.path)
		}
	}
}

func lowerMethod(method string) string {
	return strings.ToLower(method)
}

func TestOpenAPISpec_OperationsHaveSuccessResponses(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for path, operations := range spec.Paths {
		for method, op := range operations {
			if method == "parameters" {
				continue
			}
			if op.OperationID == "" {
				t.Errorf("%s %s: missing operationId", method, path)
			}
			if _, ok := op.Responses["200"]; !ok {
				if _, ok2 := op.Responses["201"]; !ok2 {
					if _, ok3 := op.Responses["202"]; !ok3 {
						t.Errorf("%s %s: no 2xx success response", method, path)
					}
				}
			}
		}
	}
}

func TestOpenAPISpec_HasCoreSchemas(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for _, schema := range []string{"ErrorEnvelope", "AccountView", "ProfileEnvelope", "AggregateEnvelope",
		"DatasetList", "SyncJobEnvelope", "OperationEnvelope", "AuditList", "ReplayRequest"} {
		if _, ok := spec.Components.Schemas[schema]; !ok {
			t.Errorf("missing schema %s", schema)
		}
	}
}
