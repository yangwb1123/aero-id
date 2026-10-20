import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import OperationDetails from "./OperationDetails.vue";
import {
  commandButton,
  mountCommandTest,
  testAccountID,
  testOtherAccountID,
  testReceiptID,
  unmountCommandTests,
} from "./commandTestFixtures";
import { saveOperationReconcileKey } from "./operationRequestKeys";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
beforeEach(() => {
  sessionStorage.clear();
  vi.useFakeTimers();
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
});
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
  vi.useRealTimers();
});
const operationPath = `/v1/operations/${testReceiptID}`;
const confirmationPhrase = "CONFIRM OPERATION RUN";
const originalKey = "01800000-0000-7000-8000-000000000004";
function operation(patch: JsonRecord = {}): JsonRecord {
  return {
    operation_id: testReceiptID,
    account_id: testAccountID,
    actor_uid: "original-initiator",
    tenant_id: "tenant-a",
    operation_type: "sync",
    home_region: "local",
    status: "requested",
    result: { original_receipt: "original-result-marker" },
    steps: [
      {
        step_key: "summary",
        source: "aero-im",
        action: "summary",
        status: "pending",
        dataset_scope: { status: "explicit", datasets: ["aero-im.workspaces"] },
        compensation_action: "erase",
      },
    ],
    ...patch,
  };
}
function json(data: JsonRecord, headers?: HeadersInit): Response {
  return new Response(JSON.stringify({ data }), { headers });
}
function storageSnapshot(): Record<string, string | null> {
  return Object.fromEntries(
    Object.keys(sessionStorage).map((key) => [
      key,
      sessionStorage.getItem(key),
    ]),
  );
}
async function settleUI(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
  await nextTick();
}
function mountOperation(initial = operation()) {
  saveOperationReconcileKey(sessionStorage, testReceiptID, originalKey);
  let read = async () => json(initial);
  const requests: Array<{ path: string; method?: string; key: string | null }> =
    [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input)).pathname;
      requests.push({
        path,
        method: init?.method,
        key: new Headers(init?.headers).get("Idempotency-Key"),
      });
      if (path.startsWith("/v1/audit/operations/") && init?.method === "GET")
        return json({ operation_id: testReceiptID, events: [] });
      if (path === operationPath && init?.method === "GET") return read();
      if (path === `${operationPath}/run` && init?.method === "POST")
        return json(operation({ status: "completed" }));
      throw new Error("unexpected request in Run admission regression");
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher as typeof fetch,
  );
  const writes = [
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileOperation"),
    vi.spyOn(client, "reconcileSyncJob"),
    vi.spyOn(client, "downloadExport"),
  ];
  const mounted = mountCommandTest(OperationDetails, {
    client,
    operationId: testReceiptID,
  });
  return {
    ...mounted,
    client,
    requests,
    writes,
    setRead: (value: () => Promise<Response>) => {
      read = value;
    },
  };
}
type MountedOperation = ReturnType<typeof mountOperation>;
async function openConfirmation(item: MountedOperation): Promise<void> {
  await settleUI();
  commandButton(item.root, "准备运行危险 Operation").click();
  await settleUI();
  const input = item.root.querySelector("input")!;
  input.value = confirmationPhrase;
  input.dispatchEvent(new Event("input"));
  await settleUI();
  expect(commandButton(item.root, "确认运行").disabled).toBe(false);
}
async function refuseRun(item: MountedOperation): Promise<void> {
  const confirm = commandButton(item.root, "确认运行");
  expect(confirm.disabled).toBe(true);
  expect(item.root.querySelector('[title="运行已暂停"]')).not.toBeNull();
  expect(item.root.textContent).toContain("当前动作资格或可运行详情未确认");
  // Exercise the handler too: a disabled DOM button alone is not the guard.
  confirm.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  await settleUI();
  for (const write of item.writes) expect(write).not.toHaveBeenCalled();
  expect(item.requests.every((request) => request.method === "GET")).toBe(true);
}

