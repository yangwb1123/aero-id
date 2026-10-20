import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import OperationDetails from "./OperationDetails.vue";
import {
  commandButton,
  mountCommandTest,
  testAccountID,
  testReceiptID,
  unmountCommandTests,
} from "./commandTestFixtures";
import { saveOperationReconcileKey } from "./operationRequestKeys";

vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    ...(await import("./commandTestFixtures")).irisCommandTestStubs(),
    IrisEmptyState: defineComponent({
      props: { title: String, description: String },
      setup: (props) => () =>
        h("div", { class: "test-empty", title: props.title }, [
          props.title,
          props.description,
        ]),
    }),
    IrisTimeline: defineComponent({
      props: ["items"],
      setup: (props) => () =>
        h(
          "ol",
          { class: "test-audit-timeline" },
          props.items.map((item: { key: string; title: string }) =>
            h("li", { key: item.key }, item.title),
          ),
        ),
    }),
  };
});
beforeEach(() => {
  sessionStorage.clear();
  vi.useFakeTimers();
});
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
  vi.useRealTimers();
});
const operationPath = `/v1/operations/${testReceiptID}`;
const auditPath = `/v1/audit/operations/${testReceiptID}`;
const originalKey = "01800000-0000-7000-8000-000000000004";
const originalResult = "original-operation-result";
function json(data: JsonRecord, headers?: HeadersInit): Response {
  return new Response(JSON.stringify({ data }), { headers });
}
function auditPage(actions: string[] = [], cursor?: string): Response {
  return json({
    operation_id: testReceiptID,
    events: actions.map((action, index) => ({
      id: `${action}-${index}`,
      operation_id: testReceiptID,
      action,
      source: "aero-id",
      occurred_at: "2026-10-15T20:00:00Z",
    })),
    next_cursor: cursor,
  });
}
function unavailable(): Response {
  return new Response(
    JSON.stringify({
      error: { code: "audit.list_failed", message: "审计读取暂不可用" },
    }),
    { status: 503 },
  );
}
function deferredResponse() {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((finish) => (resolve = finish));
  return { promise, resolve };
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
function mountOperation(read: () => Promise<Response>) {
  saveOperationReconcileKey(sessionStorage, testReceiptID, originalKey);
  const stored = storageSnapshot();
  const requests: Array<{
    path: string;
    method?: string;
    cursor: string | null;
    limit: string | null;
    signal?: AbortSignal | null;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      requests.push({
        path: url.pathname,
        method: init?.method,
        cursor: url.searchParams.get("cursor"),
        limit: url.searchParams.get("limit"),
        signal: init?.signal,
      });
      if (url.pathname === auditPath && init?.method === "GET") return read();
      if (url.pathname === operationPath && init?.method === "GET")
        return json({
          operation_id: testReceiptID,
          account_id: testAccountID,
          tenant_id: "tenant-a",
          actor_uid: "original-initiator",
          home_region: "local",
          operation_type: "sync",
          status: "unknown",
          result: { original_receipt: originalResult },
          steps: [],
        });
      throw new Error("unexpected request in Operation audit-read regression");
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
    vi.spyOn(client.commands, "abandon"),
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
    requests,
    stored,
    writes,
    auditRequests: () =>
      requests.filter((request) => request.path === auditPath),
    setRead: (value: () => Promise<Response>) => {
      read = value;
    },
  };
}
type MountedOperation = ReturnType<typeof mountOperation>;
function expectReadOnly(item: MountedOperation): void {
  expect(storageSnapshot()).toEqual(item.stored);
  expect(item.requests.every((request) => request.method === "GET")).toBe(true);
  expect(
    item.requests.filter((request) => request.path === operationPath),
  ).toHaveLength(1);
  expect(item.root.textContent).toContain(originalResult);
  expect(item.root.textContent).toContain("unknown");
  for (const write of item.writes) expect(write).not.toHaveBeenCalled();
  for (const request of item.auditRequests()) {
    expect(request.limit).toBe("50");
    expect(request.signal).toBeInstanceOf(AbortSignal);
  }
}
function expectNoEmpty(item: MountedOperation): void {
  expect(item.root.querySelector('.test-empty[title*="审计"]')).toBeNull();
  expect(item.root.textContent).not.toContain("暂无审计事实");
}
function expectReadError(item: MountedOperation, message: string): void {
  expect(item.root.querySelector('[title="读取失败"]')).not.toBeNull();
  expect(item.root.textContent).toContain(message);
  expect(item.root.querySelector(".page-loading")).toBeNull();
  expectNoEmpty(item);
  expect(item.root.textContent).not.toContain("rejected-audit-content-marker");
}
const invalidAuditPages = [
  {
    name: "wrong root Operation ID",
    data: {
      operation_id: "01800000-0000-7000-8000-000000000099",
      events: [{ action: "rejected-audit-content-marker" }],
      next_cursor: "must-not-adopt",
    },
  },
  {
    name: "missing events",
    data: { operation_id: testReceiptID, next_cursor: "must-not-adopt" },
  },
  {
    name: "invalid entry among facts",
    data: {
      operation_id: testReceiptID,
      events: [{ action: "rejected-audit-content-marker" }, null],
    },
  },
  {
    name: "invalid cursor type",
    data: {
      operation_id: testReceiptID,
      events: [{ action: "rejected-audit-content-marker" }],
      next_cursor: { id: "must-not-adopt" },
    },
  },
];
const failures = [
  ...invalidAuditPages.map(({ name, data }) => ({
    name,
    read: async () => json(data),
    message: "审计响应无效",
  })),
  {
    name: "current read denial",
    read: async () =>
      new Response(
        JSON.stringify({
          error: { code: "permission.denied", message: "当前读取被拒绝" },
        }),
        { status: 403 },
      ),
    message: "当前读取被拒绝",
  },
  {
    name: "audit dependency failure",
    read: async () => unavailable(),
    message: "审计读取暂不可用",
  },
  {
    name: "not found",
    read: async () =>
      new Response(
        JSON.stringify({
          error: { code: "audit.not_found", message: "未找到可读取记录" },
        }),
        { status: 404 },
      ),
    message: "未找到可读取记录",
  },
  {
    name: "network failure",
    read: async () => {
      throw new TypeError("controlled fetch failure");
    },
    message: "无法连接 aero-id",
  },
  {
    name: "invalid JSON",
    read: async () => new Response("{"),
    message: "aero-id 返回了无效响应",
  },
  {
    name: "stale regional response",
    read: async () => auditPageWithStaleHeader(),
    message: "当前区域无法确认最近一次写入已同步",
  },
];
function auditPageWithStaleHeader(): Response {
  return json(
    { events: [], operation_id: testReceiptID },
    { "X-Aero-Stale-Read": "true" },
  );
}

describe("Operation audit first-page reading is not an empty timeline", () => {
  it("shows pending until a successful empty query, without automatically retrying", async () => {
    const pending = deferredResponse();
    const item = mountOperation(() => pending.promise);
    await settleUI();
    expect(item.root.querySelector(".page-loading")).not.toBeNull();
    expectNoEmpty(item);
    expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
    expectReadOnly(item);
    expect(item.auditRequests()).toHaveLength(1);

    pending.resolve(auditPage());
    await settleUI();
    expect(item.root.querySelector(".page-loading")).toBeNull();
    const empty = item.root.querySelector('.test-empty[title*="审计"]');
    expect(empty?.textContent).toContain("本次查询");
    expect(empty?.textContent).toContain("不证明");
    await vi.advanceTimersByTimeAsync(60_000);
    expect(item.auditRequests()).toHaveLength(1);
    expectReadOnly(item);
  });

  it.each(failures)(
    "shows $name and only explicitly retries the original first-page GET",
    async ({ read, message }) => {
      const item = mountOperation(read);
      await settleUI();
      expectReadError(item, message);
      expectReadOnly(item);
      expect(item.auditRequests()).toHaveLength(1);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.auditRequests()).toHaveLength(1);

      const pending = deferredResponse();
      item.setRead(() => pending.promise);
      commandButton(item.root, "重试").click();
      await settleUI();
      expect(item.root.querySelector(".page-loading")).not.toBeNull();
      expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
      expectNoEmpty(item);
      expectReadOnly(item);
      expect(item.auditRequests()).toHaveLength(2);
      expect(item.auditRequests().map((request) => request.cursor)).toEqual([
        null,
        null,
      ]);

      pending.resolve(auditPage(["operation.execution.blocked"]));
      await settleUI();
      expect(item.root.querySelector(".page-loading")).toBeNull();
      expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
      expectNoEmpty(item);
      expect(item.root.querySelector(".test-audit-timeline")?.textContent).toBe(
        "operation.execution.blocked",
      );
      expectReadOnly(item);
    },
  );

  it("keeps a failed explicit retry as a read error until an eventual successful empty response", async () => {
    const item = mountOperation(async () => unavailable());
    await settleUI();
    expectReadError(item, "审计读取暂不可用");
    commandButton(item.root, "重试").click();
    await settleUI();
    expectReadError(item, "审计读取暂不可用");
    expect(item.auditRequests()).toHaveLength(2);
    item.setRead(async () => auditPage());
    commandButton(item.root, "重试").click();
    await settleUI();
    expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
    expect(
      item.root.querySelector('.test-empty[title*="审计"]'),
    ).not.toBeNull();
    expect(item.auditRequests()).toHaveLength(3);
    expectReadOnly(item);
  });

  it("retains earlier visible facts and the exact cursor across a later-page error and explicit retry", async () => {
    const cursor = "01800000-0000-7000-8000-000000000050";
    const item = mountOperation(async () =>
      auditPage(["operation.started"], cursor),
    );
    await settleUI();
    expect(item.root.querySelector(".test-audit-timeline")?.textContent).toBe(
      "operation.started",
    );
    item.setRead(async () => unavailable());
    commandButton(item.root, "加载更早事实").click();
    await settleUI();
    expectReadError(item, "审计读取暂不可用");
    expect(item.root.querySelector(".test-audit-timeline")?.textContent).toBe(
      "operation.started",
    );
    await vi.advanceTimersByTimeAsync(60_000);
    expect(item.auditRequests()).toHaveLength(2);
    item.setRead(async () => auditPage(["operation.requested"]));
    commandButton(item.root, "重试").click();
    await settleUI();
    expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
    expect(item.root.querySelectorAll(".test-audit-timeline li")).toHaveLength(
      2,
    );
    expect(item.root.querySelector(".test-audit-timeline")?.textContent).toBe(
      "operation.startedoperation.requested",
    );
    expect(item.auditRequests().map((request) => request.cursor)).toEqual([
      null,
      cursor,
      cursor,
    ]);
    expect(
      [...item.root.querySelectorAll("button")].some((button) =>
        button.textContent?.includes("加载更早事实"),
      ),
    ).toBe(false);
    expectReadOnly(item);
  });

  it.each(invalidAuditPages)(
    "does not merge $name on a later page or consume the original cursor",
    async ({ data }) => {
      const cursor = "opaque-original-cursor";
      const item = mountOperation(async () =>
        auditPage(["operation.started"], cursor),
      );
      await settleUI();
      item.setRead(async () => json(data));
      commandButton(item.root, "加载更早事实").click();
      await settleUI();
      expectReadError(item, "审计响应无效");
      expect(item.root.querySelector(".test-audit-timeline")?.textContent).toBe(
        "operation.started",
      );
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.auditRequests()).toHaveLength(2);
      item.setRead(async () => auditPage(["operation.requested"]));
      commandButton(item.root, "重试").click();
      await settleUI();
      expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
      expect(item.root.querySelector(".test-audit-timeline")?.textContent).toBe(
        "operation.startedoperation.requested",
      );
      expect(item.auditRequests().map((request) => request.cursor)).toEqual([
        null,
        cursor,
        cursor,
      ]);
      expectReadOnly(item);
    },
  );

  it.each(["initial", "retry", "later page"])(
    "aborts the %s GET on unmount without adopting late facts or changing original keys",
    async (phase) => {
      const pending = deferredResponse();
      const item = mountOperation(async () => {
        if (phase === "initial") return pending.promise;
        return phase === "retry"
          ? unavailable()
          : auditPage(["operation.started"], "cursor-original");
      });
      await settleUI();
      if (phase !== "initial") {
        item.setRead(() => pending.promise);
        commandButton(
          item.root,
          phase === "retry" ? "重试" : "加载更早事实",
        ).click();
        await settleUI();
      }
      const requests = item.auditRequests();
      const signal = requests.at(-1)?.signal;
      expect(signal?.aborted).toBe(false);
      unmountCommandTests();
      expect(signal?.aborted).toBe(true);
      pending.resolve(auditPage(["late-fact-must-not-be-adopted"]));
      await settleUI();
      expect(item.root.textContent).toBe("");
      expect(storageSnapshot()).toEqual(item.stored);
      expect(item.auditRequests()).toHaveLength(requests.length);
      expect(requests).toHaveLength(phase === "initial" ? 1 : 2);
      expect(item.requests.every((request) => request.method === "GET")).toBe(
        true,
      );
      for (const write of item.writes) expect(write).not.toHaveBeenCalled();
    },
  );
});
