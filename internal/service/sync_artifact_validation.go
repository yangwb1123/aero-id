package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

// Account manifests v1 hash captured content, excluding the transport/TTL
// fields added after capture. Preserve this shape for already-created exports.
func accountExportDigest(manifest map[string]any) string {
	captured := make(map[string]any, len(manifest))
	for key, value := range manifest {
		switch key {
		case "manifest_digest", "download_url", "expires_at", "_storage_key":
			continue
		}
		captured[key] = value
	}
	data, err := json.Marshal(captured)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateAccountArtifactManifest(job *domain.SyncJob, manifest map[string]any) error {
	digest, ok := manifest["manifest_digest"].(string)
	if !ok || digest == "" || digest != accountExportDigest(manifest) || job.Result["manifest_digest"] != digest {
		return apperr.Internal("export.integrity_failed", "export manifest integrity check failed")
	}
	selection, err := parsePersistedSyncDatasets(job.Request, true)
	if err != nil {
		return invalidExportScope()
	}
	expected, _ := json.Marshal(datasetNames(selection))
	actual, err := json.Marshal(manifest["selected_datasets"])
	if err != nil || string(expected) != string(actual) {
		return invalidExportScope()
	}
	return nil
}
