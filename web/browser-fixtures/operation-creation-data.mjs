// Synthetic CommandIntent HTTP seam only: no OIDC, source, storage or real grants.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { datasets } from "./resources-data.mjs";
import {
  operationAPIResponse,
  operationID,
} from "./operation-results-data.mjs";
export const creationIntentID = "01800000-0000-7000-8000-000000000081";
const accountID = "01800000-0000-7000-8000-000000000002";
const digest = (value) =>
  `v1:${createHash("sha256").update(value).digest("hex")}`;
const isDestructive = (action) =>
  ["erase", "delete", "source.erase"].includes(action);
function validatePlan(body) {
  assert.deepEqual(Object.keys(body).sort(), ["account_id", "plan"]);
  assert.equal(body.account_id, accountID);
  const plan = body.plan;
  assert.equal(plan.kind, "operation");
  assert.equal(plan.operation_type, "sync");
  assert(plan.steps.length > 0 && plan.steps.length <= 32);
  assert.equal(
    new Set(plan.steps.map((step) => step.step_key)).size,
    plan.steps.length,
  );
  for (const step of plan.steps) {
    assert.match(step.step_key, /^source-summary-[1-9][0-9]*$/);
    assert.equal(step.action, "summary");
    assert([undefined, "erase"].includes(step.compensation_action));
    const scope = step.request_payload.datasets;
    assert(scope.length > 0 && scope.length <= 11);
    assert.equal(new Set(scope).size, scope.length);
    assert(
      scope.every((name) =>
        datasets.some(
          (item) => item.name === name && item.source === step.source,
        ),
      ),
    );
  }
  assert.deepEqual(
    plan.datasets,
    [
      ...new Set(plan.steps.flatMap((step) => step.request_payload.datasets)),
    ].sort(),
  );
  assert.deepEqual(plan.request_payload, {});
  return structuredClone(plan);
}
function receipt(intent) {
  return {
    operation_id: operationID,
    account_id: intent.account_id,
    actor_uid: "fixture-initiator",
    tenant_id: intent.tenant_id,
    home_region: "local",
    request_id: "creation-fixture-request",
    operation_type: intent.plan.operation_type,
    status: "requested",
    request_payload: intent.plan.request_payload,
    created_at: intent.created_at,
    updated_at: intent.created_at,
    steps: intent.plan.steps.map((step) => ({
      step_key: step.step_key,
      source: step.source,
      action: step.action,
      dataset_scope: {
        status: "explicit",
        datasets: step.request_payload.datasets,
      },
      compensation_action: step.compensation_action,
      status: "pending",
      attempt: 0,
      compensation_attempt: 0,
    })),
  };
}
export function createOperationCreationAPI() {
  let intent;
  let originalKey;
  let prepareCount = 0;
  let executeCount = 0;
  let activeScenario;
  const facts = [];
  async function response(url, scenario, request) {
    if (url.pathname === "/v1/me/capabilities") {
      assert.equal(request.method(), "GET");
      if (activeScenario !== scenario) {
        activeScenario = scenario;
        intent = undefined;
        originalKey = undefined;
        prepareCount = 0;
        executeCount = 0;
      }
      const report = operationAPIResponse(url, "operation-partial").data;
      if (scenario === "creation-action-denied")
        Object.assign(
          report.capabilities.find((item) => item.name === "source.sync"),
          { authorization: "denied", available: false },
        );
      if (scenario === "creation-source-disabled")
        report.datasets.find(
          (item) => item.name === "aero-im.workspaces",
        ).enabled = false;
      return { data: report };
    }
    if (request.method() === "POST") {
      assert(
        [
          "creation-normal",
          "creation-mobile",
          "creation-compensation",
          "creation-lost-preview",
          "creation-lost-execute",
          "creation-server-denied",
        ].includes(scenario),
        "ineligible scenario cannot send a command",
      );
      if (url.pathname === "/v1/command-intents") {
        assert.equal(
          ++prepareCount,
          1,
          "one explicit preparation only, no changed-plan replacement or retry",
        );
        originalKey = await request.headerValue("Idempotency-Key");
        assert.match(originalKey, /^[0-9a-f-]{36}$/);
        const plan = validatePlan(request.postDataJSON());
        facts.push({
          scenario,
          action: "prepare",
          key_digest: digest(originalKey),
          plan,
        });
        intent = {
          intent_id: creationIntentID,
          schema_version: 1,
          account_id: accountID,
          tenant_id: "tenant-demo",
          client_id: "browser-fixture",
          issuer: "https://fixture.invalid",
          version: 1,
          status: "prepared",
          plan,
          preconditions: {
            account_status: "active",
            home_region: "local",
            writer_epoch: 3,
            resolver_version: "fixture-v1",
            snapshots: [],
          },
          confirmation_digest: `v1:${"a".repeat(64)}`,
          destructive: plan.steps.some(
            (step) =>
              isDestructive(step.action) ||
              isDestructive(step.compensation_action),
          ),
          authorization_version: "unsupported",
          resource_authorization: "unknown",
          created_at: new Date(Date.now() - 1000).toISOString(),
          expires_at: new Date(Date.now() + 300000).toISOString(),
        };
        if (scenario === "creation-server-denied") {
          intent = undefined;
          return {
            status: 403,
            error: {
              code: "operation.forbidden",
              message: "受控服务端拒绝预览；保留原键",
            },
          };
        }
        if (scenario === "creation-lost-preview") return { abort: true };
        return { status: 201, data: structuredClone(intent) };
      }
      assert.equal(
        url.pathname,
        `/v1/command-intents/${creationIntentID}/execute`,
        "no direct creation, Run, Reconcile or source calls",
      );
      assert(intent && originalKey);
      assert.equal(++executeCount, 1, "no automatic execution or retry");
      assert.deepEqual(request.postDataJSON(), {
        version: 1,
        confirmation_digest: intent.confirmation_digest,
      });
      facts.push({
        scenario,
        action: "execute",
        intent_id: intent.intent_id,
        version: 1,
        original_plan_retained: true,
      });
      intent = {
        ...intent,
        status: "executed",
        version: 2,
        receipt_kind: "operation",
        receipt_id: operationID,
      };
      if (scenario === "creation-lost-execute") return { abort: true };
      return { data: structuredClone(intent) };
    }
    assert.equal(request.method(), "GET");
    if (url.pathname === "/v1/command-intents/by-key") {
      assert(originalKey);
      assert.equal(await request.headerValue("Idempotency-Key"), originalKey);
      facts.push({
        scenario,
        action: "get-original-key",
        key_digest: digest(originalKey),
      });
      if (!intent)
        return {
          status: 404,
          error: {
            code: "command.not_found",
            message: "受控原键尚无回执；不证明在途请求不存在",
          },
        };
      return {
        data: {
          key_digest: digest(originalKey),
          intent: structuredClone(intent),
        },
      };
    }
    if (url.pathname === `/v1/command-intents/${creationIntentID}`) {
      assert(intent);
      facts.push({
        scenario,
        action: "get-original-intent",
        intent_id: intent.intent_id,
      });
      return { data: structuredClone(intent) };
    }
    if (url.pathname === "/v1/operations")
      return {
        data: {
          operations: intent?.status === "executed" ? [receipt(intent)] : [],
          next_cursor: "",
        },
      };
    if (url.pathname === `/v1/operations/${operationID}`) {
      assert.equal(intent?.status, "executed");
      return { data: receipt(intent) };
    }
    if (url.pathname === `/v1/audit/operations/${operationID}`)
      return {
        data: { operation_id: operationID, events: [], next_cursor: "" },
      };
    return operationAPIResponse(url, "operation-partial");
  }
  return { response, facts };
}
