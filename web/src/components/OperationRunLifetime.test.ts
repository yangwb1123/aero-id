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

let retainedPrepare: (() => void) | undefined;
let retainedExecute: (() => void) | undefined;
vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, h } = await import("vue");
  const { irisCommandTestStubs } = await import("./commandTestFixtures");
  return {
    ...irisCommandTestStubs(),
    IrisButton: defineComponent({
      inheritAttrs: false,
      props: { disabled: Boolean, type: String },
      setup:
        (props, { slots, attrs }) =>
        () => {
          const children = slots.default?.();
          const listener = attrs.onClick;
          const click = () => {
            if (typeof listener === "function") listener();
          };
          if (
            children?.some(
              (child) =>
                typeof child.children === "string" &&
                child.children.includes("运行 Operation"),
            ) ||
            children?.some(
              (child) => child.children === "准备运行危险 Operation",
            )
          )
            retainedPrepare = click;
          if (children?.some((child) => child.children === "确认运行"))
            retainedExecute = click;
          return h(
            "button",
            { disabled: props.disabled, type: props.type, onClick: click },
            children,
          );
        },
    }),
  };
});
beforeEach(() => {
  retainedPrepare = undefined;
  retainedExecute = undefined;
  sessionStorage.clear();
  vi.useFakeTimers();
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
});
afterEach(async () => {
  unmountCommandTests();
  await settleUI();
  vi.restoreAllMocks();
  vi.useRealTimers();
});
const operationPath = `/v1/operations/${testReceiptID}`;
const runKey = `operation.run:${testReceiptID}`;
const originalReconcileKey = "01800000-0000-7000-8000-000000000004";
const modes = ["ordinary", "compensation", "erase"] as const;
type Mode = (typeof modes)[number];
function operation(mode: Mode, status = "requested"): JsonRecord {
  return {
    operation_id: testReceiptID,
    account_id: testAccountID,
    actor_uid: "original-initiator",
    tenant_id: "tenant-a",
    operation_type: mode === "erase" ? "erase" : "sync",
    home_region: "local",
    status,
    result: { original_receipt: "original-result-marker" },
    steps: [
      {
        step_key: "original-step",
        source: "aero-im",
        action: mode === "erase" ? "erase" : "summary",
        dataset_scope: { status: "explicit", datasets: ["aero-im.workspaces"] },
        ...(mode === "compensation" ? { compensation_action: "erase" } : {}),
      },
    ],
  };
}
function json(data: JsonRecord): Response {
  return new Response(JSON.stringify({ data }));
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
function mountOperation(mode: Mode, status = "requested") {
  saveOperationReconcileKey(
    sessionStorage,
    testReceiptID,
    originalReconcileKey,
  );
  sessionStorage.setItem("unrelated-recovery-pointer", testOtherAccountID);
  let run = async () => json(operation(mode, "completed"));
  const requests: Array<{
    path: string;
    method?: string;
    key: string | null;
    signal?: AbortSignal | null;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input)).pathname;
      requests.push({
        path,
        method: init?.method,
        key: new Headers(init?.headers).get("Idempotency-Key"),
        signal: init?.signal,
      });
      if (path === operationPath && init?.method === "GET")
        return json(operation(mode, status));
      if (path.startsWith("/v1/audit/operations/") && init?.method === "GET")
        return json({ operation_id: testReceiptID, events: [] });
      if (path === `${operationPath}/run` && init?.method === "POST")
        return run();
      throw new Error("unexpected request in Run lifetime regression");
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher as typeof fetch,
  );
  const runWrite = vi.spyOn(client, "runOperation");
  const otherWrites = [
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client, "reconcileOperation"),
    vi.spyOn(client, "reconcileSyncJob"),
    vi.spyOn(client, "downloadExport"),
  ];
  const changed = vi.fn();
  const mounted = mountCommandTest(OperationDetails, {
    client,
    operationId: testReceiptID,
    onChanged: changed,
  });
  return {
    ...mounted,
    requests,
    runWrite,
    otherWrites,
    changed,
    setRun: (value: () => Promise<Response>) => {
      run = value;
    },
  };
}
type MountedOperation = ReturnType<typeof mountOperation>;
async function captureRun(item: MountedOperation, mode: Mode) {
  await settleUI();
  expect(retainedPrepare).toBeTypeOf("function");
  const prepare = retainedPrepare!;
  if (mode !== "ordinary") {
    commandButton(item.root, "准备运行危险 Operation").click();
    await settleUI();
    const input = item.root.querySelector("input")!;
    input.value = mode === "erase" ? "CONFIRM ERASE" : "CONFIRM OPERATION RUN";
    input.dispatchEvent(new Event("input"));
    await settleUI();
    expect(commandButton(item.root, "确认运行").disabled).toBe(false);
    expect(retainedExecute).toBeTypeOf("function");
  }
  expect(item.runWrite).not.toHaveBeenCalled();
  return { prepare, execute: mode === "ordinary" ? prepare : retainedExecute! };
}
function expectNoOtherWrites(item: MountedOperation): void {
  for (const write of item.otherWrites) expect(write).not.toHaveBeenCalled();
}
function expectNoRun(
  item: MountedOperation,
  stored: ReturnType<typeof storageSnapshot>,
) {
  expect(item.runWrite).not.toHaveBeenCalled();
  expect(item.changed).not.toHaveBeenCalled();
  expectNoOtherWrites(item);
  expect(storageSnapshot()).toEqual(stored);
  expect(
    item.requests.every(
      (request) => request.method === "GET" && request.key === null,
    ),
  ).toBe(true);
}
function posts(item: MountedOperation) {
  return item.requests.filter((request) => request.method === "POST");
}
const runnableCases = modes.flatMap((mode) =>
  ["requested", "waiting"].map((status) => ({ mode, status })),
);

