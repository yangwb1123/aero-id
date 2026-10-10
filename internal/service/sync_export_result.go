package service

import "github.com/aero/aero-id/internal/domain"

// Account export contents belong only in the encrypted TTL artifact. Persist
// and expose bounded manifest metadata, not another profile/projection copy.
// Apply this allow-list to historical job reads as well as new finish writes.
func safeAccountExportResult(input map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{"schema_version", "type", "job_id", "tenant_id", "account_id", "selected_datasets",
		"missing_datasets", "complete", "coverage_complete", "traversal_complete", "point_in_time", "source_count", "membership_count",
		"dataset_count", "manifest_digest", "generated_at", "expires_at", "download_url", "_storage_key", "artifact_deleted_at"} {
		if value, ok := input[key]; ok {
			result[key] = value
		}
	}
	return result
}

func sanitizeAccountExportJob(job *domain.SyncJob) *domain.SyncJob {
	if job == nil || (job.JobType != domain.SyncJobExport && job.JobType != domain.SyncJobAuditExport) {
		return job
	}
	copy := *job
	copy.Result = artifactResultMetadata(job.JobType, job.Result)
	return &copy
}
