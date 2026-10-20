package k8s_test

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Schema-valid replicas omitted/left nonzero would still start a default writer.
// Compare whole resource documents: only removing HPA and setting the exact two
// application deployments to zero is allowed; config/checksums/DB stay unchanged.
func assertPausedKubernetesRender(t *testing.T, dir string) {
	t.Helper()
	steady := yamlDocuments(t, readFile(t, filepath.Join(dir, "steady.yaml")))
	paused := indexKubernetesResources(t, yamlDocuments(t, readFile(t, filepath.Join(dir, "paused.yaml"))))
	for _, resource := range steady {
		if resource["kind"] == "HorizontalPodAutoscaler" {
			continue
		}
		name := nestedString(t, resource, "metadata", "name")
		if resource["kind"] == "Deployment" && (name == "aero-id-api" || name == "aero-id-workers") {
			nestedMap(t, resource, "spec")["replicas"] = 0
		}
		key := kubernetesResourceIdentity(t, resource)
		if !reflect.DeepEqual(resource, paused[key]) {
			t.Fatalf("paused resource %s changed beyond the expected writer quiescence", key)
		}
		delete(paused, key)
	}
	if len(paused) != 0 {
		t.Fatal("paused render retained HPA or added another resource")
	}
}

func indexKubernetesResources(t *testing.T, resources []map[string]any) map[string]map[string]any {
	t.Helper()
	indexed := make(map[string]map[string]any, len(resources))
	for _, resource := range resources {
		key := kubernetesResourceIdentity(t, resource)
		if _, exists := indexed[key]; exists {
			t.Fatalf("duplicate resource %s", key)
		}
		indexed[key] = resource
	}
	return indexed
}

func kubernetesResourceIdentity(t *testing.T, resource map[string]any) string {
	t.Helper()
	metadata := nestedMap(t, resource, "metadata")
	namespace, _ := metadata["namespace"].(string)
	return nestedString(t, resource, "apiVersion") + "/" + nestedString(t, resource, "kind") +
		"/" + namespace + "/" + nestedString(t, resource, "metadata", "name")
}
