package k8s_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	maxKubernetesManifestBytes = 2 << 20
	maxKubernetesDocuments     = 32
)

func validateKubernetesManifest(schemas map[string]*jsonschema.Schema, text string) (int, error) {
	if len(text) > maxKubernetesManifestBytes {
		return 0, fmt.Errorf("manifest exceeds the byte budget")
	}
	decoder := yaml.NewDecoder(strings.NewReader(text))
	count := 0
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return count, fmt.Errorf("invalid YAML document %d: %w", count+1, err)
		}
		if document == nil {
			return count, fmt.Errorf("empty YAML document %d", count+1)
		}
		count++
		if count > maxKubernetesDocuments {
			return count, fmt.Errorf("manifest exceeds the document budget")
		}
		if err := validateKubernetesDocument(schemas, document); err != nil {
			return count, fmt.Errorf("invalid resource %d: %w", count, err)
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("manifest contains no resources")
	}
	return count, nil
}

func validateKubernetesDocument(schemas map[string]*jsonschema.Schema, document map[string]any) error {
	version, versionOK := document["apiVersion"].(string)
	kind, kindOK := document["kind"].(string)
	if !versionOK || !kindOK {
		return fmt.Errorf("apiVersion and kind must be strings")
	}
	schema, ok := schemas[version+"/"+kind]
	if !ok {
		return fmt.Errorf("unsupported full apiVersion/kind; add a reviewed pinned schema")
	}
	metadata, ok := document["metadata"].(map[string]any)
	if !ok {
		return fmt.Errorf("metadata must be a mapping")
	}
	name, ok := metadata["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("metadata.name must be a non-empty string")
	}
	data, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("resource is not JSON-compatible: %w", err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode resource JSON: %w", err)
	}
	return schema.Validate(value)
}
