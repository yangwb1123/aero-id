// Synthetic legacy/missing-scope metadata; no per-step request_payload/timestamps.
import {
  capabilityFixture,
  datasets,
  resourceFixture,
} from "./resources-data.mjs";
export const operationID = "01800000-0000-7000-8000-000000000006";
export const operationRequestID = "operation-fixture-request";
export const auditCursor = "operation-audit-original-cursor";
const stepID = "01800000-0000-7000-8000-000000000010";
function receipt() {
  return {
    operation_id: operationID,
    account_id: "01800000-0000-7000-8000-000000000002",
    tenant_id: "tenant-demo",
    actor_uid: "fixture-initiator",
    request_id: operationRequestID,
    home_region: "local",
    operation_type: "sync",
    status: "partially_completed",
    idempotency_key: "fixture-original-operation-key",
    created_at: "2026-10-16T09:00:00Z",
    updated_at: "2026-10-16T09:02:00Z",
    request_payload: {},
    result: {},
    steps: [
      {
        id: stepID,
        step_key: "vault-summary",
        source: "aero-vault",
        action: "summary",
        dataset_scope: { status: "missing", datasets: [] },
        status: "succeeded",
        attempt: 1,
        result: {
          status: "partially_completed",
          count: 1,
          complete: false,
          application_outcome: "applied",
          skipped_datasets: ["aero-vault.buckets"],
          message_body: "UNAPPROVED-FIELD-MUST-NOT-RENDER",
        },
      },
      {
        id: "01800000-0000-7000-8000-000000000011",
        step_key: "im-summary",
        source: "aero-im",
        action: "summary",
        dataset_scope: { status: "missing", datasets: [] },
        status: "failed",
        attempt: 1,
        source_operation_id: "im-original-operation",
        result: null,
        error_code: "operation.step_failed",
      },
      {
        id: "01800000-0000-7000-8000-000000000012",
        step_key: "later-summary",
        source: "aero-im",
        action: "summary",
        dataset_scope: { status: "missing", datasets: [] },
        status: "skipped",
        attempt: 0,
        result: null,
      },
    ],
  };
}
export function operationFixture(scenario) {
  const value = receipt();
  if (scenario === "operation-requested") value.status = "requested";
  if (scenario === "operation-completed-missing") {
    value.status = "completed";
    value.completed_at = "2026-10-16T09:02:00Z";
    value.steps = [
      { ...value.steps[0], result: null, compensation_action: "summary" },
    ];
  }
  if (scenario === "operation-unknown-forward") {
    value.status = "unknown";
    value.steps = [
      {
        ...value.steps[1],
        status: "unknown",
        source_operation_id: undefined,
        error_code: "operation.unknown_outcome",
      },
    ];
  }
  if (scenario.startsWith("operation-compensation-")) {
    value.status =
      scenario === "operation-compensation-success"
        ? "failed"
        : scenario === "operation-compensation-pending"
          ? "compensating"
          : "unknown";
    value.operation_type = "source.command";
    value.steps = [
      {
        ...value.steps[0],
        source_operation_id: "forward-original",
        compensation_action: "erase",
        compensation_status:
          scenario === "operation-compensation-success"
            ? "succeeded"
            : scenario === "operation-compensation-pending"
              ? "pending"
              : "unknown",
        compensation_attempt:
          scenario === "operation-compensation-pending" ? 0 : 2,
        compensation_source_operation_id:
          scenario === "operation-compensation-pending"
            ? undefined
            : "compensation-original",
        compensation_result:
          scenario === "operation-compensation-success"
            ? { status: "completed" }
            : null,
        compensation_error_code:
          scenario === "operation-compensation-unknown"
            ? "operation.compensation_unknown_outcome"
            : undefined,
      },
      { ...value.steps[1] },
    ];
  }
  if (scenario === "operation-invalid-steps") value.steps = [null];
  if (scenario === "operation-malformed") {
    value.steps[0].result = {
      status: ["completed"],
      count: "1",
      complete: "true",
      application_outcome: "unverified",
      skipped_datasets: ["private.scope"],
    };
    value.steps[1].status = ["succeeded"];
    value.steps[1].attempt = undefined;
  }
  return value;
}
function fact(action, payload, index = 1) {
  return {
    event_id: `01800000-0000-7000-8000-${String(20 + index).padStart(12, "0")}`,
    operation_id: operationID,
    source: "aero-id",
    source_region: "local",
    action,
    resource_type: "operation",
    resource_id: operationID,
    occurred_at: `2026-10-16T09:0${index}:00Z`,
    payload,
  };
}
export function operationAPIResponse(url, scenario) {
  if (url.pathname === "/v1/me/capabilities") {
    const report = capabilityFixture("healthy");
    report.capabilities = [
      "operation.read",
      "operation.manage",
      "source.sync",
      "account.erase",
      "audit.read",
    ].map((name) => ({
      name,
      registered: true,
      enabled: true,
      authorization: "allowed",
      authorization_scope: "global",
      available: true,
    }));
    return { data: report };
  }
  if (url.pathname === "/v1/account-datasets") return { data: { datasets } };
  if (url.pathname === "/v1/me/resources") {
    if (url.searchParams.get("consistency") !== "eventual")
      throw new Error("unexpected non-local resource read");
    return { data: resourceFixture("healthy") };
  }
  if (url.pathname === "/v1/me")
    return {
      data: {
        account: {
          account_id: receipt().account_id,
          tenant_id: "tenant-demo",
          home_region: "local",
        },
        sources: [],
        memberships: [],
      },
    };
  if (url.pathname === "/v1/operations")
    return {
      data: { operations: [operationFixture(scenario)], next_cursor: "" },
    };
  if (url.pathname === `/v1/operations/${operationID}`) {
    if (scenario === "operation-read-failure")
      return {
        status: 503,
        error: { code: "operation.unavailable", message: "受控详情读取故障" },
      };
    return { data: operationFixture(scenario) };
  }
  if (url.pathname === `/v1/audit/operations/${operationID}`) {
    if (scenario === "operation-audit-denied")
      return {
        status: 403,
        error: { code: "audit.forbidden", message: "受控审计读取被拒绝" },
      };
    if (url.searchParams.get("cursor") === auditCursor) {
      if (scenario === "operation-audit-later-failure")
        return {
          status: 503,
          error: { code: "audit.unavailable", message: "受控后续审计读取故障" },
        };
      return {
        data: {
          operation_id: operationID,
          events: [
            fact(
              "operation.requested",
              {
                type: "sync",
                steps: [
                  {
                    step_key: "vault-summary",
                    source: "aero-vault",
                    action: "summary",
                    datasets: ["aero-vault.usage", "aero-vault.buckets"],
                  },
                ],
              },
              0,
            ),
          ],
          next_cursor: "",
        },
      };
    }
    if (scenario === "operation-audit-empty")
      return {
        data: { operation_id: operationID, events: [], next_cursor: "" },
      };
    return {
      data: {
        operation_id: operationID,
        events: [
          fact(
            "operation.compensation.reconciled",
            {
              step_id: stepID,
              step_key: "vault-summary",
              status: "unknown",
              compensation_attempt: 2,
            },
            3,
          ),
          fact(
            "operation.step.finished",
            {
              step_id: stepID,
              step_key: "vault-summary",
              source: "aero-vault",
              action: "summary",
              status: "succeeded",
              attempt: 1,
              source_operation_id: "fact-original-source-id",
              datasets: ["aero-vault.usage"],
              message_body: "UNAPPROVED-FIELD-MUST-NOT-RENDER",
            },
            2,
          ),
        ],
        next_cursor: auditCursor,
      },
    };
  }
  if (url.pathname === "/v1/audit/events")
    return { data: { events: [], next_cursor: "" } };
  throw new Error("unexpected Operation fixture path");
}
