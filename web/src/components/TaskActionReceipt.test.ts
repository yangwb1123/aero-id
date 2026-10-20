import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import JobDetails from "./JobDetails.vue";
import OperationDetails from "./OperationDetails.vue";
import {
  commandButton,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testReceiptID,
  unmountCommandTests,
} from "./commandTestFixtures";
import {
  loadOperationReconcileKey,
  loadSyncJobReconcileKey,
  saveOperationReconcileKey,
  saveSyncJobReconcileKey,
} from "./operationRequestKeys";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
beforeEach(() => {
  sessionStorage.clear();
  saveOperationReconcileKey(sessionStorage, testReceiptID, originalKey);
  saveSyncJobReconcileKey(sessionStorage, testReceiptID, originalKey);
});
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
});
const originalKey = "01800000-0000-7000-8000-000000000004";
const otherID = "01800000-0000-7000-8000-000000000099";
const flows = [
  { kind: "sync", action: "reconcile", field: "job_id", label: "对账未知结果" },
  {
    kind: "operation",
    action: "reconcile",
    field: "operation_id",
    label: "对账未知结果",
  },
  {
    kind: "operation",
    action: "run",
    field: "operation_id",
    label: "运行 Operation",
  },
] as const;
type Flow = (typeof flows)[number];
function task(flow: Flow): JsonRecord {
  const common = {
    account_id: testAccountID,
    tenant_id: "tenant-a",
    actor_uid: "original-initiator",
    status: flow.action === "run" ? "requested" : "unknown",
    result: { receipt: "original-receipt-marker" },
  };
  return flow.kind === "sync"
    ? {
        ...common,
        job_id: testReceiptID,
        job_type: "export",
        request: { datasets: ["aero-vault.usage"] },
      }
    : {
        ...common,
        operation_id: testReceiptID,
        operation_type: "sync",
        home_region: "local",
        steps: [
          {
            step_key: "summary",
            source: "aero-im",
            action: "summary",
            dataset_scope: {
              status: "explicit",
              datasets: ["aero-im.workspaces"],
            },
          },
        ],
      };
}
function completed(flow: Flow): JsonRecord {
  return {
    ...task(flow),
    status: "completed",
    result: { receipt: "action-receipt-marker" },
  };
}
function json(data: JsonRecord): Response {
  return new Response(JSON.stringify({ data }), {
    headers: { "Content-Type": "application/json" },
  });
}
function stored(): Record<string, string | null> {
  return Object.fromEntries(
    Object.keys(sessionStorage).map((key) => [
      key,
      sessionStorage.getItem(key),
    ]),
  );
}
function mountTask(flow: Flow, reply: () => Response | Promise<Response>) {
  const requests: Array<{ url: URL; init?: RequestInit }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      requests.push({ url, init });
      if (init?.method === "POST") return reply();
      return json(
        url.pathname.includes("/audit/")
          ? { operation_id: testReceiptID, events: [] }
          : task(flow),
      );
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher as typeof fetch,
  );
  const commands = [
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client, "createOperation"),
    vi.spyOn(client, "createExportJob"),
    vi.spyOn(client, "downloadExport"),
  ];
  const changed = vi.fn();
  const item = mountCommandTest(
    flow.kind === "sync" ? JobDetails : OperationDetails,
    flow.kind === "sync"
      ? { client, jobId: testReceiptID, onChanged: changed }
      : { client, operationId: testReceiptID, onChanged: changed },
  );
  return { ...item, requests, commands, changed };
}
const badShapes = [
  "id alias",
  "other family",
  "nested only",
  "missing",
  "null with alias",
  "number with alias",
  "boolean with alias",
  "array with alias",
  "object with alias",
  "wrong ID with alias",
  "padded ID",
] as const;
function badReceipt(flow: Flow, shape: (typeof badShapes)[number]) {
  const value = completed(flow);
  delete value[flow.field];
  if (shape === "id alias") value.id = testReceiptID;
  else if (shape === "other family")
    value[flow.field === "job_id" ? "operation_id" : "job_id"] = testReceiptID;
  else if (shape === "nested only")
    value.receipt = { [flow.field]: testReceiptID };
  else if (shape === "padded ID") value[flow.field] = ` ${testReceiptID} `;
  else if (shape !== "missing") {
    const ids: Record<string, unknown> = {
      "null with alias": null,
      "number with alias": 3,
      "boolean with alias": true,
      "array with alias": [testReceiptID],
      "object with alias": { id: testReceiptID },
      "wrong ID with alias": otherID,
    };
    value[flow.field] = ids[shape];
    value.id = testReceiptID;
  }
  return value;
}
function invalidStatuses(flow: Flow) {
  return [
    { label: "missing", status: undefined },
    { label: "null", status: null },
    { label: "number", status: 1 },
    { label: "boolean", status: true },
    { label: "singleton array", status: ["completed"] },
    { label: "nested array", status: [["completed"]] },
    { label: "object", status: { status: "completed" } },
    { label: "padded string", status: " completed " },
    { label: "future status", status: "future-status" },
    { label: "unsupported cancellation", status: "canceled" },
    ...(flow.kind === "sync"
      ? ["requested", "waiting", "compensating"]
      : ["pending"]
    ).map((status) => ({ label: `other family ${status}`, status })),
  ];
}
function posts(item: ReturnType<typeof mountTask>) {
  return item.requests.filter(({ init }) => init?.method === "POST");
}
function assertOriginalPost(item: ReturnType<typeof mountTask>, flow: Flow) {
  const sent = posts(item);
  const key =
    flow.action === "run" ? `operation.run:${testReceiptID}` : originalKey;
  for (const { url, init } of sent) {
    expect(url.pathname).toBe(
      `/v1/${flow.kind === "sync" ? "sync/jobs" : "operations"}/${testReceiptID}/${flow.action}`,
    );
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBe(key);
    expect(init?.body).toBeUndefined();
  }
  for (const command of item.commands) expect(command).not.toHaveBeenCalled();
}