describe("Operation Run admission belongs to the mounted detail", () => {
  it.each(runnableCases)(
    "refuses retained $mode listeners after unmount of a readable $status detail",
    async ({ mode, status }) => {
      const item = mountOperation(mode, status);
      const handlers = await captureRun(item, mode);
      const stored = storageSnapshot();
      unmountCommandTests();
      // Call the captured original listeners, not detached or disabled DOM nodes.
      handlers.execute();
      handlers.prepare();
      await settleUI();
      await vi.advanceTimersByTimeAsync(60_000);
      expectNoRun(item, stored);
    },
  );

  it.each(runnableCases)(
    "still admits one explicit mounted $mode Run of $status with the original ID/key",
    async ({ mode, status }) => {
      const item = mountOperation(mode, status);
      const handlers = await captureRun(item, mode);
      const stored = storageSnapshot();
      expectNoRun(item, stored);
      handlers.execute();
      await settleUI();
      expect(item.runWrite).toHaveBeenCalledExactlyOnceWith(
        testReceiptID,
        runKey,
      );
      expect(posts(item)).toMatchObject([
        { path: `${operationPath}/run`, method: "POST", key: runKey },
      ]);
      expect(item.changed).toHaveBeenCalledTimes(1);
      expect(item.root.textContent).toContain("original-result-marker");
      expect(storageSnapshot()).toEqual(stored);
      expectNoOtherWrites(item);
    },
  );

  it.each(modes)(
    "does not let an old %s listener borrow a fresh same-ID mount or client",
    async (mode) => {
      const previous = mountOperation(mode);
      const old = await captureRun(previous, mode);
      const stored = storageSnapshot();
      unmountCommandTests();
      const current = mountOperation(mode);
      const fresh = await captureRun(current, mode);
      old.execute();
      old.prepare();
      await settleUI();
      expectNoRun(previous, stored);
      expectNoRun(current, stored);
      fresh.execute();
      await settleUI();
      expect(previous.runWrite).not.toHaveBeenCalled();
      expect(previous.changed).not.toHaveBeenCalled();
      expect(current.runWrite).toHaveBeenCalledExactlyOnceWith(
        testReceiptID,
        runKey,
      );
      expect(current.changed).toHaveBeenCalledTimes(1);
      expect(storageSnapshot()).toEqual(stored);
      expectNoOtherWrites(current);
    },
  );

  it.each(modes)(
    "keeps %s Run single-in-flight without queuing another click",
    async (mode) => {
      const item = mountOperation(mode);
      const handlers = await captureRun(item, mode);
      const stored = storageSnapshot();
      let finish!: (value: Response) => void;
      item.setRun(() => new Promise<Response>((resolve) => (finish = resolve)));
      handlers.execute();
      handlers.execute();
      handlers.prepare();
      await settleUI();
      expect(item.runWrite).toHaveBeenCalledExactlyOnceWith(
        testReceiptID,
        runKey,
      );
      expect(item.changed).not.toHaveBeenCalled();
      expect(posts(item)).toHaveLength(1);
      finish(json(operation(mode, "unknown")));
      await settleUI();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.runWrite).toHaveBeenCalledTimes(1);
      expect(item.changed).toHaveBeenCalledTimes(1);
      expect(
        item.root.querySelector('[title="远端结果尚未确认"]'),
      ).not.toBeNull();
      expect(item.root.textContent).toContain("对账会查询来源 operation ID");
      handlers.execute();
      await settleUI();
      expect(item.runWrite).toHaveBeenCalledTimes(1);
      expect(storageSnapshot()).toEqual(stored);
      expectNoOtherWrites(item);
    },
  );

  it.each(modes)(
    "retains %s Run's stable key after uncertainty; only another explicit action retries",
    async (mode) => {
      const item = mountOperation(mode);
      const handlers = await captureRun(item, mode);
      const stored = storageSnapshot();
      item.setRun(async () => {
        throw new TypeError("lost Run response");
      });
      handlers.execute();
      await settleUI();
      expect(item.runWrite).toHaveBeenCalledExactlyOnceWith(
        testReceiptID,
        runKey,
      );
      expect(item.changed).not.toHaveBeenCalled();
      expect(storageSnapshot()).toEqual(stored);
      // Metadata polling remains GET-only; it must not retry Run or clear keys.
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.runWrite).toHaveBeenCalledTimes(1);
      expect(posts(item)).toHaveLength(1);
      expect(storageSnapshot()).toEqual(stored);
      item.setRun(async () => json(operation(mode, "completed")));
      handlers.execute();
      await settleUI();
      expect(item.runWrite).toHaveBeenNthCalledWith(2, testReceiptID, runKey);
      expect(posts(item).map((request) => request.key)).toEqual([
        runKey,
        runKey,
      ]);
      expect(item.changed).toHaveBeenCalledTimes(1);
      expect(storageSnapshot()).toEqual(stored);
      expectNoOtherWrites(item);
    },
  );

  it.each(
    modes.flatMap((mode) =>
      ["completed", "unknown", "lost-response"].map((outcome) => ({
        mode,
        outcome,
      })),
    ),
  )(
    "does not revoke or adopt an admitted $mode Run after unmount ($outcome)",
    async ({ mode, outcome }) => {
      const item = mountOperation(mode);
      const handlers = await captureRun(item, mode);
      const stored = storageSnapshot();
      let finish!: (value: Response) => void;
      let fail!: (reason: Error) => void;
      item.setRun(
        () =>
          new Promise<Response>((resolve, reject) => {
            finish = resolve;
            fail = reject;
          }),
      );
      handlers.execute();
      await settleUI();
      expect(item.runWrite).toHaveBeenCalledExactlyOnceWith(
        testReceiptID,
        runKey,
      );
      const admitted = posts(item)[0];
      unmountCommandTests();
      expect(admitted.signal?.aborted).toBe(false);
      if (outcome === "lost-response") fail(new TypeError("lost Run response"));
      else finish(json(operation(mode, outcome)));
      await settleUI();
      handlers.execute();
      handlers.prepare();
      await settleUI();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.runWrite).toHaveBeenCalledTimes(1);
      expect(posts(item)).toHaveLength(1);
      expect(item.changed).not.toHaveBeenCalled();
      expect(storageSnapshot()).toEqual(stored);
      expectNoOtherWrites(item);
    },
  );
});