describe("Operation Run uses the latest observed readable runnable state", () => {
  it.each(
    [
      "running",
      "compensating",
      "unknown",
      "completed",
      "partially_completed",
      "failed",
      "unrecognized",
      ["requested"],
      null,
    ].map((status) => ({
      status,
      invalidRead: typeof status !== "string" || status === "unrecognized",
    })),
  )(
    "refuses an open confirmation after polling observes $status",
    async ({ status, invalidRead }) => {
      const item = mountOperation();
      await openConfirmation(item);
      const stored = storageSnapshot();
      item.setRead(async () => json(operation({ status })));
      await vi.advanceTimersByTimeAsync(2000);
      await settleUI();

      await refuseRun(item);
      expect(item.root.querySelector("input")?.value).toBe(confirmationPhrase);
      if (invalidRead) {
        expect(item.root.textContent).toContain("任务详情与原请求不一致");
        expect(item.root.textContent).not.toContain("original-result-marker");
      } else expect(item.root.textContent).toContain("original-result-marker");
      expect(storageSnapshot()).toEqual(stored);
      expect(
        item.requests.filter((request) => request.path === operationPath),
      ).toHaveLength(2);
    },
  );

  it.each(["requested", "waiting"])(
    "requires no completion evidence even when observed status stays %s",
    async (status) => {
      const item = mountOperation();
      await openConfirmation(item);
      const stored = storageSnapshot();
      item.setRead(async () =>
        json(operation({ status, completed_at: "2026-10-15T20:00:00Z" })),
      );
      await vi.advanceTimersByTimeAsync(2000);
      await settleUI();
      await refuseRun(item);
      expect(storageSnapshot()).toEqual(stored);
    },
  );

  it.each(["wrong-id", "forbidden", "stale-read"])(
    "does not lend old metadata to an open confirmation after %s",
    async (failure) => {
      const item = mountOperation();
      await openConfirmation(item);
      const stored = storageSnapshot();
      item.setRead(async () => {
        if (failure === "wrong-id")
          return json(operation({ operation_id: testOtherAccountID }));
        if (failure === "forbidden")
          return new Response(
            JSON.stringify({
              error: { code: "operation.forbidden", message: "读取被拒绝" },
            }),
            { status: 403 },
          );
        return json(operation(), { "X-Aero-Stale-Read": "true" });
      });
      await vi.advanceTimersByTimeAsync(2000);
      await settleUI();

      await refuseRun(item);
      expect(item.root.querySelector("input")?.value).toBe(confirmationPhrase);
      expect(storageSnapshot()).toEqual(stored);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(
        item.requests.filter((request) => request.path === operationPath),
      ).toHaveLength(2);
    },
  );

  it("suspends during explicit detail reload, retaining the input/key without a POST", async () => {
    const item = mountOperation();
    await openConfirmation(item);
    const stored = storageSnapshot();
    let finish!: (value: Response) => void;
    item.setRead(() => new Promise<Response>((resolve) => (finish = resolve)));
    commandButton(item.root, "刷新状态").click();
    await settleUI();

    await refuseRun(item);
    expect(item.root.querySelector("input")?.value).toBe(confirmationPhrase);
    expect(storageSnapshot()).toEqual(stored);
    finish(json(operation({ status: "waiting", completed_at: null })));
    await settleUI();
    expect(commandButton(item.root, "确认运行").disabled).toBe(false);
    expect(item.root.querySelector("input")?.value).toBe(confirmationPhrase);
    for (const write of item.writes) expect(write).not.toHaveBeenCalled();
  });

  it.each(["requested", "waiting"])(
    "keeps %s runnable only by an explicit confirmed POST with the original ID/key",
    async (status) => {
      const item = mountOperation();
      await openConfirmation(item);
      const stored = storageSnapshot();
      item.setRead(async () => json(operation({ status, completed_at: null })));
      await vi.advanceTimersByTimeAsync(2000);
      await settleUI();
      expect(commandButton(item.root, "确认运行").disabled).toBe(false);
      for (const write of item.writes) expect(write).not.toHaveBeenCalled();
      commandButton(item.root, "确认运行").click();
      await settleUI();

      expect(item.client.runOperation).toHaveBeenCalledExactlyOnceWith(
        testReceiptID,
        `operation.run:${testReceiptID}`,
      );
      expect(
        item.requests.filter((request) => request.method === "POST"),
      ).toEqual([
        {
          path: `${operationPath}/run`,
          method: "POST",
          key: `operation.run:${testReceiptID}`,
        },
      ]);
      expect(item.client.commands.prepare).not.toHaveBeenCalled();
      expect(item.client.commands.execute).not.toHaveBeenCalled();
      expect(item.client.reconcileOperation).not.toHaveBeenCalled();
      expect(item.root.textContent).toContain("original-result-marker");
      expect(storageSnapshot()).toEqual(stored);
    },
  );
});
