package k8s_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Negative cases derive from the actual fully rendered, schema-valid bundle.
// Mutation anchors are asserted so failed test setup cannot create false greens.
func TestKubernetesStrictSchemaRejectsUnsafeDocuments(t *testing.T) {
	schemas, err := loadKubernetesSchemas(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := renderKubernetesSchemaFixtures(t)
	steady := readFile(t, filepath.Join(dir, "steady.yaml"))
	job := readFile(t, filepath.Join(dir, "migration-up.yaml"))
	cases := []struct{ name, original, old, replacement string }{
		{"unknown-nested-field", steady, "replicas: 3", "replicaCount: 3"},
		{"unknown-resource-field", steady, "kind: Namespace", "kind: Namespace\nnot_a_kubernetes_field: true"},
		{"integer-field-as-string", steady, "replicas: 3", "replicas: \"three\""},
		{"integer-field-as-list", steady, "containerPort: 8080", "containerPort: [8080]"},
		{"non-string-env-value", steady, "value: aero_id", "value: false"},
		{"duplicate-key", steady, "replicas: 3", "replicas: 3\n  replicas: 0"},
		{"unknown-kind", steady, "kind: Namespace", "kind: NamespaceTypo"},
		{"wrong-full-group", steady, "apiVersion: apps/v1", "apiVersion: apps.invalid/v1"},
		{"wrong-api-version", steady, "apiVersion: apps/v1", "apiVersion: apps/v2"},
		{"no-list-expansion-bypass", steady, "kind: Namespace", "kind: List"},
		{"missing-name", steady, "  name: aero-id\n", "  name: \"\"\n"},
		{"unsafe-migration-args-type", job, "args: [", "args: {"},
		{"wrong-migration-field-type", job, "backoffLimit: 0", "backoffLimit: \"never\""},
		{"unknown-migration-field", job, "backoffLimit: 0", "backofLimit: 0"},
		{"missing-required-container-name", job, "- name: migrate", "- missingName: migrate"},
	}
	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			if !strings.Contains(fixture.original, fixture.old) {
				t.Fatal("mutation anchor missing")
			}
			mutated := strings.Replace(fixture.original, fixture.old, fixture.replacement, 1)
			if _, err := validateKubernetesManifest(schemas, mutated); err == nil {
				t.Fatal("invalid rendered artifact passed strict validation")
			}
		})
	}
}

func TestKubernetesSchemaIntegrityAndNoExternalResolution(t *testing.T) {
	pin := kubernetesSchemaPins[0]
	root := repositoryRoot(t)
	path := filepath.Join(root, "deployments/k8s/schemas", kubernetesSchemaVersion, pin.file)
	t.Run("missing-file-does-not-download", func(t *testing.T) {
		if _, err := loadKubernetesSchema(filepath.Join(t.TempDir(), pin.file), pin); err == nil {
			t.Fatal("missing schema passed")
		}
	})
	t.Run("changed-even-with-valid-json", func(t *testing.T) {
		changed := filepath.Join(t.TempDir(), pin.file)
		if err := os.WriteFile(changed, []byte(readFile(t, path)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadKubernetesSchema(changed, pin); err == nil {
			t.Fatal("changed schema passed SHA-256 pin")
		}
	})
	for _, uri := range []string{"https://example.invalid/schema.json", "http://127.0.0.1:1/schema.json", "file:///tmp/unreviewed.json"} {
		t.Run(uri, func(t *testing.T) {
			_, err := compileKubernetesSchema(map[string]any{"$ref": uri})
			var loadErr *jsonschema.LoadURLError
			if !errors.As(err, &loadErr) {
				t.Fatalf("external resolution must fail without any resolver: %v", err)
			}
			var unsupported *jsonschema.UnsupportedURLSchemeError
			if !errors.As(loadErr.Err, &unsupported) {
				t.Fatalf("unexpected resolver failure: %v", loadErr.Err)
			}
		})
	}
}

func TestKubernetesSchemaManifestBudgets(t *testing.T) {
	schemas, err := loadKubernetesSchemas(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	const minimal = "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: bounded\n"
	for _, text := range []string{"", "# empty\n", "null\n", "[]\n", minimal + "---\n[broken",
		strings.Repeat("x", maxKubernetesManifestBytes+1), strings.Repeat(minimal+"---\n", maxKubernetesDocuments+1)} {
		if _, err := validateKubernetesManifest(schemas, text); err == nil {
			t.Fatal("empty, malformed or over-budget manifest passed")
		}
	}
}
