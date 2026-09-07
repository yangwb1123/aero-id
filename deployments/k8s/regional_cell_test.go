package k8s_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	appconfig "github.com/aero/aero-id/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	regionalCellTemplate = "deployments/k8s/regional-cell.yaml"
	shippedConfig        = "configs/config.yaml"
)

func TestShippedVaultConfigurationIsDisabledAndReadOnly(t *testing.T) {
	root := repositoryRoot(t)
	config := yamlDocument(t, filepath.Join(root, shippedConfig))
	assertDisabledVaultConfig(t, nestedMap(t, config, "sources", "aero_vault"))
}

func TestRegionalCellBaseAndPodWiringAreSafe(t *testing.T) {
	root := repositoryRoot(t)
	data := readFile(t, filepath.Join(root, regionalCellTemplate))
	if strings.Contains(data, "image: aero-id:latest") || strings.Contains(data, "image: aero-id@") {
		t.Fatal("regional-cell template contains a mutable application image")
	}
	if !strings.Contains(data, "ghcr.io/aero/aero-id@sha256:REPLACE_WITH_RELEASE_DIGEST") {
		t.Fatal("regional-cell template is missing the application digest placeholder")
	}
	documents := yamlDocuments(t, data)
	config := findDocument(t, documents, "ConfigMap", "aero-id-config")
	configText := nestedString(t, config, "data", "config.yaml")
	baseConfig := nestedMapFromText(t, configText, "ConfigMap.data.config.yaml")
	assertDisabledVaultConfig(t, nestedMap(t, baseConfig, "sources", "aero_vault"))

	allDeployments := documentsByKind(documents, "Deployment")
	deployments := make([]map[string]any, 0, 2)
	for _, deployment := range allDeployments {
		name := nestedString(t, deployment, "metadata", "name")
		if name == "aero-id-api" || name == "aero-id-workers" {
			deployments = append(deployments, deployment)
		}
	}
	if len(deployments) != 2 {
		t.Fatalf("regional-cell application deployments = %d, want API and worker deployments", len(deployments))
	}
	for _, deployment := range deployments {
		assertDeploymentTemplate(t, deployment)
	}
	assertOptionalSourceSecrets(t, documents)
}

func TestRenderedRegionalCellBaseConfigLoadsWithRequiredSecrets(t *testing.T) {
	root := repositoryRoot(t)
	output := filepath.Join(t.TempDir(), "regional-cell.yaml")
	renderRegionalCell(t, root, output)
	documents := yamlDocuments(t, readFile(t, output))
	configMap := findDocument(t, documents, "ConfigMap", "aero-id-config")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(nestedString(t, configMap, "data", "config.yaml")), 0600); err != nil {
		t.Fatalf("write rendered config: %v", err)
	}
	pinRegionalCellConfigEnvironment(t)
	if _, err := appconfig.Load(configPath); err != nil {
		t.Fatalf("rendered disabled-source base config must load: %v", err)
	}
}

func pinRegionalCellConfigEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("AERO_ID_DATABASE_PASSWORD", "database-password")
	t.Setenv("AERO_ID_SIGNING_KEY", strings.Repeat("s", 32))
	t.Setenv("AERO_ID_AUDIT_LEDGER_SIGNING_KEY", strings.Repeat("a", 32))
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "false")
	t.Setenv("AERO_ID_BILLING_ENABLED", "false")
	t.Setenv("AERO_ID_ACCOUNT_REGION", "us-east-1")
	for _, source := range []string{"SNAPLINK", "AERO_IM", "AERO_VAULT"} {
		for _, field := range []string{"ENDPOINT", "BEARER_TOKEN", "OAUTH_TOKEN_URL", "OAUTH_CLIENT_ID",
			"OAUTH_CLIENT_SECRET", "OAUTH_SCOPE", "OAUTH_RESOURCE", "OAUTH_ALLOWED_HOSTS",
			"OAUTH_TLS_CA_FILE", "OAUTH_TLS_CERT_FILE", "OAUTH_TLS_KEY_FILE", "OAUTH_TLS_SERVER_NAME",
			"OAUTH_TENANT_ID", "ALLOW_INSECURE_LOOPBACK"} {
			t.Setenv("AERO_ID_SOURCES_"+source+"_"+field, "")
		}
	}
	t.Setenv("AERO_ID_SOURCES_AERO_VAULT_CAPABILITIES_ERASE", "false")
	t.Setenv("AERO_ID_SOURCES_AERO_VAULT_CAPABILITIES_OPERATION_STATUS", "false")
	t.Setenv("AERO_ID_REPLICATION_MAX_TERMINAL_ATTEMPTS", "0")
}

