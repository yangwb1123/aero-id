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
import {
  loadOperationReconcileKey,
  saveOperationReconcileKey,
} from "./operationRequestKeys";

let retainedReconcile: (() => void) | undefined;
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
          if (children?.some((child) => child.children === "对账未知结果"))
            retainedReconcile = click;
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
  retainedReconcile = undefined;
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
const originalKey = "01800000-0000-7000-8000-000000000004";
function operation(patch: JsonRecord = {}): JsonRecord {
  return {
    operation_id: testReceiptID,
    account_id: testAccountID,
    actor_uid: "original-initiator",
    tenant_id: "tenant-a",
    operation_type: "sync",
    home_region: "local",
    status: "unknown",
    result: { source_operation_id: "original-result-marker" },
    steps: [
      {
        step_key: "summary",
        source: "aero-im",
        action: "summary",
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
function mountOperation(withOriginalKey = true) {
  if (withOriginalKey)
    saveOperationReconcileKey(sessionStorage, testReceiptID, originalKey);
  sessionStorage.setItem("unrelated-recovery-pointer", testOtherAccountID);
  let read = async () => json(operation());
  let reconcile = async () => json(operation());
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
      if (path === operationPath && init?.method === "GET") return read();
      if (path.startsWith("/v1/audit/operations/") && init?.method === "GET")
        return json({ operation_id: testReceiptID, events: [] });
      if (path === `${operationPath}/reconcile` && init?.method === "POST")
        return reconcile();
      throw new Error("unexpected request in Reconcile admission regression");
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher as typeof fetch,
  );
  const reconcileWrite = vi.spyOn(client, "reconcileOperation");
  const otherWrites = [
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileSyncJob"),
    vi.spyOn(client, "downloadExport"),
  ];
  const writes = [...otherWrites, reconcileWrite];
  const changed = vi.fn();
  const mounted = mountCommandTest(OperationDetails, {
    client,
    operationId: testReceiptID,
    onChanged: changed,
  });
  return {
    ...mounted,
    client,
    requests,
    writes,
    otherWrites,
    reconcileWrite,
    changed,
    setRead: (value: () => Promise<Response>) => {
      read = value;
    },
    setReconcile: (value: () => Promise<Response>) => {
      reconcile = value;
    },
  };
}
type MountedOperation = ReturnType<typeof mountOperation>;
async function captureReconcile(): Promise<() => void> {
  await settleUI();
  expect(retainedReconcile).toBeTypeOf("function");
  return retainedReconcile!;
}
async function expectRefusal(
  item: MountedOperation,
  handler: () => void,
  stored: ReturnType<typeof storageSnapshot>,
): Promise<void> {
  // Invoke the original component listener, not a disabled or removed DOM node.
  handler();
  await settleUI();
  for (const write of item.writes) expect(write).not.toHaveBeenCalled();
  expect(item.changed).not.toHaveBeenCalled();
  expect(
    item.requests.every(
      (request) => request.method === "GET" && request.key === null,
    ),
  ).toBe(true);
  expect(storageSnapshot()).toEqual(stored);
}

describe("Operation Reconcile uses the latest observed readable unknown state", () => {
  it.each([
    "requested",
    "running",
    "waiting",
    "compensating",
    "completed",
    "partially_completed",
    "failed",
    "pending",
    "unrecognized",
    ["unknown"],
    null,
  ])("refuses a retained handler after GET observes %j", async (status) => {
    const item = mountOperation();
    const handler = await captureReconcile();
    const stored = storageSnapshot();
    item.setRead(async () => json(operation({ status })));
    commandButton(item.root, "刷新状态").click();
    await settleUI();
    await expectRefusal(item, handler, stored);
  });

  it.each([
    "denied",
    "unavailable",
    "wrong-id",
    "stale",
    "invalid-json",
    "network",
  ])("does not lend old unknown metadata after a %s read", async (failure) => {
    const item = mountOperation();
    const handler = await captureReconcile();
    const stored = storageSnapshot();
    item.setRead(async () => {
      if (failure === "network") throw new TypeError("lost read response");
      if (failure === "invalid-json") return new Response("{");
      if (failure === "wrong-id")
        return json(operation({ operation_id: testOtherAccountID }));
      if (failure === "stale")
        return json(operation(), { "X-Aero-Stale-Read": "true" });
      return new Response(
        JSON.stringify({
          error: {
            code: "operation.read_unavailable",
            message: "current read unavailable",
          },
        }),
        { status: failure === "denied" ? 403 : 503 },
      );
    });
    commandButton(item.root, "刷新状态").click();
    await settleUI();
    expect(item.root.textContent).not.toContain("original-result-marker");
    await expectRefusal(item, handler, stored);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(
      item.requests.filter((request) => request.path === operationPath),
    ).toHaveLength(2);
  });

  it.each(["fetch", "body"])(
    "refuses during foreground %s, without generating a key or automatically reconciling after a readable unknown",
    async (stage) => {
      const item = mountOperation(false);
      const handler = await captureReconcile();
      const stored = storageSnapshot();
      const random = vi.spyOn(crypto, "randomUUID");
      let finish!: (response: Response) => void;
      let body!: ReadableStreamDefaultController<Uint8Array>;
      item.setRead(async () =>
        stage === "fetch"
          ? new Promise<Response>((resolve) => {
              finish = resolve;
            })
          : new Response(
              new ReadableStream<Uint8Array>({
                start: (value) => {
                  body = value;
                },
              }),
            ),
      );
      commandButton(item.root, "刷新状态").click();
      await settleUI();
      expect(item.root.textContent).toContain("正在读取平台数据");
      // The GET has its own request ID; refusal must not mint a command key.
      const requestIDs = random.mock.calls.length;
      expect(requestIDs).toBe(1);
      await expectRefusal(item, handler, stored);
      expect(random).toHaveBeenCalledTimes(requestIDs);
      if (stage === "fetch") finish(json(operation()));
      else {
        body.enqueue(
          new TextEncoder().encode(JSON.stringify({ data: operation() })),
        );
        body.close();
      }
      await settleUI();
      expect(commandButton(item.root, "对账未知结果").disabled).toBe(false);
      await vi.advanceTimersByTimeAsync(60_000);
      await expectRefusalWithoutClick(item, stored);
      expect(random).toHaveBeenCalledTimes(requestIDs);
    },
  );

  it.each(["operation.manage", "source.sync", "account.erase"])(
    "still requires current %s, including compensation guidance, in the handler",
    async (permission) => {
      const item = mountOperation();
      const handler = await captureReconcile();
      const stored = storageSnapshot();
      item.report.value!.capabilities.find(
        (feature) => feature.name === permission,
      )!.available = false;
      await settleUI();
      expect(commandButton(item.root, "对账未知结果").disabled).toBe(true);
      await expectRefusal(item, handler, stored);
    },
  );

  it.each(["report", "scope", "tenant", "home"])(
    "does not lend old unknown metadata after current %s guidance becomes unusable",
    async (failure) => {
      const item = mountOperation();
      const handler = await captureReconcile();
      const stored = storageSnapshot();
      if (failure === "report") item.report.value = undefined;
      if (failure === "scope") item.scope.value = undefined;
      if (failure === "tenant") item.report.value!.tenant_id = "other-tenant";
      if (failure === "home") item.report.value!.local_region = "other-region";
      await settleUI();
      expect(commandButton(item.root, "对账未知结果").disabled).toBe(true);
      await expectRefusal(item, handler, stored);
    },
  );

  it("refuses the retained handler after unmount, keeping the original key", async () => {
    const item = mountOperation();
    const handler = await captureReconcile();
    const stored = storageSnapshot();
    unmountCommandTests();
    await expectRefusal(item, handler, stored);
  });

  it("only explicitly reconciles after GET-only recovery, reusing the original ID/key", async () => {
    const item = mountOperation();
    const handler = await captureReconcile();
    const stored = storageSnapshot();
    item.setRead(async () => new Response("unavailable", { status: 503 }));
    commandButton(item.root, "刷新状态").click();
    await settleUI();
    await expectRefusal(item, handler, stored);
    item.setRead(async () => json(operation()));
    commandButton(item.root, "重试").click();
    await settleUI();
    await expectRefusalWithoutClick(item, stored);
    commandButton(item.root, "对账未知结果").click();
    await settleUI();
    expect(item.client.reconcileOperation).toHaveBeenCalledExactlyOnceWith(
      testReceiptID,
      originalKey,
    );
    expect(
      item.requests.filter((request) => request.method === "POST"),
    ).toEqual([
      { path: `${operationPath}/reconcile`, method: "POST", key: originalKey },
    ]);
    expect(item.root.textContent).toContain("original-result-marker");
    expect(item.changed).toHaveBeenCalledTimes(1);
    expect(
      loadOperationReconcileKey(sessionStorage, testReceiptID),
    ).toBeUndefined();
    expect(sessionStorage.getItem("unrelated-recovery-pointer")).toBe(
      testOtherAccountID,
    );
    for (const write of item.otherWrites) expect(write).not.toHaveBeenCalled();
  });

  it("creates a key only for an explicit readable unknown action and retains it on uncertainty", async () => {
    const item = mountOperation(false);
    await captureReconcile();
    const stored = storageSnapshot();
    await expectRefusalWithoutClick(item, stored);
    item.setReconcile(async () => {
      throw new TypeError("lost reconciliation response");
    });
    commandButton(item.root, "对账未知结果").click();
    await settleUI();
    const key = item.reconcileWrite.mock.calls[0]?.[1];
    expect(key).toMatch(/^[0-9a-f-]{36}$/);
    expect(loadOperationReconcileKey(sessionStorage, testReceiptID)).toBe(key);
    const uncertain = storageSnapshot();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(item.client.reconcileOperation).toHaveBeenCalledTimes(1);
    expect(storageSnapshot()).toEqual(uncertain);
    expect(item.changed).not.toHaveBeenCalled();
    item.setReconcile(async () => json(operation()));
    commandButton(item.root, "对账未知结果").click();
    await settleUI();
    expect(item.client.reconcileOperation).toHaveBeenNthCalledWith(
      2,
      testReceiptID,
      key,
    );
    expect(
      loadOperationReconcileKey(sessionStorage, testReceiptID),
    ).toBeUndefined();
    expect(item.changed).toHaveBeenCalledTimes(1);
    expect(storageSnapshot()).toEqual(stored);
  });

  it("keeps one in-flight POST and the original key after a lost response; no automatic retry", async () => {
    const item = mountOperation();
    const handler = await captureReconcile();
    const stored = storageSnapshot();
    let reject!: (reason: Error) => void;
    item.setReconcile(
      () =>
        new Promise<Response>((_resolve, failed) => {
          reject = failed;
        }),
    );
    handler();
    handler();
    await settleUI();
    expect(item.client.reconcileOperation).toHaveBeenCalledExactlyOnceWith(
      testReceiptID,
      originalKey,
    );
    expect(storageSnapshot()).toEqual(stored);
    expect(commandButton(item.root, "对账未知结果").disabled).toBe(true);
    reject(new TypeError("lost reconciliation response"));
    await settleUI();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(item.client.reconcileOperation).toHaveBeenCalledTimes(1);
    expect(item.changed).not.toHaveBeenCalled();
    expect(storageSnapshot()).toEqual(stored);
    item.setReconcile(async () => json(operation()));
    commandButton(item.root, "对账未知结果").click();
    await settleUI();
    expect(item.client.reconcileOperation).toHaveBeenNthCalledWith(
      2,
      testReceiptID,
      originalKey,
    );
    expect(item.changed).toHaveBeenCalledTimes(1);
  });
});

async function expectRefusalWithoutClick(
  item: MountedOperation,
  stored: ReturnType<typeof storageSnapshot>,
): Promise<void> {
  for (const write of item.writes) expect(write).not.toHaveBeenCalled();
  expect(item.changed).not.toHaveBeenCalled();
  expect(storageSnapshot()).toEqual(stored);
  expect(
    item.requests.every(
      (request) => request.method === "GET" && request.key === null,
    ),
  ).toBe(true);
}
