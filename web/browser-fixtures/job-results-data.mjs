// Synthetic task metadata and an original fixture artifact, never live user/source data.
import {
  datasets,
  capabilityFixture,
  resourceFixture,
} from "./resources-data.mjs";
export const syncJobID = "01800000-0000-7000-8000-000000000053";
export const exportJobID = "01800000-0000-7000-8000-000000000055";
export const auditJobID = "01800000-0000-7000-8000-000000000056";
export const jobRequestID = "fixture-job-request-55";
// One original TTL for this owned fixture process; repeated GET never renews it.
const artifactExpiresAt = new Date(Date.now() + 300000).toISOString();
export const jobArtifact = JSON.stringify({
  schema_version: 1,
  type: "export",
  job_id: exportJobID,
  selected_datasets: ["aero-vault.usage", "aero-im.workspaces"],
  missing_datasets: ["aero-im.workspaces"],
  fixture: "original synthetic projection artifact; no source object bytes",
});
function result(kind) {
  return {
    schema_version: 1,
    type: kind,
    job_id: kind === "export" ? exportJobID : auditJobID,
    account_id: "01800000-0000-7000-8000-000000000002",
    tenant_id: "tenant-demo",
    generated_at: "2026-10-16T09:02:00Z",
    expires_at: artifactExpiresAt,
    download_url: "/untrusted-artifact-link-must-not-be-followed",
    manifest_digest: "a".repeat(64),
  };
}
export function jobResultFixture(scenario, id) {
  const kind =
    id === syncJobID ? "sync" : id === auditJobID ? "audit_export" : "export";
  const job = {
    job_id: id,
    account_id: "01800000-0000-7000-8000-000000000002",
    actor_uid: "fixture-original-initiator",
    tenant_id: "tenant-demo",
    request_id: jobRequestID,
    job_type: kind,
    status: kind === "sync" ? "partially_completed" : "completed",
    request: { datasets: ["aero-vault.usage", "aero-im.workspaces"] },
    result:
      kind === "sync"
        ? {
            mode: "sync",
            sources: {
              "aero-vault": { status: "synced", count: 1, complete: true },
              "aero-im": {
                status: "unavailable",
                error_code: "source.http_503",
              },
            },
          }
        : kind === "audit_export"
          ? {
              ...result(kind),
              event_count: 0,
              truncated: true,
              continuation_job_id: auditJobID,
            }
          : {
              ...result(kind),
              selected_datasets: ["aero-vault.usage", "aero-im.workspaces"],
              missing_datasets: ["aero-im.workspaces"],
              complete: false,
              coverage_complete: false,
              traversal_complete: true,
              point_in_time: false,
              dataset_count: 1,
              source_count: 3,
              membership_count: 0,
            },
    attempt: 1,
    created_at: "2026-10-16T09:00:00Z",
    updated_at: "2026-10-16T09:02:00Z",
    completed_at: "2026-10-16T09:02:00Z",
  };
  if (scenario === "job-missing-source") delete job.result.sources["aero-im"];
  if (scenario === "job-expired")
    job.result.expires_at = "2026-01-01T00:00:00Z";
  if (scenario === "job-disposed")
    job.result.artifact_deleted_at = "2026-10-16T09:03:00Z";
  if (scenario === "job-unknown") {
    job.status = "unknown";
    job.completed_at = null;
  }
  if (scenario === "job-failed") {
    job.status = "failed";
    job.error_code = "export.read_failed";
    job.error_message = "failed to read export data";
  }
  if (scenario === "job-malformed") {
    job.result.source_count = "3";
    delete job.result.membership_count;
    job.result.dataset_count = Number.MAX_SAFE_INTEGER + 1;
  }
  if (scenario === "job-mismatch") job.result.job_id = syncJobID;
  job.request.token = "UNAPPROVED-FIELD-MUST-NOT-RENDER";
  job.result.message_body = "UNAPPROVED-FIELD-MUST-NOT-RENDER";
  return job;
}
function capabilities(scenario) {
  const report = capabilityFixture("healthy");
  report.capabilities = [
    "account.self.read",
    "account.self.sync",
    "account.self.export",
    "account.export",
    "source.sync",
    "audit.export",
    "audit.read",
  ].map((name) => ({
    name,
    registered: true,
    enabled: true,
    authorization:
      scenario === "job-audit-denied" && name === "audit.read"
        ? "denied"
        : "allowed",
    authorization_scope: name.startsWith("account.self.") ? "self" : "global",
    available: !(scenario === "job-audit-denied" && name === "audit.read"),
  }));
  return report;
}
export function jobAPIResponse(url, scenario) {
  if (url.pathname === "/v1/account-datasets") return { data: { datasets } };
  if (url.pathname === "/v1/me/capabilities")
    return { data: capabilities(scenario) };
  if (url.pathname === "/v1/me/resources")
    return { data: resourceFixture("healthy") };
  if (url.pathname === "/v1/audit/events")
    return { data: { events: [], next_cursor: "" } };
  if (url.pathname === "/v1/sync/jobs")
    return {
      data: { jobs: [jobResultFixture(scenario, syncJobID)], next_cursor: "" },
    };
  const match = /^\/v1\/sync\/jobs\/([\da-f-]+)(\/export\/download)?$/.exec(
    url.pathname,
  );
  if (!match || ![syncJobID, exportJobID, auditJobID].includes(match[1]))
    throw new Error("unexpected job fixture API target");
  if (match[2]) {
    if (
      url.searchParams.get("one_time") !== "true" ||
      match[1] !== exportJobID ||
      scenario !== "job-export"
    )
      throw new Error("unexpected download in task fixture");
    return { artifact: jobArtifact };
  }
  if (scenario === "job-read-failure")
    return {
      status: 503,
      error: { code: "sync.read_unavailable", message: "受控任务读取故障" },
    };
  return { data: jobResultFixture(scenario, match[1]) };
}
