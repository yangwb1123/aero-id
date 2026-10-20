import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import JobsPage from "./JobsPage.vue";
import OperationsPage from "./OperationsPage.vue";
import {
  commandButton,
  mountCommandTest,
  testAccountID,
  testDatasets,
  testReceiptID,
  unmountCommandTests,
} from "../components/commandTestFixtures";
import {
  saveOperationReconcileKey,
  saveSyncJobReconcileKey,
} from "../components/operationRequestKeys";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("../components/commandTestFixtures")).irisCommandTestStubs(),
);
vi.mock("../components/JobDetails.vue", () => ({
  default: {
    emits: ["changed"],
    template: "<button @click=\"$emit('changed')\">列表更新事件</button>",
  },
}));
vi.mock("../components/OperationDetails.vue", () => ({
  default: {
    emits: ["changed"],
    template: "<button @click=\"$emit('changed')\">列表更新事件</button>",
  },
}));
vi.mock("../components/OperationCreateDialog.vue", () => ({
  default: { template: "<div />" },
}));

const cleanup: Array<() => void> = [];
beforeEach(() => {
  sessionStorage.clear();
  vi.useFakeTimers();
});
afterEach(async () => {
  unmountCommandTests();
  cleanup.splice(0).forEach((release) => release());
  await settleUI();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const families = [
  { kind: "sync", path: "/sync/jobs", field: "jobs", id: "job_id" },
  {
    kind: "operation",
    path: "/operations",
    field: "operations",
    id: "operation_id",
  },
] as const;
type Family = (typeof families)[number];
type Read = (signal: AbortSignal) => Promise<Response>;
const cursor = "01800000-0000-7000-8000-000000000090";
const nextID = "01800000-0000-7000-8000-000000000080";
const refreshedID = "01800000-0000-7000-8000-000000000099";

function json(payload: JsonRecord, status = 200, headers = {}): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}
function page(family: Family, id: string, nextCursor?: string): JsonRecord {
  return {
    data: {
      [family.field]: [{ [family.id]: id, status: "unknown" }],
      next_cursor: nextCursor,
    },
  };
}
function heldRead(stage: "fetch" | "body", ignoreAbort = false) {
  let resolve!: (response: Response) => void;
  let reject!: (reason: Error) => void;
  const pending = new Promise<Response>((done, failed) => {
    resolve = done;
    reject = failed;
  });
  let stream: ReadableStreamDefaultController<Uint8Array> | undefined;
  let settled = false;
  const read: Read = async (signal) => {
    const response =
      stage === "body"
        ? new Response(
            new ReadableStream<Uint8Array>({
              start: (value) => {
                stream = value;
              },
            }),
          )
        : undefined;
    signal.addEventListener(
      "abort",
      () => {
        if (settled || ignoreAbort) return;
        settled = true;
        const reason = new DOMException("Read aborted", "AbortError");
        if (stream) stream.error(reason);
        else reject(reason);
      },
      { once: true },
    );
    return response ?? pending;
  };
  const release = (payload: JsonRecord) => {
    if (settled) return;
    settled = true;
    if (stream) {
      stream.enqueue(new TextEncoder().encode(JSON.stringify(payload)));
      stream.close();
    } else resolve(json(payload));
  };
  cleanup.push(() => release({ data: {} }));
  return { read, release };
}
function storedKeys(): Record<string, string | null> {
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
function mountList(family: Family, initialPage?: JsonRecord) {
  saveSyncJobReconcileKey(sessionStorage, testReceiptID, "original-sync-key");
  saveOperationReconcileKey(
    sessionStorage,
    testReceiptID,
    "original-operation-key",
  );
  const queue: Read[] = [];
  const requests: Array<{
    url: URL;
    method?: string;
    key: string | null;
    signal: AbortSignal;
  }> = [];
  let firstPages = 0;
  const path = `/v1${family.path}`;
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      if (!init?.signal) throw new Error("missing transport signal");
      const url = new URL(String(input));
      requests.push({
        url,
        method: init.method,
        key: new Headers(init.headers).get("Idempotency-Key"),
        signal: init.signal,
      });
      if (init.method !== "GET") throw new Error("unexpected list write");
      if (url.pathname === "/v1/account-datasets")
        return json({ data: { datasets: testDatasets } });
      if (url.pathname === "/v1/me")
        return json({ data: { account: { account_id: testAccountID } } });
      if (url.pathname !== path) throw new Error("unexpected list path");
      if (!url.searchParams.has("cursor")) {
        firstPages += 1;
        return json(
          firstPages === 1 && initialPage
            ? initialPage
            : page(family, firstPages === 1 ? cursor : refreshedID, cursor),
        );
      }
      const read = queue.shift();
      if (!read) throw new Error("unexpected automatic continuation");
      return read(init.signal);
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
    vi.spyOn(client, "createSyncJob"),
    vi.spyOn(client, "createExportJob"),
    vi.spyOn(client, "createOperation"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileOperation"),
    vi.spyOn(client, "reconcileSyncJob"),
    vi.spyOn(client, "downloadExport"),
  ];
  const mounted = mountCommandTest(
    family.kind === "sync" ? JobsPage : OperationsPage,
    {
      client,
      ...(family.kind === "sync"
        ? { jobId: testReceiptID }
        : { operationId: testReceiptID }),
    },
  );
  return {
    ...mounted,
    writes,
    requests,
    enqueue: (read: Read) => queue.push(read),
    reads: () => requests.filter((request) => request.url.pathname === path),
  };
}
function expectReadOnly(
  item: ReturnType<typeof mountList>,
  stored: ReturnType<typeof storedKeys>,
): void {
  expect(storedKeys()).toEqual(stored);
  item.writes.forEach((write) => expect(write).not.toHaveBeenCalled());
  expect(
    item.requests.every(
      (request) => request.method === "GET" && request.key === null,
    ),
  ).toBe(true);
  for (const request of item.reads()) {
    expect([...request.url.searchParams.keys()].sort()).toEqual(
      request.url.searchParams.has("cursor") ? ["cursor", "limit"] : ["limit"],
    );
    expect(request.url.searchParams.get("limit")).toBe("50");
    if (request.url.searchParams.has("cursor"))
      expect(request.url.searchParams.get("cursor")).toBe(cursor);
  }
}
async function startNext(
  item: ReturnType<typeof mountList>,
  read: Read,
): Promise<AbortSignal> {
  await settleUI();
  expect(item.root.textContent).toContain(cursor);
  item.enqueue(read);
  commandButton(item.root, "加载更多").click();
  await settleUI();
  expect(item.reads()).toHaveLength(2);
  return item.reads()[1].signal;
}

describe.each(families)("$kind list continuation lifetime", (family) => {
  const invalidPages = [
    { name: "missing list", data: {} },
    { name: "object list", data: { [family.field]: {} } },
    {
      name: "invalid member after valid row",
      data: {
        [family.field]: [{ [family.id]: nextID, status: "unknown" }, null],
      },
    },
    {
      name: "null cursor",
      data: { [family.field]: [{ [family.id]: nextID }], next_cursor: null },
    },
    {
      name: "numeric cursor",
      data: { [family.field]: [{ [family.id]: nextID }], next_cursor: 123 },
    },
  ];
  it.each(invalidPages)(
    "does not present $name as a successful empty first page",
    async ({ data }) => {
      const item = mountList(family, { data });
      const stored = storedKeys();
      await settleUI();
      expect(item.root.textContent).toContain("列表响应无效");
      expect(item.root.textContent).not.toContain("暂无同步或导出任务");
      expect(item.root.textContent).not.toContain("暂无跨系统操作");
      expect(item.root.querySelector("tbody")).toBeNull();
      await vi.advanceTimersByTimeAsync(20_000);
      expect(item.reads()).toHaveLength(1);
      commandButton(item.root, "重试").click();
      await settleUI();
      expect(item.reads()).toHaveLength(2);
      expect(item.root.textContent).toContain(refreshedID);
      expect(item.root.textContent).not.toContain("列表响应无效");
      expectReadOnly(item, stored);
    },
  );

  it.each(invalidPages)(
    "rejects the whole continuation on $name and retains original rows/cursor for explicit retry",
    async ({ data }) => {
      const item = mountList(family);
      const stored = storedKeys();
      await startNext(item, async () => json({ data }));
      expect(item.root.textContent).toContain("列表响应无效");
      expect(item.root.textContent).toContain(cursor);
      expect(item.root.textContent).not.toContain(nextID);
      expect(item.root.querySelectorAll("tbody tr")).toHaveLength(1);
      await vi.advanceTimersByTimeAsync(20_000);
      expect(item.reads()).toHaveLength(2);
      item.enqueue(async () => json(page(family, nextID)));
      commandButton(item.root, "重试").click();
      await settleUI();
      expect(item.reads()).toHaveLength(3);
      const rows = [...item.root.querySelectorAll("tbody tr")];
      expect(rows).toHaveLength(2);
      expect(rows[0].textContent).toContain(cursor);
      expect(rows[1].textContent).toContain(nextID);
      expect(item.root.textContent).not.toContain("列表响应无效");
      expectReadOnly(item, stored);
    },
  );

  describe.each(["fetch", "body"] as const)("pending %s", (stage) => {
    it.each([false, true])(
      "aborts on unmount (non-cooperating dependency: %s) without late adoption or retries",
      async (ignoreAbort) => {
        const item = mountList(family);
        const stored = storedKeys();
        const pending = heldRead(stage, ignoreAbort);
        const signal = await startNext(item, pending.read);
        expect(signal.aborted).toBe(false);
        item.app.unmount();
        expect(signal.aborted).toBe(true);
        pending.release(page(family, nextID));
        await settleUI();
        await vi.advanceTimersByTimeAsync(20_000);
        expect(item.reads()).toHaveLength(2);
        expect(item.root.textContent).not.toContain(nextID);
        expectReadOnly(item, stored);
      },
    );

    it("aborts a replaced list and refuses the old page even if its dependency ignores cancellation", async () => {
      const item = mountList(family);
      const stored = storedKeys();
      const pending = heldRead(stage, true);
      const signal = await startNext(item, pending.read);
      commandButton(item.root, "列表更新事件").click();
      await settleUI();
      expect(signal.aborted).toBe(true);
      expect(item.reads()).toHaveLength(3);
      expect(item.root.textContent).toContain(refreshedID);
      pending.release(page(family, nextID));
      await settleUI();
      expect(item.root.textContent).not.toContain(nextID);
      expect(item.root.textContent).not.toContain(cursor);
      expect(item.root.textContent).toContain(refreshedID);
      expect(item.reads()).toHaveLength(3);
      expectReadOnly(item, stored);
    });

    it("keeps the original fifteen-second request deadline and retries only on an explicit GET", async () => {
      const item = mountList(family);
      const stored = storedKeys();
      const pending = heldRead(stage);
      const signal = await startNext(item, pending.read);
      await vi.advanceTimersByTimeAsync(14_999);
      expect(signal.aborted).toBe(false);
      expect(item.reads()).toHaveLength(2);
      await vi.advanceTimersByTimeAsync(1);
      await settleUI();
      expect(signal.aborted).toBe(true);
      expect(item.root.textContent).toContain("请求超时");
      expect(item.root.textContent).toContain(cursor);
      await vi.advanceTimersByTimeAsync(20_000);
      expect(item.reads()).toHaveLength(2);
      item.enqueue(async () => json(page(family, nextID)));
      commandButton(item.root, "重试").click();
      await settleUI();
      expect(item.reads()).toHaveLength(3);
      expect(item.root.textContent).toContain(nextID);
      expectReadOnly(item, stored);
    });
  });

  it("uses one in-flight continuation, appends a valid page once and stops at the returned end", async () => {
    const item = mountList(family);
    const stored = storedKeys();
    const pending = heldRead("fetch");
    const signal = await startNext(item, pending.read);
    commandButton(item.root, "加载更多").click();
    commandButton(item.root, "加载更多").click();
    await settleUI();
    expect(item.reads()).toHaveLength(2);
    expect(signal.aborted).toBe(false);
    pending.release(page(family, nextID));
    await settleUI();
    const rows = [...item.root.querySelectorAll("tbody tr")];
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain(cursor);
    expect(rows[1].textContent).toContain(nextID);
    expect(item.root.textContent).not.toContain("加载更多");
    await vi.advanceTimersByTimeAsync(20_000);
    expect(signal.aborted).toBe(false);
    expect(item.reads()).toHaveLength(2);
    expectReadOnly(item, stored);
  });

  it.each([
    {
      name: "denied",
      response: () =>
        json({ error: { code: "auth.forbidden", message: "读取被拒绝" } }, 403),
      message: "读取被拒绝",
    },
    {
      name: "unavailable",
      response: () =>
        json(
          { error: { code: "audit.unavailable", message: "读取暂不可用" } },
          503,
        ),
      message: "读取暂不可用",
    },
    {
      name: "invalid JSON",
      response: () => new Response("not-json"),
      message: "返回了无效响应",
    },
    {
      name: "stale",
      response: () =>
        json(page(family, nextID), 200, { "X-Aero-Stale-Read": "true" }),
      message: "当前区域无法确认",
    },
  ])(
    "retains the original page/cursor on $name and never auto-retries",
    async ({ response, message }) => {
      const item = mountList(family);
      const stored = storedKeys();
      await startNext(item, async () => response());
      expect(item.root.textContent).toContain(message);
      expect(item.root.textContent).toContain(cursor);
      expect(item.root.textContent).not.toContain(nextID);
      await vi.advanceTimersByTimeAsync(20_000);
      expect(item.reads()).toHaveLength(2);
      item.enqueue(async () => json(page(family, nextID)));
      commandButton(item.root, "重试").click();
      await settleUI();
      expect(item.reads()).toHaveLength(3);
      expect(item.root.textContent).toContain(nextID);
      expect(item.root.textContent).not.toContain(message);
      expectReadOnly(item, stored);
    },
  );
});
