import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import OperationDetails from "./OperationDetails.vue";
import {
  commandButton,
  commandTestReport,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testReceiptID,
  testScope,
  unmountCommandTests,
} from "./commandTestFixtures";
import {
  operationReceiptPlan,
  operationActionAvailable,
} from "./taskActionEligibility";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
beforeEach(() => sessionStorage.clear());
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
});
function operation(
  status = "requested",
  scope: unknown = { status: "explicit", datasets: ["aero-im.workspaces"] },
): JsonRecord {
  return {
    operation_id: testReceiptID,
    account_id: testAccountID,
    tenant_id: "tenant-a",
    actor_uid: "original-initiator",
    home_region: "local",
    operation_type: "sync",
    status,
    steps: [
      {
        step_key: "im-summary",
        source: "aero-im",
        action: "summary",
        status: status === "unknown" ? "unknown" : "pending",
        dataset_scope: scope,
      },
    ],
  };
}
function mount(value: JsonRecord) {
  const requests: Array<{
    method?: string;
    path: string;
    key?: string | null;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input)).pathname;
      requests.push({
        method: init?.method,
        path,
        key: new Headers(init?.headers).get("Idempotency-Key"),
      });
      const data = path.startsWith("/v1/audit/")
        ? { operation_id: testReceiptID, events: [] }
        : init?.method === "POST"
          ? { ...value, status: "completed" }
          : value;
      return new Response(JSON.stringify({ data }));
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "component-fixture-marker",
    fetcher as typeof fetch,
  );
  return {
    ...mountCommandTest(OperationDetails, {
      client,
      operationId: testReceiptID,
    }),
    requests,
  };
}
describe("explicit HTTP Operation step scope unlocks only the original eligible workflow", () => {
  it("builds action guidance from safe per-step metadata without adopting arbitrary request data", () => {
    const value = operation();
    (value.steps as JsonRecord[])[0].request_payload = {
      datasets: ["aero-vault.usage"],
      credential: "SYNTHETIC-NOT-FOR-PLAN",
      source_body: { unapproved: true },
    };
    const plan = operationReceiptPlan(value);
    expect(plan).toEqual({
      kind: "operation",
      operation_type: "sync",
      datasets: ["aero-im.workspaces"],
      steps: [
        {
          step_key: "im-summary",
          source: "aero-im",
          action: "summary",
          request_payload: { datasets: ["aero-im.workspaces"] },
        },
      ],
    });
    expect(
      operationActionAvailable(
        value,
        testReceiptID,
        commandTestReport(),
        testScope,
      ),
    ).toBe(true);
    const raw = operation("requested", undefined);
    raw.steps = [
      {
        step_key: "im-summary",
        source: "aero-im",
        action: "summary",
        request_payload: { datasets: ["aero-im.workspaces"] },
      },
    ];
    expect(operationReceiptPlan(raw)).toBeUndefined();
  });
  it.each(["requested", "unknown"])(
    "shows the actual step range and explicitly %s continues only the original receipt",
    async (status) => {
      const item = mount(operation(status));
      await flushCommandUI();
      const step = item.root.querySelector("[data-operation-step]");
      expect(step?.textContent).toContain("本步骤明确范围");
      expect(step?.textContent).toContain("IM 工作空间");
      expect(step?.textContent).toContain("aero-im.workspaces");
      expect(item.requests.every((request) => request.method === "GET")).toBe(
        true,
      );
      const button = commandButton(
        item.root,
        status === "unknown" ? "对账未知结果" : "运行 Operation",
      );
      expect(button.disabled).toBe(false);
      button.click();
      await flushCommandUI();
      const posts = item.requests.filter(
        (request) => request.method === "POST",
      );
      expect(posts).toHaveLength(1);
      expect(posts[0].path).toBe(
        `/v1/operations/${testReceiptID}/${status === "unknown" ? "reconcile" : "run"}`,
      );
      expect(posts[0].key).toBeTruthy();
      if (status === "requested")
        expect(posts[0].key).toBe(`operation.run:${testReceiptID}`);
    },
  );
  it.each([
    { name: "missing", scope: undefined },
    { name: "explicit empty", scope: { status: "explicit", datasets: [] } },
    {
      name: "unknown status",
      scope: { status: "future", datasets: ["aero-im.workspaces"] },
    },
    {
      name: "invalid with plausible names",
      scope: { status: "invalid", datasets: ["aero-im.workspaces"] },
    },
    {
      name: "wrong source",
      scope: { status: "explicit", datasets: ["aero-vault.usage"] },
    },
    {
      name: "unapproved",
      scope: { status: "explicit", datasets: ["aero-im.private"] },
    },
    {
      name: "duplicate",
      scope: {
        status: "explicit",
        datasets: ["aero-im.workspaces", "aero-im.workspaces"],
      },
    },
    {
      name: "wrong type",
      scope: { status: "explicit", datasets: "aero-im.workspaces" },
    },
  ])(
    "keeps $name paused without borrowing root/audit/raw payload scope",
    ({ scope }) => {
      const value = operation();
      (value.steps as JsonRecord[])[0].dataset_scope = scope;
      (value.steps as JsonRecord[])[0].request_payload = {
        datasets: ["aero-im.workspaces"],
      };
      value.request_payload = { datasets: ["aero-im.workspaces"] };
      expect(operationReceiptPlan(value)).toBeUndefined();
      expect(
        operationActionAvailable(
          value,
          testReceiptID,
          commandTestReport(),
          testScope,
        ),
      ).toBe(false);
    },
  );
  it.each(["action", "compensation_action"])(
    "never coerces malformed %s into a safe scope plan",
    (field) => {
      for (const value of [["summary"], { action: "summary" }, true, 1]) {
        const receipt = operation();
        (receipt.steps as JsonRecord[])[0][field] = value;
        expect(operationReceiptPlan(receipt)).toBeUndefined();
      }
    },
  );
  it("shows an invalid scope reason and refuses raw payload fallback on the original page", async () => {
    const value = operation();
    (value.steps as JsonRecord[])[0].dataset_scope = {
      status: "invalid",
      datasets: ["aero-im.private"],
    };
    (value.steps as JsonRecord[])[0].request_payload = {
      datasets: ["aero-im.workspaces"],
    };
    const item = mount(value);
    await flushCommandUI();
    expect(
      item.root.querySelector("[data-operation-step]")?.textContent,
    ).toContain("本步骤范围无效或无法确认");
    expect(
      item.root.querySelector("[data-operation-step]")?.textContent,
    ).not.toContain("aero-im.private");
    expect(commandButton(item.root, "运行 Operation").disabled).toBe(true);
    expect(item.requests.every((request) => request.method === "GET")).toBe(
      true,
    );
  });
  it("does not borrow operator grants for compensation or bypass tenant/home checks", () => {
    const value = operation();
    (value.steps as JsonRecord[])[0].compensation_action = "erase";
    const denied = commandTestReport();
    denied.capabilities.find(
      (item) => item.name === "account.erase",
    )!.available = false;
    expect(
      operationActionAvailable(value, testReceiptID, denied, testScope),
    ).toBe(false);
    for (const patch of [{ tenant_id: "other" }, { home_region: "foreign" }])
      expect(
        operationActionAvailable(
          { ...value, ...patch },
          testReceiptID,
          commandTestReport(),
          testScope,
        ),
      ).toBe(false);
  });
});
