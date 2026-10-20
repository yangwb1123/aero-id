package k8s_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const kubernetesSchemaVersion = "v1.34.1"

type kubernetesSchemaPin struct {
	gvk, file, digest string
}

// Full GVKs are checked independently: OpenAPI's kind/apiVersion strings and
// x-kubernetes-group-version-kind annotation do not enforce these identities.
var kubernetesSchemaPins = []kubernetesSchemaPin{
	{"v1/Namespace", "namespace-v1.json", "324fae677b98d1a6d54340db0c334d053e8ffbafceb3f73326e41de2610d5843"},
	{"v1/ConfigMap", "configmap-v1.json", "e0eaddebd677c08aa092b2da2264d86ac4fc34eed112b9fac2945b3f00c1e9b1"},
	{"v1/Service", "service-v1.json", "8bf019854daed511e7c174896a898173fa65d88ec5937c687a37303d4cc9351b"},
	{"apps/v1/Deployment", "deployment-apps-v1.json", "92b7a333a49124f5300095d3e7786ca2038b7d6e602c309f73e90631377e4b0a"},
	{"apps/v1/StatefulSet", "statefulset-apps-v1.json", "e095a9d88776fbb2e9c9312baac322c992a9f5188f81ae690bb7dc00fc3915fa"},
	{"autoscaling/v2/HorizontalPodAutoscaler", "horizontalpodautoscaler-autoscaling-v2.json",
		"613e60f54bc1d70776eb3e75fbdbc2f1250b5c1a92c88f54846c5fabd02f6f3e"},
	{"policy/v1/PodDisruptionBudget", "poddisruptionbudget-policy-v1.json",
		"da73f50ad0264d73f668eecaa9959da65afe2846602a7a5c8fd51f7799d6a258"},
	{"networking.k8s.io/v1/Ingress", "ingress-networking-v1.json", "4e0f63ad84c2bf22565e489d1f4b885ddaa9f6bf7cff1ddd562553760afe4d79"},
	{"batch/v1/Job", "job-batch-v1.json", "967234ab9eb5477fd8e5538c83c85817a560aecc1aaf886fafbe8a4ac0972023"},
}

func loadKubernetesSchemas(root string) (map[string]*jsonschema.Schema, error) {
	schemas := make(map[string]*jsonschema.Schema, len(kubernetesSchemaPins))
	for _, pin := range kubernetesSchemaPins {
		path := filepath.Join(root, "deployments/k8s/schemas", kubernetesSchemaVersion, pin.file)
		schema, err := loadKubernetesSchema(path, pin)
		if err != nil {
			return nil, fmt.Errorf("load pinned %s schema: %w", pin.gvk, err)
		}
		schemas[pin.gvk] = schema
	}
	return schemas, nil
}

func loadKubernetesSchema(path string, pin kubernetesSchemaPin) (*jsonschema.Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	if len(data) > 1<<20 || fmt.Sprintf("%x", sha256.Sum256(data)) != pin.digest {
		return nil, fmt.Errorf("schema size or SHA-256 differs from the reviewed pin")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	return compileKubernetesSchema(doc)
}

func compileKubernetesSchema(doc any) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	// No HTTP, file or arbitrary reference resolver. Built-in metaschemas are
	// embedded in the pinned library; the reviewed resources are standalone.
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	const location = "urn:aero-id:kubernetes-schema"
	if err := compiler.AddResource(location, doc); err != nil {
		return nil, fmt.Errorf("add schema: %w", err)
	}
	return compiler.Compile(location)
}
