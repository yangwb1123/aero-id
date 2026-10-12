package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

type callbackManifestContainer struct {
	Env []struct {
		Name      string `yaml:"name"`
		ValueFrom struct {
			SecretKeyRef struct {
				Name     string `yaml:"name"`
				Key      string `yaml:"key"`
				Optional bool   `yaml:"optional"`
			} `yaml:"secretKeyRef"`
		} `yaml:"valueFrom"`
	} `yaml:"env"`
}

func TestCallbackKeyringBoundForEveryAPIWorkerAndPreflightContainer(t *testing.T) {
	path := filepath.Join(repoRootConfigTest(t), "deployments", "k8s", "regional-cell.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var applications, preflights int
	for {
		var document struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers     []callbackManifestContainer `yaml:"containers"`
						InitContainers []callbackManifestContainer `yaml:"initContainers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if document.Kind != "Deployment" {
			continue
		}
		for _, container := range document.Spec.Template.Spec.Containers {
			applications += assertCallbackManifestKeyring(t, container)
		}
		for _, container := range document.Spec.Template.Spec.InitContainers {
			preflights += assertCallbackManifestKeyring(t, container)
		}
	}
	if applications != 2 || preflights != 2 {
		t.Fatalf("callback keyrings bound to applications=%d preflights=%d, want 2 each", applications, preflights)
	}
}

func assertCallbackManifestKeyring(t *testing.T, container callbackManifestContainer) int {
	t.Helper()
	var active, previous bool
	for _, entry := range container.Env {
		switch entry.Name {
		case "AERO_ID_CALLBACK_MASTER_KEY":
			active = true
		case "AERO_ID_CALLBACK_PREVIOUS_MASTER_KEYS":
			ref := entry.ValueFrom.SecretKeyRef
			if ref.Name != "aero-id-secrets" || ref.Key != "callback_previous_master_keys" || !ref.Optional {
				t.Fatal("previous callback keyring must come from the optional approved Secret reference")
			}
			previous = true
		}
	}
	if active != previous {
		t.Fatal("active/previous key bindings differ between callback process and preflight")
	}
	if active {
		return 1
	}
	return 0
}