func renderRegionalCell(t *testing.T, root, output string) {
	t.Helper()
	command := exec.Command("bash", "scripts/render-regional-cell.sh", regionalCellTemplate, output)
	command.Dir = root
	command.Env = append(os.Environ(), "REGION=us-east-1", "AERO_ID_IMAGE_DIGEST=sha256:"+strings.Repeat("a", 64), "AERO_ID_PAUSE_WORKLOADS=0")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("render regional-cell: %v: %s", err, stderr.String())
	}
}

func TestMigrationJobRendererProducesBoundedReleaseJob(t *testing.T) {
	root := repositoryRoot(t)
	output := filepath.Join(t.TempDir(), "migration-job.yaml")
	command := exec.Command("bash", "scripts/render-migration-job.sh", "deployments/k8s/migration-job.yaml", output)
	command.Dir = root
	command.Env = append(os.Environ(), "RELEASE=2026-08-31", "AERO_ID_IMAGE_DIGEST=sha256:"+strings.Repeat("a", 64), "MIGRATION_ACTION=down", "MIGRATION_COUNT=2")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("render migration Job: %v: %s", err, stderr.String())
	}
	documents := yamlDocuments(t, readFile(t, output))
	job := findDocument(t, documents, "Job", "aero-id-migrate-2026-08-31-down-2")
	pod := nestedMap(t, job, "spec", "template", "spec")
	container := findNamedContainer(t, nestedList(t, pod, "containers"), "migrate")
	if nestedString(t, container, "image") != "ghcr.io/aero/aero-id@sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("migration image is not pinned: %#v", container)
	}
	args := nestedList(t, container, "args")
	if !listContains(args, "down") || !listContains(args, "2") {
		t.Fatalf("migration down args = %#v, want down 2", args)
	}
}

func TestRegionalCellRendererProducesReleaseWiring(t *testing.T) {
	root := repositoryRoot(t)
	output := filepath.Join(t.TempDir(), "regional-cell.yaml")
	command := exec.Command("bash", "scripts/render-regional-cell.sh", regionalCellTemplate, output)
	command.Dir = root
	command.Env = append(os.Environ(), "REGION=us-east-1", "AERO_ID_IMAGE_DIGEST=sha256:"+strings.Repeat("a", 64), "AERO_ID_PAUSE_WORKLOADS=0")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("render regional-cell: %v: %s", err, stderr.String())
	}
	rendered := readFile(t, output)
	if strings.Contains(rendered, "REPLACE_WITH_") || strings.Contains(rendered, "REGION_PLACEHOLDER") {
		t.Fatal("rendered regional-cell retains a release placeholder")
	}
	if strings.Count(rendered, "ghcr.io/aero/aero-id@sha256:"+strings.Repeat("a", 64)) != 4 {
		t.Fatalf("rendered application/preflight images are not all pinned: count=%d", strings.Count(rendered, "ghcr.io/aero/aero-id@sha256:"+strings.Repeat("a", 64)))
	}
	if !regexp.MustCompile(`checksum/config: "[0-9a-f]{64}"`).MatchString(rendered) {
		t.Fatal("rendered workload is missing a SHA-256 config checksum")
	}
	_ = yamlDocuments(t, rendered)
}

func assertDisabledVaultConfig(t *testing.T, source map[string]any) {
	t.Helper()
	if nestedString(t, source, "endpoint") != "" || nestedString(t, source, "summary_path") != "/v1/internal/account-summary" ||
		nestedString(t, source, "erase_path") != "" || nestedString(t, source, "status_path") != "" {
		t.Fatalf("Vault base configuration is not read-only/disabled: %#v", source)
	}
	capabilities := nestedMap(t, source, "capabilities")
	if nestedBool(t, capabilities, "erase") || nestedBool(t, capabilities, "operation_status") {
		t.Fatalf("Vault base capabilities are enabled: %#v", capabilities)
	}
}

