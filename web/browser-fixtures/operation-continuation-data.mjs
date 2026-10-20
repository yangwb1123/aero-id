// Isolated synthetic API only. Uses the documented HTTP safe-scope DTO; never
// performs a real source call or supplies a production identity/credential.
import assert from "node:assert/strict";
import {
  operationAPIResponse,
  operationFixture,
  operationID,
} from "./operation-results-data.mjs";
export const originalReconcileKey = "01800000-0000-7000-8000-000000000077";

function receipt(scenario) {
  const value = operationFixture("operation-requested");
  value.steps = [
    {
      ...value.steps[1],
      status: "pending",
      attempt: 0,
      result: null,
      error_code: undefined,
      source_operation_id: undefined,
      dataset_scope: { status: "explicit", datasets: ["aero-im.workspaces"] },
    },
    {
      ...value.steps[0],
      status: "pending",
      attempt: 0,
      result: null,
      dataset_scope: {
        status: "explicit",
        datasets: ["aero-vault.usage", "aero-vault.buckets"],
      },
    },
  ];
  if (scenario === "continuation-unknown") {
    value.status = "unknown";
    value.steps[0].status = "unknown";
    value.steps[0].source_operation_id = "im-original-operation";
  }
  if (
    ["continuation-dangerous", "continuation-compensation-denied"].includes(
      scenario,
    )
  )
    value.steps[0].compensation_action = "erase";
  if (scenario === "continuation-foreign-home")
    value.home_region = "other-region";
  if (scenario === "continuation-invalid-scope") {
    value.steps[0].dataset_scope = { status: "invalid", datasets: [] };
    value.steps[0].request_payload = { datasets: ["aero-im.workspaces"] }; // Must not be a fallback.
    value.request_payload = { datasets: ["aero-im.workspaces"] };
  }
  return value;
}
export function createOperationContinuationAPI() {
  let current;
  let commands = 0;
  return async function response(url, scenario, request) {
    if (url.pathname === "/v1/me/capabilities") {
      assert.equal(request.method(), "GET");
      current = receipt(scenario);
      commands = 0;
      const report = operationAPIResponse(url, "operation-partial").data;
      const denied =
        scenario === "continuation-denied"
          ? "source.sync"
          : scenario === "continuation-compensation-denied"
            ? "account.erase"
            : undefined;
      if (denied)
        Object.assign(
          report.capabilities.find((item) => item.name === denied),
          { authorization: "denied", available: false },
        );
      if (scenario === "continuation-disabled-source")
        report.datasets.find(
          (item) => item.name === "aero-im.workspaces",
        ).enabled = false;
      return { data: report };
    }
    assert(current, "fixture must initialize in its own document");
    if (request.method() === "POST") {
      const action = scenario === "continuation-unknown" ? "reconcile" : "run";
      assert(
        [
          "continuation-requested",
          "continuation-unknown",
          "continuation-dangerous",
          "continuation-server-denied",
        ].includes(scenario),
        "ineligible scenario must not issue any command",
      );
      assert.equal(url.pathname, `/v1/operations/${operationID}/${action}`);
      assert.equal(
        request.postData(),
        null,
        "continuation never overrides the stored plan",
      );
      assert.equal(
        await request.headerValue("Idempotency-Key"),
        action === "run"
          ? `operation.run:${operationID}`
          : originalReconcileKey,
      );
      assert.equal(
        ++commands,
        1,
        "each scenario permits one explicit original command, never an automatic retry",
      );
      if (scenario === "continuation-server-denied")
        return {
          status: 403,
          error: {
            code: "operation.forbidden",
            message: "受控服务端拒绝继续原操作；请核对原回执",
          },
        };
      current.status = "completed";
      current.completed_at = "2026-10-16T09:04:00Z";
      current.steps = current.steps.map((step) => ({
        ...step,
        status: "succeeded",
        attempt: 1,
        result: {
          status: "synced",
          count: 1,
          complete: true,
          application_outcome: "applied",
        },
      }));
      return { data: structuredClone(current) };
    }
    assert.equal(request.method(), "GET");
    if (url.pathname === `/v1/operations/${operationID}`)
      return { data: structuredClone(current) };
    if (url.pathname === "/v1/operations")
      return {
        data: { operations: [structuredClone(current)], next_cursor: "" },
      };
    if (url.pathname === `/v1/audit/operations/${operationID}`)
      return {
        data: {
          operation_id: operationID,
          next_cursor: "",
          events: [
            {
              event_id: "01800000-0000-7000-8000-000000000090",
              operation_id: operationID,
              source: "aero-id",
              source_region: "local",
              action: "operation.requested",
              occurred_at: "2026-10-16T09:00:00Z",
              payload: {
                type: "sync",
                steps: [
                  {
                    step_key: "im-summary",
                    source: "aero-im",
                    action: "summary",
                    datasets: ["aero-im.workspaces"],
                  },
                ],
              },
            },
          ],
        },
      };
    return operationAPIResponse(url, "operation-partial");
  };
}
