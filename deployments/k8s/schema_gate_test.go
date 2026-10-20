package k8s_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type kubernetesRenderCase struct {
	file, option string
	documents    int
}

var kubernetesRenderCases = []kubernetesRenderCase{
	{"steady.yaml", "0", 14},
	{"paused.yaml", "1", 13},
	{"migration-up.yaml", "up", 1},
	{"migration-down.yaml", "down", 1},
	{"migration-version.yaml", "version", 1},
}

// Called by both make test and the static shell gate. The gate supplies its
// actual rendered files, not a second rendering of only the known-good source.
func TestKubernetesRenderedSchemaGate(t *testing.T) {
	root := repositoryRoot(t)
	schemas, err := loadKubernetesSchemas(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("AERO_ID_K8S_RENDERED_DIR")
	if dir == "" {
		dir = renderKubernetesSchemaFixtures(t)
	}
	for _, fixture := range kubernetesRenderCases {
		t.Run(fixture.file, func(t *testing.T) {
			count, err := validateKubernetesManifest(schemas, readFile(t, filepath.Join(dir, fixture.file)))
			if err != nil {
				t.Fatal(err)
			}
			if count != fixture.documents {
				t.Fatalf("validated %d resources, expected %d (no skips/empty/partial bundles)", count, fixture.documents)
			}
		})
	}
	assertPausedKubernetesRender(t, dir)
}

func renderKubernetesSchemaFixtures(t *testing.T) string {
	t.Helper()
	root, dir := repositoryRoot(t), t.TempDir()
	for _, fixture := range kubernetesRenderCases {
		output := filepath.Join(dir, fixture.file)
		command := exec.Command("bash", "scripts/render-migration-job.sh", "deployments/k8s/migration-job.yaml", output)
		if fixture.documents > 1 {
			command = exec.Command("bash", "scripts/render-regional-cell.sh", regionalCellTemplate, output)
		}
		command.Dir = root
		command.Env = append(os.Environ(), "REGION=us-east-1", "RELEASE=schema-check",
			"AERO_ID_IMAGE_DIGEST=sha256:"+strings.Repeat("a", 64), "MIGRATION_COUNT=2")
		if fixture.documents > 1 {
			command.Env = append(command.Env, "AERO_ID_PAUSE_WORKLOADS="+fixture.option)
		} else {
			command.Env = append(command.Env, "MIGRATION_ACTION="+fixture.option)
		}
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("render %s: %v: %s", fixture.file, err, stderr.String())
		}
	}
	return dir
}