func assertDeploymentTemplate(t *testing.T, deployment map[string]any) {
	t.Helper()
	template := nestedMap(t, deployment, "spec", "template")
	annotations := nestedMap(t, template, "metadata", "annotations")
	if nestedString(t, annotations, "checksum/config") != "REPLACE_WITH_CONFIG_SHA256" {
		t.Fatalf("%s config checksum annotation is not a placeholder", nestedString(t, deployment, "metadata", "name"))
	}
	initContainers := nestedList(t, template, "spec", "initContainers")
	preflight := findNamedContainer(t, initContainers, "preflight")
	if nestedString(t, preflight, "image") != "ghcr.io/aero/aero-id@sha256:REPLACE_WITH_RELEASE_DIGEST" ||
		!listContains(nestedList(t, preflight, "command"), "/app/preflight") {
		t.Fatalf("%s preflight is not pinned to the release image/command", nestedString(t, deployment, "metadata", "name"))
	}
	if !envUsesConfigChecksum(t, nestedList(t, preflight, "env")) {
		t.Fatalf("%s preflight is not bound to checksum/config", nestedString(t, deployment, "metadata", "name"))
	}
	containers := nestedList(t, template, "spec", "containers")
	application := findNamedContainer(t, containers, "aero-id")
	if nestedString(t, application, "image") != "ghcr.io/aero/aero-id@sha256:REPLACE_WITH_RELEASE_DIGEST" ||
		nestedString(t, application, "imagePullPolicy") != "Always" {
		t.Fatalf("%s application image is not immutable: %#v", nestedString(t, deployment, "metadata", "name"), application)
	}
	if !envUsesConfigChecksum(t, nestedList(t, application, "env")) {
		t.Fatalf("%s application is not bound to checksum/config", nestedString(t, deployment, "metadata", "name"))
	}
}

func assertOptionalSourceSecrets(t *testing.T, documents []map[string]any) {
	t.Helper()
	wanted := map[string]bool{
		"snaplink_bearer_token": false, "snaplink_oauth_client_secret": false,
		"aero_im_bearer_token": false, "aero_im_oauth_client_secret": false,
		"aero_im_target_assertion_private_key": false, "aero_vault_bearer_token": false,
		"aero_vault_oauth_client_secret": false,
	}
	for _, deployment := range documentsByKind(documents, "Deployment") {
		name := nestedString(t, deployment, "metadata", "name")
		if name != "aero-id-api" && name != "aero-id-workers" {
			continue
		}
		template := nestedMap(t, deployment, "spec", "template")
		for _, group := range []string{"initContainers", "containers"} {
			for _, raw := range nestedList(t, template, "spec", group) {
				container := asMap(t, raw, group)
				for _, item := range nestedList(t, container, "env") {
					entry := asMap(t, item, "environment entry")
					name := nestedString(t, entry, "name")
					key := secretKey(t, entry)
					if !wantedHas(wanted, key) {
						continue
					}
					wanted[key] = true
					secret := nestedMap(t, entry, "valueFrom", "secretKeyRef")
					if !nestedBool(t, secret, "optional") {
						t.Fatalf("source secret %s (%s) is not optional", key, name)
					}
				}
			}
		}
	}
	for key, found := range wanted {
		if !found {
			t.Fatalf("source secret %s is not wired into the regional-cell workloads", key)
		}
	}
}

func secretKey(t *testing.T, entry map[string]any) string {
	t.Helper()
	ref, ok := lookupMap(entry, "valueFrom", "secretKeyRef")
	if !ok {
		return ""
	}
	value, _ := ref["key"].(string)
	return value
}

func wantedHas(wanted map[string]bool, key string) bool {
	_, ok := wanted[key]
	return ok
}

