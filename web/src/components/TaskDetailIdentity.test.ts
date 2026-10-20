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
  saveOperationReconcileKey,
  saveSyncJobReconcileKey,
} from "./operationRequestKeys";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
beforeEach(() => sessionStorage.clear());
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
});
const otherID = "01800000-0000-7000-8000-000000000099";
const originalKey = "01800000-0000-7000-8000-000000000004";
const cases = [
  { kind: "sync", status: "unknown", action: "对账未知结果" },
  { kind: "sync", status: "completed", action: "一次性下载" },
  { kind: "operation", status: "unknown", action: "对账未知结果" },
  { kind: "operation", status: "requested", action: "运行 Operation" },
] as const;
type Kind = (typeof cases)[number]["kind"];
function task(kind: Kind, status: string, id = testReceiptID): JsonRecord {
  const common = {
    account_id: testAccountID,
    tenant_id: "tenant-a",
    actor_uid: "original-initiator",
    status,
    result: {
      original_receipt:
        id === testReceiptID
          ? "original-receipt-marker"
          : "wrong-receipt-marker",
    },
  };
  return kind === "sync"
    ? {
        ...common,
        job_id: id,
        job_type: "export",
        request: { datasets: ["aero-vault.usage"] },
      }
    : {
        ...common,
        operation_id: id,
        operation_type: "sync",
        home_region: "local",
        steps: [
          {
            step_key: "summary",
            source: "aero-im",
            action: "summary",
            status: "pending",
            dataset_scope: {
              status: "explicit",
              datasets: ["aero-im.workspaces"],
            },
          },
        ],
      };
}
function json(data: JsonRecord): Response {
  return new Response(JSON.stringify({ data }), {
    headers: { "Content-Type": "application/json" },
  });
}
function storageSnapshot(): Record<string, string | null> {
  return Object.fromEntries(
    Object.keys(sessionStorage).map((key) => [
      key,
      sessionStorage.getItem(key),
    ]),
  );
}
function mountTask(kind: Kind, fetcher: typeof fetch) {
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher,
  );
  const writes = [
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileOperation"),
    vi.spyOn(client, "reconcileSyncJob"),
    vi.spyOn(client, "downloadExport"),
  ];
  const mounted = mountCommandTest(
    kind === "sync" ? JobDetails : OperationDetails,
    kind === "sync"
      ? { client, jobId: testReceiptID }
      : { client, operationId: testReceiptID },
  );
  return { ...mounted, writes };
}

describe("real task client identity binding at the existing details", () => {
  it.each(cases)(
    "$kind/$status refuses initial and refresh substitutions; retry only GETs the original receipt",
    async ({ kind, status, action }) => {
      saveSyncJobReconcileKey(sessionStorage, testReceiptID, originalKey);
      saveOperationReconcileKey(sessionStorage, testReceiptID, originalKey);
      const stored = storageSnapshot();
      let taskReads = 0;
      const requests: Array<{ path: string; method?: string }> = [];
      const fetcher = vi.fn(
        async (input: RequestInfo | URL, init?: RequestInit) => {
          const path = new URL(String(input)).pathname;
          requests.push({ path, method: init?.method });
          if (path.startsWith("/v1/audit/operations/"))
            return json({ operation_id: testReceiptID, events: [] });
          taskReads += 1;
          return json(
            task(kind, status, taskReads % 2 === 1 ? otherID : testReceiptID),
          );
        },
      );
      const item = mountTask(kind, fetcher as typeof fetch);
      await flushCommandUI();

      expect(item.root.textContent).toContain("详情与原请求不一致");
      expect(item.root.textContent).not.toContain("wrong-receipt-marker");
      expect(item.root.textContent).not.toContain(action);
      expect(storageSnapshot()).toEqual(stored);
      expect(taskReads).toBe(1);

      commandButton(item.root, "重试").click();
      await flushCommandUI();
      expect(item.root.textContent).toContain("original-receipt-marker");
      expect(commandButton(item.root, action).disabled).toBe(false);
      expect(taskReads).toBe(2);

      commandButton(item.root, "刷新状态").click();
      await flushCommandUI();
      expect(item.root.textContent).toContain("详情与原请求不一致");
      expect(item.root.textContent).not.toContain("wrong-receipt-marker");
      expect(item.root.textContent).not.toContain(action);
      expect(storageSnapshot()).toEqual(stored);
      expect(taskReads).toBe(3);

      commandButton(item.root, "重试").click();
      await flushCommandUI();
      expect(item.root.textContent).toContain("original-receipt-marker");
      expect(item.root.textContent).not.toContain("wrong-receipt-marker");
      expect(commandButton(item.root, action).disabled).toBe(false);
      expect(storageSnapshot()).toEqual(stored);
      expect(taskReads).toBe(4);
      expect(requests.every((request) => request.method === "GET")).toBe(true);
      const expectedPath = `/v1${kind === "sync" ? "/sync/jobs" : "/operations"}/${testReceiptID}`;
      expect(
        requests.filter((request) => !request.path.includes("/audit/")),
      ).toEqual(
        Array.from({ length: 4 }, () => ({
          path: expectedPath,
          method: "GET",
        })),
      );
      for (const write of item.writes) expect(write).not.toHaveBeenCalled();
    },
  );

  it.each(["sync", "operation"] as const)(
    "%s keeps the unmount/late-read guard without clearing original keys",
    async (kind) => {
      saveSyncJobReconcileKey(sessionStorage, testReceiptID, originalKey);
      saveOperationReconcileKey(sessionStorage, testReceiptID, originalKey);
      const stored = storageSnapshot();
      let complete!: (response: Response) => void;
      let readSignal: AbortSignal | null | undefined;
      const fetcher = vi.fn(
        async (input: RequestInfo | URL, init?: RequestInit) => {
          if (new URL(String(input)).pathname.includes("/audit/"))
            return json({ operation_id: testReceiptID, events: [] });
          readSignal = init?.signal;
          return new Promise<Response>((resolve) => {
            complete = resolve;
          });
        },
      );
      const item = mountTask(kind, fetcher as typeof fetch);
      await flushCommandUI();
      expect(readSignal?.aborted).toBe(false);
      unmountCommandTests();
      expect(readSignal?.aborted).toBe(true);
      complete(json(task(kind, "unknown")));
      await flushCommandUI();

      expect(item.root.textContent).toBe("");
      expect(storageSnapshot()).toEqual(stored);
      expect(fetcher).toHaveBeenCalledTimes(kind === "sync" ? 1 : 2);
      for (const write of item.writes) expect(write).not.toHaveBeenCalled();
    },
  );
});
