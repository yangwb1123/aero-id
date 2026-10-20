import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import JobDetails from "./JobDetails.vue";
import {
  commandButton,
  mountCommandTest,
  testAccountID,
  testOtherAccountID,
  testReceiptID,
  unmountCommandTests,
} from "./commandTestFixtures";
import {
  loadSyncJobReconcileKey,
  saveSyncJobReconcileKey,
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
const jobPath = `/v1/sync/jobs/${testReceiptID}`;
const originalKey = "01800000-0000-7000-8000-000000000004";
function job(patch: JsonRecord = {}): JsonRecord {
  return {
    job_id: testReceiptID,
    account_id: testAccountID,
    actor_uid: "original-initiator",
    tenant_id: "tenant-a",
    job_type: "sync",
    status: "unknown",
    request: { datasets: ["aero-im.workspaces"] },
    result: { source_operation_id: "original-result-marker" },
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
function mountJob(withOriginalKey = true) {
  if (withOriginalKey)
    saveSyncJobReconcileKey(sessionStorage, testReceiptID, originalKey);
  sessionStorage.setItem("unrelated-recovery-pointer", testOtherAccountID);
  let read = async () => json(job());
  let reconcile = async () => json(job());
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
      if (path === jobPath && init?.method === "GET") return read();
      if (path === `${jobPath}/reconcile` && init?.method === "POST")
        return reconcile();
      throw new Error(
        "unexpected request in Job Reconcile admission regression",
      );
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher as typeof fetch,
  );
  const reconcileWrite = vi.spyOn(client, "reconcileSyncJob");
  const otherWrites = [
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileOperation"),
    vi.spyOn(client, "downloadExport"),
  ];
  const changed = vi.fn();
  const mounted = mountCommandTest(JobDetails, {
    client,
    jobId: testReceiptID,
    onChanged: changed,
  });
  return {
    ...mounted,
    requests,
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
type MountedJob = ReturnType<typeof mountJob>;
async function captureReconcile(): Promise<() => void> {
  await settleUI();
  expect(retainedReconcile).toBeTypeOf("function");
  return retainedReconcile!;
}
function expectNoWrite(
  item: MountedJob,
  stored: ReturnType<typeof storageSnapshot>,
): void {
  expect(item.reconcileWrite).not.toHaveBeenCalled();
  for (const write of item.otherWrites) expect(write).not.toHaveBeenCalled();
  expect(item.changed).not.toHaveBeenCalled();
  expect(
    item.requests.every(
      (request) => request.method === "GET" && request.key === null,
    ),
  ).toBe(true);
  expect(storageSnapshot()).toEqual(stored);
}
async function expectRefusal(
  item: MountedJob,
  handler: () => void,
  stored: ReturnType<typeof storageSnapshot>,
): Promise<void> {
  // Call the original listener even if its button has been disabled or removed.
  handler();
  await settleUI();
  expectNoWrite(item, stored);
}

describe("Job Reconcile requires a readable, observed unknown in the mounted view", () => {
  it.each([
    "pending",
    "running",
    "completed",
    "partially_completed",
    "failed",
    "requested",
    ["unknown"],
    null,
  ])("refuses a retained handler after GET receives %j", async (status) => {
    const item = mountJob();
    const handler = await captureReconcile();
    const stored = storageSnapshot();
    item.setRead(async () => json(job({ status })));
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
  ])(
    "does not borrow old unknown metadata after a %s read",
    async (failure) => {
      const item = mountJob(false);
      const handler = await captureReconcile();
      const stored = storageSnapshot();
      item.setRead(async () => {
        if (failure === "network") throw new TypeError("lost read response");
        if (failure === "invalid-json") return new Response("{");
        if (failure === "wrong-id")
          return json(job({ job_id: testOtherAccountID }));
        if (failure === "stale")
          return json(job(), { "X-Aero-Stale-Read": "true" });
        return new Response(
          JSON.stringify({
            error: {
              code: "sync.read_unavailable",
              message: "read unavailable",
            },
          }),
          { status: failure === "denied" ? 403 : 503 },
        );
      });
      commandButton(item.root, "刷新状态").click();
      await settleUI();
      expect(item.root.textContent).not.toContain("original-result-marker");
      const random = vi.spyOn(crypto, "randomUUID");
      await expectRefusal(item, handler, stored);
      expect(random).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.requests).toHaveLength(2);
    },
  );

  it.each(["fetch", "body"])(
    "refuses during foreground %s before generating a key; GET-only recovery does not reconcile",
    async (stage) => {
      const item = mountJob(false);
      const handler = await captureReconcile();
      const stored = storageSnapshot();
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
      // The foreground GET already minted its request ID, not a command key.
      const random = vi.spyOn(crypto, "randomUUID");
      await expectRefusal(item, handler, stored);
      expect(random).not.toHaveBeenCalled();
      if (stage === "fetch") finish(json(job()));
      else {
        body.enqueue(new TextEncoder().encode(JSON.stringify({ data: job() })));
        body.close();
      }
      await settleUI();
      expect(commandButton(item.root, "对账未知结果").disabled).toBe(false);
      expect(item.root.textContent).toContain("original-result-marker");
      await vi.advanceTimersByTimeAsync(60_000);
      expectNoWrite(item, stored);
      expect(random).not.toHaveBeenCalled();
      expect(item.requests).toHaveLength(2);
    },
  );

  it.each(["permission", "report", "scope", "tenant", "home"])(
    "still requires current %s guidance in the retained handler",
    async (failure) => {
      const item = mountJob();
      const handler = await captureReconcile();
      const stored = storageSnapshot();
      if (failure === "permission")
        item.report.value!.capabilities.find(
          (feature) => feature.name === "source.sync",
        )!.available = false;
      if (failure === "report") item.report.value = undefined;
      if (failure === "scope") item.scope.value = undefined;
      if (failure === "tenant") item.report.value!.tenant_id = "other-tenant";
      if (failure === "home") item.report.value!.local_region = "other-region";
      await settleUI();
      expect(commandButton(item.root, "对账未知结果").disabled).toBe(true);
      await expectRefusal(item, handler, stored);
    },
  );

  it.each([true, false])(
    "refuses after unmount without changing keys (original key present: %s)",
    async (withOriginalKey) => {
      const item = mountJob(withOriginalKey);
      const handler = await captureReconcile();
      const stored = storageSnapshot();
      unmountCommandTests();
      const random = vi.spyOn(crypto, "randomUUID");
      await expectRefusal(item, handler, stored);
      expect(random).not.toHaveBeenCalled();
    },
  );

  it("only explicitly reconciles after GET-only recovery, using the original ID/key", async () => {
    const item = mountJob();
    const handler = await captureReconcile();
    const stored = storageSnapshot();
    item.setRead(async () => new Response("unavailable", { status: 503 }));
    commandButton(item.root, "刷新状态").click();
    await settleUI();
    await expectRefusal(item, handler, stored);
    item.setRead(async () => json(job()));
    commandButton(item.root, "重试").click();
    await settleUI();
    expectNoWrite(item, stored);
    commandButton(item.root, "对账未知结果").click();
    await settleUI();
    expect(item.reconcileWrite).toHaveBeenCalledExactlyOnceWith(
      testReceiptID,
      originalKey,
    );
    expect(
      item.requests.filter((request) => request.method === "POST"),
    ).toEqual([
      { path: `${jobPath}/reconcile`, method: "POST", key: originalKey },
    ]);
    expect(item.root.textContent).toContain("original-result-marker");
    expect(item.changed).toHaveBeenCalledTimes(1);
    expect(
      loadSyncJobReconcileKey(sessionStorage, testReceiptID),
    ).toBeUndefined();
    expect(sessionStorage.getItem("unrelated-recovery-pointer")).toBe(
      testOtherAccountID,
    );
    for (const write of item.otherWrites) expect(write).not.toHaveBeenCalled();
  });

  it("only mints a command key for an explicit readable unknown and keeps it on uncertainty", async () => {
    const item = mountJob(false);
    await captureReconcile();
    const stored = storageSnapshot();
    expectNoWrite(item, stored);
    item.setReconcile(async () => {
      throw new TypeError("lost reconciliation response");
    });
    commandButton(item.root, "对账未知结果").click();
    await settleUI();
    const key = item.reconcileWrite.mock.calls[0]?.[1];
    expect(key).toMatch(/^[0-9a-f-]{36}$/);
    expect(loadSyncJobReconcileKey(sessionStorage, testReceiptID)).toBe(key);
    const uncertain = storageSnapshot();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(item.reconcileWrite).toHaveBeenCalledTimes(1);
    expect(storageSnapshot()).toEqual(uncertain);
    expect(item.changed).not.toHaveBeenCalled();
    item.setReconcile(async () => json(job()));
    commandButton(item.root, "对账未知结果").click();
    await settleUI();
    expect(item.reconcileWrite).toHaveBeenNthCalledWith(2, testReceiptID, key);
    expect(
      loadSyncJobReconcileKey(sessionStorage, testReceiptID),
    ).toBeUndefined();
    expect(item.changed).toHaveBeenCalledTimes(1);
    expect(storageSnapshot()).toEqual(stored);
    for (const write of item.otherWrites) expect(write).not.toHaveBeenCalled();
  });

  it("keeps a single in-flight POST and the original key after a lost response, without automatic retry", async () => {
    const item = mountJob();
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
    expect(item.reconcileWrite).toHaveBeenCalledExactlyOnceWith(
      testReceiptID,
      originalKey,
    );
    expect(storageSnapshot()).toEqual(stored);
    expect(commandButton(item.root, "对账未知结果").disabled).toBe(true);
    reject(new TypeError("lost reconciliation response"));
    await settleUI();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(item.reconcileWrite).toHaveBeenCalledTimes(1);
    expect(item.changed).not.toHaveBeenCalled();
    expect(storageSnapshot()).toEqual(stored);
    item.setReconcile(async () => json(job()));
    commandButton(item.root, "对账未知结果").click();
    await settleUI();
    expect(item.reconcileWrite).toHaveBeenNthCalledWith(
      2,
      testReceiptID,
      originalKey,
    );
    expect(item.changed).toHaveBeenCalledTimes(1);
    for (const write of item.otherWrites) expect(write).not.toHaveBeenCalled();
  });
});
