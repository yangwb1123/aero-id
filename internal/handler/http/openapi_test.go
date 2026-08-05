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

// requiredClientPaths is the account-console contract surface (P7-03 user
// portal + P7-04 admin portal); each path+method must exist in the spec.
var requiredClientPaths = [][2]string{
	{"/account-datasets", "GET"},
	{"/me", "GET"},
	{"/me/profile", "GET"},
	{"/me/profile", "PATCH"},
	{"/me/sources", "GET"},
	{"/me/memberships", "GET"},
	{"/me/data", "GET"},
	{"/me/overview", "GET"},
	{"/me/activity", "GET"},
	{"/me/sync", "POST"},
	{"/me/export", "POST"},
	{"/me/erase", "POST"},
	{"/sync/jobs", "GET"},
	{"/sync/jobs/{id}", "GET"},
	{"/sync/jobs/{id}/reconcile", "POST"},
	{"/sync/jobs/{id}/export/download", "GET"},
	{"/operations", "GET"},
	{"/operations", "POST"},
	{"/operations/{id}", "GET"},
	{"/operations/{id}/run", "POST"},
	{"/operations/{id}/reconcile", "POST"},
	{"/audit/events", "GET"},
	{"/audit/operations/{id}", "GET"},
	{"/audit/replays", "POST"},
	{"/audit/replays/projection", "POST"},
	{"/audit/replays/{id}", "GET"},
	{"/admin/callback-subscriptions", "GET"},
	{"/admin/callback-subscriptions", "POST"},
	{"/admin/callback-subscriptions/{id}", "DELETE"},
	{"/admin/callback-deliveries/{id}/replay", "POST"},
	{"/admin/regions/promote", "POST"},
	{"/admin/regions/directory/{account_id}", "GET"},
}

func TestOpenAPISpec_CoversClientContract(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for _, pair := range requiredClientPaths {
		path, method := pair[0], pair[1]
		operations, ok := spec.Paths[path]
		if !ok {
			t.Errorf("missing path %s in openapi spec", path)
			continue
		}
		op, ok := operations[lowerMethod(method)]
		if !ok {
			t.Errorf("missing method %s on path %s", method, path)
			continue
		}
		if op.OperationID == "" {
			t.Errorf("path %s %s has no operationId", method, path)
		}
		if len(op.Responses) == 0 {
			t.Errorf("path %s %s has no responses", method, path)
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