describe.each(flows)("$kind $action receipt adoption", (flow) => {
  it.each(badShapes)(
    "rejects %s without replacing the original task or clearing any key",
    async (shape) => {
      const snapshot = stored();
      const item = mountTask(flow, () => json(badReceipt(flow, shape)));
      await flushCommandUI();
      commandButton(item.root, flow.label).click();
      await flushCommandUI();

      expect(item.root.textContent).toContain("回执与原");
      expect(item.root.textContent).toContain("original-receipt-marker");
      expect(item.root.textContent).not.toContain("action-receipt-marker");
      expect(item.root.textContent).not.toContain(otherID);
      expect(commandButton(item.root, flow.label).disabled).toBe(false);
      expect(stored()).toEqual(snapshot);
      expect(item.changed).not.toHaveBeenCalled();
      expect(posts(item)).toHaveLength(1);
      const count = item.requests.length;
      await flushCommandUI();
      expect(item.requests).toHaveLength(count);
      assertOriginalPost(item, flow);
    },
  );

  it.each(invalidStatuses(flow))(
    "rejects $label status without adopting a success or clearing the original key",
    async ({ status }) => {
      const snapshot = stored();
      const item = mountTask(flow, () => json({ ...completed(flow), status }));
      await flushCommandUI();
      commandButton(item.root, flow.label).click();
      await flushCommandUI();

      expect(item.root.textContent).toContain("回执与原");
      expect(item.root.textContent).toContain("original-receipt-marker");
      expect(item.root.textContent).not.toContain("action-receipt-marker");
      expect(commandButton(item.root, flow.label).disabled).toBe(false);
      expect(stored()).toEqual(snapshot);
      expect(item.changed).not.toHaveBeenCalled();
      expect(posts(item)).toHaveLength(1);
      const count = item.requests.length;
      await flushCommandUI();
      expect(item.requests).toHaveLength(count);
      assertOriginalPost(item, flow);
    },
  );

  it.each([
    { rejected: "ID alias", accepted: "completed" },
    { rejected: "array status", accepted: "unknown" },
    { rejected: "other family status", accepted: "completed" },
  ])(
    "keeps the original key after $rejected, GET refresh, and an explicit original-ID retry to $accepted",
    async ({ rejected, accepted }) => {
      const snapshot = stored();
      let valid = false;
      const invalid =
        rejected === "ID alias"
          ? badReceipt(flow, "id alias")
          : {
              ...completed(flow),
              status:
                rejected === "array status"
                  ? ["completed"]
                  : flow.kind === "sync"
                    ? "waiting"
                    : "pending",
            };
      const item = mountTask(flow, () =>
        json(valid ? { ...completed(flow), status: accepted } : invalid),
      );
      await flushCommandUI();
      commandButton(item.root, flow.label).click();
      await flushCommandUI();
      expect(stored()).toEqual(snapshot);
      expect(item.root.textContent).toContain("original-receipt-marker");
      expect(item.changed).not.toHaveBeenCalled();

      commandButton(item.root, "刷新状态").click();
      await flushCommandUI();
      expect(posts(item)).toHaveLength(1);
      expect(stored()).toEqual(snapshot);
      const read = item.requests.at(-1)!;
      expect(read.init?.method).toBe("GET");
      expect(read.url.pathname).toBe(
        `/v1/${flow.kind === "sync" ? "sync/jobs" : "operations"}/${testReceiptID}`,
      );
      valid = true;
      commandButton(item.root, flow.label).click();
      await flushCommandUI();
      expect(posts(item)).toHaveLength(2);
      expect(item.root.textContent).toContain("action-receipt-marker");
      expect(item.root.textContent).not.toContain("original-receipt-marker");
      expect(item.changed).toHaveBeenCalledExactlyOnceWith();
      expect(loadOperationReconcileKey(sessionStorage, testReceiptID)).toBe(
        flow.kind === "operation" && flow.action === "reconcile"
          ? undefined
          : originalKey,
      );
      expect(loadSyncJobReconcileKey(sessionStorage, testReceiptID)).toBe(
        flow.kind === "sync" ? undefined : originalKey,
      );
      assertOriginalPost(item, flow);
    },
  );

  it("ignores an in-flight POST result after unmount without treating unmount as rollback", async () => {
    const snapshot = stored();
    let finish!: (response: Response) => void;
    const item = mountTask(
      flow,
      () => new Promise<Response>((resolve) => (finish = resolve)),
    );
    await flushCommandUI();
    commandButton(item.root, flow.label).click();
    await flushCommandUI();
    expect(posts(item)).toHaveLength(1);
    const signal = posts(item)[0].init?.signal;
    expect(signal?.aborted).toBe(false);
    unmountCommandTests();
    expect(signal?.aborted).toBe(false);
    finish(json(completed(flow)));
    await flushCommandUI();
    expect(item.root.textContent).toBe("");
    expect(stored()).toEqual(snapshot);
    expect(item.changed).not.toHaveBeenCalled();
    expect(posts(item)).toHaveLength(1);
    assertOriginalPost(item, flow);
  });
});