func envUsesConfigChecksum(t *testing.T, values []any) bool {
	t.Helper()
	for _, raw := range values {
		entry := asMap(t, raw, "environment entry")
		if nestedString(t, entry, "name") != "AERO_ID_CONFIG_SHA256" {
			continue
		}
		fieldRef := nestedMap(t, entry, "valueFrom", "fieldRef")
		return nestedString(t, fieldRef, "fieldPath") == "metadata.annotations['checksum/config']"
	}
	return false
}

func findNamedContainer(t *testing.T, values []any, name string) map[string]any {
	t.Helper()
	for _, raw := range values {
		container := asMap(t, raw, "container")
		if nestedString(t, container, "name") == name {
			return container
		}
	}
	t.Fatalf("container %s is missing", name)
	return nil
}

func listContains(values []any, wanted string) bool {
	for _, value := range values {
		if text, ok := value.(string); ok && text == wanted {
			return true
		}
	}
	return false
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve regional-cell test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return string(data)
}

func yamlDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	documents := yamlDocuments(t, readFile(t, path))
	if len(documents) != 1 {
		t.Fatalf("%s contains %d YAML documents, want one", filepath.Base(path), len(documents))
	}
	return documents[0]
}

func yamlDocuments(t *testing.T, text string) []map[string]any {
	t.Helper()
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var documents []map[string]any
	for {
		var value map[string]any
		err := decoder.Decode(&value)
		if errors.Is(err, io.EOF) {
			return documents
		}
		if err != nil {
			t.Fatalf("decode YAML: %v", err)
		}
		if value != nil {
			documents = append(documents, value)
		}
	}
}

func nestedMapFromText(t *testing.T, text, label string) map[string]any {
	t.Helper()
	documents := yamlDocuments(t, text)
	if len(documents) != 1 {
		t.Fatalf("%s contains %d YAML documents, want one", label, len(documents))
	}
	return documents[0]
}

func findDocument(t *testing.T, documents []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, document := range documents {
		if nestedString(t, document, "kind") == kind && nestedString(t, document, "metadata", "name") == name {
			return document
		}
	}
	t.Fatalf("YAML document %s/%s is missing", kind, name)
	return nil
}

func documentsByKind(documents []map[string]any, kind string) []map[string]any {
	result := make([]map[string]any, 0)
	for _, document := range documents {
		if value, ok := document["kind"].(string); ok && value == kind {
			result = append(result, document)
		}
	}
	return result
}

func nestedString(t *testing.T, value map[string]any, path ...string) string {
	t.Helper()
	result, ok := lookupMap(value, path...)
	if !ok {
		t.Fatalf("YAML field %s is missing", strings.Join(path, "."))
	}
	text, ok := result["__value__"].(string)
	if ok {
		return text
	}
	return ""
}

func nestedBool(t *testing.T, value map[string]any, path ...string) bool {
	t.Helper()
	result, ok := lookupMap(value, path...)
	if !ok {
		t.Fatalf("YAML field %s is missing", strings.Join(path, "."))
	}
	boolean, ok := result["__bool__"].(bool)
	if ok {
		return boolean
	}
	return false
}

func lookupMap(value map[string]any, path ...string) (map[string]any, bool) {
	current := value
	for index, key := range path {
		item, ok := current[key]
		if !ok {
			return nil, false
		}
		if index == len(path)-1 {
			switch typed := item.(type) {
			case string:
				return map[string]any{"__value__": typed}, true
			case bool:
				return map[string]any{"__bool__": typed}, true
			case map[string]any:
				return typed, true
			default:
				return map[string]any{"__raw__": typed}, true
			}
		}
		next, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func nestedList(t *testing.T, value map[string]any, path ...string) []any {
	t.Helper()
	current := value
	for _, key := range path[:len(path)-1] {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("YAML map %s is missing", strings.Join(path, "."))
		}
		current = next
	}
	list, ok := current[path[len(path)-1]].([]any)
	if !ok {
		t.Fatalf("YAML list %s is missing", strings.Join(path, "."))
	}
	return list
}

func nestedMap(t *testing.T, value map[string]any, path ...string) map[string]any {
	t.Helper()
	current := value
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("YAML map %s is missing", strings.Join(path, "."))
		}
		current = next
	}
	return current
}

func asMap(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not a YAML map", label)
	}
	return result
}
