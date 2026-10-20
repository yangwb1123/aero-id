import assert from "node:assert/strict";
import {
  jobAPIResponse,
  exportJobID,
  jobRequestID,
} from "./job-results-data.mjs";
import {
  operationAPIResponse,
  operationID,
  operationRequestID,
} from "./operation-results-data.mjs";
export const auditRequestID = "fixture/request?scope=original&value=%+";
export const auditNavigationCursor = "task-audit-original-cursor";
export function taskAuditAPI(url, scenario) {
  const operation = scenario.startsWith("auditnav-operation");
  if (url.pathname === "/v1/audit/events") {
    assert.equal(url.searchParams.get("limit"), "50");
    assert.equal(url.searchParams.has("job_id"), false);
    assert.equal(url.searchParams.has("tenant_id"), false);
    const request = url.searchParams.get("request_id");
    const operationFilter = url.searchParams.get("operation_id");
    assert(request === auditRequestID || operationFilter === operationID);
    assert(
      !(request && operationFilter),
      "links must not silently intersect ID kinds",
    );
    if (scenario.endsWith("-denied") || scenario.endsWith("-unavailable"))
      return {
        status: scenario.endsWith("-denied") ? 403 : 503,
        error: {
          code: scenario.endsWith("-denied")
            ? "auth.forbidden"
            : "audit.list_failed",
          message: "受控审计查询未获结果",
        },
      };
    if (scenario.endsWith("-empty"))
      return { data: { events: [], next_cursor: "" } };
    const cursor = url.searchParams.get("cursor");
    assert(cursor === null || cursor === auditNavigationCursor);
    return {
      data: {
        events: [
          {
            id: cursor
              ? "01800000-0000-7000-8000-000000000071"
              : "01800000-0000-7000-8000-000000000070",
            action: cursor ? "sync.completed" : "sync.requested",
            source: "aero-id",
            actor_uid: "fixture-subject",
            resource_type: operation ? "operation" : "sync_job",
            resource_id: operation ? operationID : exportJobID,
            ...(operationFilter
              ? { operation_id: operationID }
              : { request_id: auditRequestID }),
            occurred_at: "2026-10-16T10:00:00Z",
          },
        ],
        next_cursor: cursor ? "" : auditNavigationCursor,
      },
    };
  }
  const response = operation
    ? operationAPIResponse(url, "operation-partial")
    : jobAPIResponse(url, "job-export");
  if (
    url.pathname === `/v1/sync/jobs/${exportJobID}` ||
    url.pathname === `/v1/operations/${operationID}`
  ) {
    assert(
      [jobRequestID, operationRequestID].includes(response.data.request_id),
    );
    response.data = { ...response.data, request_id: auditRequestID };
    if (scenario.endsWith("-missing")) delete response.data.request_id;
  }
  if (url.pathname === "/v1/me/capabilities" && scenario.endsWith("-no-read"))
    response.data = {
      ...response.data,
      capabilities: response.data.capabilities.map((capability) =>
        capability.name === "audit.read"
          ? { ...capability, authorization: "denied", available: false }
          : capability,
      ),
    };
  return response;
}
