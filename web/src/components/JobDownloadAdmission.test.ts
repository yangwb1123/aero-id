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
import { saveSyncJobReconcileKey } from "./operationRequestKeys";

let retainedDownload: (() => void) | undefined;
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
          if (children?.some((child) => child.children === "确认并下载"))
            retainedDownload = click;
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
  retainedDownload = undefined;
  sessionStorage.clear();
  vi.useFakeTimers();
  // Keep the unchanged fixture expiry independent of the host's current date.
  vi.setSystemTime(new Date("2026-10-16T00:00:00Z"));
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = vi.fn((_blob: Blob) => "blob:test-download");
      static revokeObjectURL = vi.fn((_url: string) => undefined);
    },
  );
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
});
afterEach(async () => {
  unmountCommandTests();
  await settleUI();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
const kinds = ["export", "audit_export"] as const;
type Kind = (typeof kinds)[number];
const jobPath = `/v1/sync/jobs/${testReceiptID}`;
function job(kind: Kind, patch: JsonRecord = {}): JsonRecord {
  return {
    job_id: testReceiptID,
    account_id: testAccountID,
    actor_uid: "original-initiator",
    tenant_id: "tenant-a",
    job_type: kind,
    status: "completed",
    request: { datasets: ["aero-vault.usage"] },
    result: {
      digest: "original-digest-marker",
      expires_at: "2026-10-17T00:00:00Z",
    },
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
function heldResponse(stage: "fetch" | "body") {
  let finish!: (response: Response) => void;
  const pending = new Promise<Response>((resolve) => {
    finish = resolve;
  });
  let body: ReadableStreamDefaultController<Uint8Array>;
  return {
    read: async () =>
      stage === "fetch"
        ? pending
        : new Response(
            new ReadableStream<Uint8Array>({
              start: (value) => {
                body = value;
              },
            }),
          ),
    release(text: string): void {
      if (stage === "fetch") finish(new Response(text));
      else {
        body.enqueue(new TextEncoder().encode(text));
        body.close();
      }
    },
  };
}
function mountJob(kind: Kind) {
  saveSyncJobReconcileKey(
    sessionStorage,
    testReceiptID,
    "original-reconcile-key",
  );
  sessionStorage.setItem("unrelated-recovery-pointer", testOtherAccountID);
  let read = async () => json(job(kind));
  let download = async () =>
    new Response('{"schema_version":1}', {
      headers: { "Content-Type": "application/json" },
    });
  const requests: Array<{
    path: string;
    query: string;
    method?: string;
    key: string | null;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      requests.push({
        path: url.pathname,
        query: url.search,
        method: init?.method,
        key: new Headers(init?.headers).get("Idempotency-Key"),
      });
      if (url.pathname === jobPath && init?.method === "GET") return read();
      if (
        url.pathname === `${jobPath}/export/download` &&
        init?.method === "GET"
      )
        return download();
      throw new Error(
        "unexpected request in one-time download admission regression",
      );
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher as typeof fetch,
  );
  const downloadRead = vi.spyOn(client, "downloadExport");
  const writes = [
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileOperation"),
    vi.spyOn(client, "reconcileSyncJob"),
    vi.spyOn(client, "createExportJob"),
  ];
  const changed = vi.fn();
  return {
    ...mountCommandTest(JobDetails, {
      client,
      jobId: testReceiptID,
      onChanged: changed,
    }),
    requests,
    downloadRead,
    writes,
    changed,
    setRead: (value: () => Promise<Response>) => {
      read = value;
    },
    setDownload: (value: () => Promise<Response>) => {
      download = value;
    },
  };
}
type MountedJob = ReturnType<typeof mountJob>;
async function captureDownload(item: MountedJob): Promise<() => void> {
  await settleUI();
  commandButton(item.root, "一次性下载").click();
  await settleUI();
  expect(commandButton(item.root, "确认并下载").disabled).toBe(false);
  expect(retainedDownload).toBeTypeOf("function");
  expect(item.downloadRead).not.toHaveBeenCalled();
  return retainedDownload!;
}
function expectUnchanged(
  item: MountedJob,
  stored: ReturnType<typeof storageSnapshot>,
): void {
  expect(storageSnapshot()).toEqual(stored);
  expect(item.changed).not.toHaveBeenCalled();
  for (const write of item.writes) expect(write).not.toHaveBeenCalled();
  expect(
    item.requests.every(
      (request) => request.method === "GET" && request.key === null,
    ),
  ).toBe(true);
}
function expectNoDownload(
  item: MountedJob,
  stored: ReturnType<typeof storageSnapshot>,
): void {
  expectUnchanged(item, stored);
  expect(item.downloadRead).not.toHaveBeenCalled();
  expect(item.requests.every((request) => request.path === jobPath)).toBe(true);
  expect(URL.createObjectURL).not.toHaveBeenCalled();
  expect(HTMLAnchorElement.prototype.click).not.toHaveBeenCalled();
}
async function expectRefusal(
  item: MountedJob,
  handler: () => void,
  stored: ReturnType<typeof storageSnapshot>,
): Promise<void> {
  // Invoke the original final-confirmation listener, even after its DOM disappears.
  handler();
  await settleUI();
  expectNoDownload(item, stored);
}

describe.each(kinds)(
  "%s one-time download requires a successful mounted detail read",
  (kind) => {
    it.each([
      "denied",
      "unavailable",
      "wrong-id",
      "stale",
      "invalid-json",
      "network",
      "bad-status",
    ])(
      "does not consume using retained completed metadata after a %s read",
      async (failure) => {
        const item = mountJob(kind);
        const handler = await captureDownload(item);
        const stored = storageSnapshot();
        item.setRead(async () => {
          if (failure === "network")
            throw new TypeError("lost metadata response");
          if (failure === "invalid-json") return new Response("{");
          if (failure === "wrong-id")
            return json(job(kind, { job_id: testOtherAccountID }));
          if (failure === "stale")
            return json(job(kind), { "X-Aero-Stale-Read": "true" });
          if (failure === "bad-status")
            return json(job(kind, { status: ["completed"] }));
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
        expect(item.root.textContent).not.toContain("original-digest-marker");
        await expectRefusal(item, handler, stored);
        await vi.advanceTimersByTimeAsync(60_000);
        expect(item.requests).toHaveLength(2);
      },
    );

    it.each(["fetch", "body"] as const)(
      "refuses during foreground %s, then only downloads on explicit confirmation",
      async (stage) => {
        const item = mountJob(kind);
        const handler = await captureDownload(item);
        const stored = storageSnapshot();
        const held = heldResponse(stage);
        item.setRead(held.read);
        commandButton(item.root, "刷新状态").click();
        await settleUI();
        expect(item.root.textContent).toContain("正在读取平台数据");
        await expectRefusal(item, handler, stored);
        held.release(JSON.stringify({ data: job(kind) }));
        await settleUI();
        expect(commandButton(item.root, "确认并下载").disabled).toBe(false);
        expect(item.root.textContent).toContain("original-digest-marker");
        await vi.advanceTimersByTimeAsync(60_000);
        expectNoDownload(item, stored);
        commandButton(item.root, "确认并下载").click();
        await settleUI();
        expect(item.downloadRead).toHaveBeenCalledExactlyOnceWith(
          testReceiptID,
          true,
        );
        expect(item.requests.at(-1)).toEqual({
          path: `${jobPath}/export/download`,
          query: "?one_time=true",
          method: "GET",
          key: null,
        });
        expect(URL.createObjectURL).toHaveBeenCalledTimes(1);
        expect(HTMLAnchorElement.prototype.click).toHaveBeenCalledTimes(1);
        expectUnchanged(item, stored);
        await vi.advanceTimersByTimeAsync(1000);
        expect(URL.revokeObjectURL).toHaveBeenCalledExactlyOnceWith(
          "blob:test-download",
        );
      },
    );

    it.each(["pending", "running", "unknown", "partially_completed", "failed"])(
      "still refuses a newly observed %s",
      async (status) => {
        const item = mountJob(kind);
        const handler = await captureDownload(item);
        const stored = storageSnapshot();
        item.setRead(async () => json(job(kind, { status })));
        commandButton(item.root, "刷新状态").click();
        await settleUI();
        expect(commandButton(item.root, "确认并下载").disabled).toBe(true);
        await expectRefusal(item, handler, stored);
      },
    );

    it.each(["report", "scope", "tenant"])(
      "continues to fail closed on changed %s guidance",
      async (failure) => {
        const item = mountJob(kind);
        const handler = await captureDownload(item);
        const stored = storageSnapshot();
        if (failure === "report") item.report.value = undefined;
        if (failure === "scope") item.scope.value = undefined;
        if (failure === "tenant") item.report.value!.tenant_id = "other-tenant";
        await settleUI();
        expect(commandButton(item.root, "确认并下载").disabled).toBe(true);
        await expectRefusal(item, handler, stored);
      },
    );

    it("refuses a new download through the old listener after unmount", async () => {
      const item = mountJob(kind);
      const handler = await captureDownload(item);
      const stored = storageSnapshot();
      unmountCommandTests();
      await expectRefusal(item, handler, stored);
    });

    it.each([-1, 0, 1])(
      "checks the unchanged artifact expiry at offset %i ms on final confirmation",
      async (offset) => {
        const expiry = Date.parse("2026-10-17T00:00:00Z");
        vi.setSystemTime(expiry - 1000);
        const item = mountJob(kind);
        const handler = await captureDownload(item);
        const stored = storageSnapshot();
        // Jump without a timer tick: the retained handler must check Date.now().
        vi.setSystemTime(expiry + offset);
        handler();
        await settleUI();
        expect(Date.now()).toBe(expiry + offset);
        if (offset < 0) {
          expect(item.downloadRead).toHaveBeenCalledExactlyOnceWith(
            testReceiptID,
            true,
          );
          expect(URL.createObjectURL).toHaveBeenCalledTimes(1);
          expect(HTMLAnchorElement.prototype.click).toHaveBeenCalledTimes(1);
          expectUnchanged(item, stored);
        } else {
          expectNoDownload(item, stored);
          await vi.advanceTimersByTimeAsync(1000);
          expect(commandButton(item.root, "确认并下载").disabled).toBe(true);
          expect(
            item.root.querySelector('[title="制品已到期"]'),
          ).not.toBeNull();
          expect(item.root.textContent).toContain("刷新不会续期");
          commandButton(item.root, "刷新状态").click();
          await settleUI();
          await expectRefusal(item, handler, stored);
          expect(item.requests).toHaveLength(2);
          expect(
            item.root.querySelector('[title="制品已到期"]'),
          ).not.toBeNull();
        }
      },
    );

    it("GET-only recovery of completed metadata never automatically consumes the artifact", async () => {
      const item = mountJob(kind);
      await captureDownload(item);
      const stored = storageSnapshot();
      item.setRead(async () => new Response("unavailable", { status: 503 }));
      commandButton(item.root, "刷新状态").click();
      await settleUI();
      item.setRead(async () => json(job(kind)));
      commandButton(item.root, "重试").click();
      await settleUI();
      expect(commandButton(item.root, "确认并下载").disabled).toBe(false);
      expect(item.root.textContent).toContain("original-digest-marker");
      await vi.advanceTimersByTimeAsync(60_000);
      expectNoDownload(item, stored);
      expect(item.requests).toHaveLength(3);
    });

    it.each(["fetch", "body"] as const)(
      "keeps one admitted download while %s is pending",
      async (stage) => {
        const item = mountJob(kind);
        const handler = await captureDownload(item);
        const stored = storageSnapshot();
        const held = heldResponse(stage);
        item.setDownload(held.read);
        handler();
        handler();
        await settleUI();
        expect(commandButton(item.root, "确认并下载").disabled).toBe(true);
        expect(item.downloadRead).toHaveBeenCalledExactlyOnceWith(
          testReceiptID,
          true,
        );
        held.release('{"schema_version":1}');
        await settleUI();
        expect(URL.createObjectURL).toHaveBeenCalledTimes(1);
        expectUnchanged(item, stored);
      },
    );

    it("keeps uncertainty after a lost admitted response without automatic download or new export", async () => {
      const item = mountJob(kind);
      const handler = await captureDownload(item);
      const stored = storageSnapshot();
      item.setDownload(async () => {
        throw new TypeError("lost one-time response");
      });
      handler();
      await settleUI();
      expect(item.root.textContent).toContain("无法连接 aero-id");
      expect(item.root.textContent).toContain(
        "一次性指针消费与读取审计先于响应提交",
      );
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.downloadRead).toHaveBeenCalledExactlyOnceWith(
        testReceiptID,
        true,
      );
      expect(URL.createObjectURL).not.toHaveBeenCalled();
      expectUnchanged(item, stored);
    });

    it("does not save a previously admitted late response into an unmounted view", async () => {
      const item = mountJob(kind);
      const handler = await captureDownload(item);
      const stored = storageSnapshot();
      const held = heldResponse("fetch");
      item.setDownload(held.read);
      handler();
      await settleUI();
      expect(item.downloadRead).toHaveBeenCalledExactlyOnceWith(
        testReceiptID,
        true,
      );
      unmountCommandTests();
      held.release('{"schema_version":1}');
      await settleUI();
      expect(URL.createObjectURL).not.toHaveBeenCalled();
      expect(HTMLAnchorElement.prototype.click).not.toHaveBeenCalled();
      expectUnchanged(item, stored);
    });
  },
);

it.each([
  ["export", "account.export"],
  ["audit_export", "audit.export"],
  ["audit_export", "audit.read"],
] as const)(
  "still requires current %s/%s at final confirmation",
  async (kind, permission) => {
    const item = mountJob(kind);
    const handler = await captureDownload(item);
    const stored = storageSnapshot();
    item.report.value!.capabilities.find(
      (feature) => feature.name === permission,
    )!.authorization = "denied";
    await settleUI();
    expect(commandButton(item.root, "确认并下载").disabled).toBe(true);
    await expectRefusal(item, handler, stored);
  },
);
