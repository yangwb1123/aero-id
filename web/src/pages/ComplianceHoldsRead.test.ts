import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import {
  commandButton,
  mountCommandTest,
  unmountCommandTests,
} from "../components/commandTestFixtures";
import CompliancePage from "./CompliancePage.vue";

vi.mock("@iris-ui-kit/vue", async () => {
  const { irisCommandTestStubs } =
    await import("../components/commandTestFixtures");
  return irisCommandTestStubs();
});

const marker = "untrusted-hold-row-marker";
const empty = "本次查询无可见的生效 Legal Hold";
const row = {
  scope_type: "account",
  scope_id: "original-account",
  reason: marker,
  created_by: "operator",
  created_at: "2026-10-16T01:00:00Z",
  expires_at: null,
};
const cleanups: Array<() => void> = [];
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
function json(data: unknown, status = 200): Response {
  return new Response(
    JSON.stringify(
      status === 200
        ? { data }
        : {
            error: {
              code: "audit_hold.list_failed",
              message: "Hold query unavailable",
            },
          },
    ),
    { status },
  );
}
async function settleUI(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
  await nextTick();
}
function mountHolds() {
  const requests: Array<{
    url: URL;
    response: ReturnType<typeof deferred<Response>>;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.method).toBe("GET");
      expect(init?.body).toBeUndefined();
      expect(new Headers(init?.headers).get("Idempotency-Key")).toBeNull();
      const response = deferred<Response>();
      cleanups.push(() => response.reject(new Error("test request cleanup")));
      requests.push({ url: new URL(String(input)), response });
      return response.promise;
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-session",
    fetcher,
  );
  const otherCalls = [
    vi.spyOn(client.compliance, "putHold"),
    vi.spyOn(client.compliance, "removeHold"),
    vi.spyOn(client.compliance, "createAuditExport"),
    vi.spyOn(client.compliance, "runAuditAnchors"),
    vi.spyOn(client.compliance, "getAuditAnchorManifest"),
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client.commands, "abandon"),
    vi.spyOn(client, "downloadExport"),
  ];
  return {
    ...mountCommandTest(CompliancePage, { client }, { provide: false }),
    client,
    requests,
    fetcher,
    otherCalls,
  };
}
type MountedHolds = ReturnType<typeof mountHolds>;
function expectCalls(item: MountedHolds, count: number): void {
  expect(item.requests).toHaveLength(count);
  expect(item.fetcher).toHaveBeenCalledTimes(count);
  for (const request of item.requests)
    expect(request.url.pathname).toBe("/v1/admin/audit/holds");
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(Object.keys(sessionStorage)).toEqual(["original-recovery-pointer"]);
  expect(sessionStorage.getItem("original-recovery-pointer")).toBe(
    "retained-original-key",
  );
}
function expectReadFailure(item: MountedHolds): void {
  expect(item.root.querySelector('[title="读取失败"]')).not.toBeNull();
  expect(item.root.querySelector("table")).toBeNull();
  expect(item.root.querySelector(".compliance-create-grid")).toBeNull();
  expect(item.root.querySelector(`[title="${empty}"]`)).toBeNull();
  expect(item.root.textContent).not.toContain("暂无生效的 Legal Hold");
  expect(item.root.textContent).not.toContain(marker);
}

beforeEach(() => {
  sessionStorage.clear();
  sessionStorage.setItem("original-recovery-pointer", "retained-original-key");
  vi.useFakeTimers();
});
afterEach(async () => {
  unmountCommandTests();
  cleanups.splice(0).forEach((cleanup) => cleanup());
  await settleUI();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("Legal Hold read errors are not proof of no holds", () => {
  it.each([
    { label: "missing list", data: {} },
    { label: "null list", data: { holds: null } },
    { label: "object list", data: { holds: row } },
    { label: "alias list", data: { items: [row] } },
    { label: "all malformed rows", data: { holds: [null, [], marker] } },
    { label: "mixed malformed rows", data: { holds: [row, null] } },
  ])(
    "rejects $label and only an explicit GET can recover",
    async ({ data }) => {
      const item = mountHolds();
      expectCalls(item, 1);
      item.requests[0].response.resolve(json(data));
      await settleUI();
      expectReadFailure(item);
      await vi.advanceTimersByTimeAsync(60_000);
      expectCalls(item, 1);
      commandButton(item.root, "重试").click();
      await settleUI();
      expectCalls(item, 2);
      expect(item.requests[1].url.href).toBe(item.requests[0].url.href);
      item.requests[1].response.resolve(
        json({ holds: [{ ...row, reason: "explicit-read-visible-hold" }] }),
      );
      await settleUI();
      expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
      expect(item.root.textContent).toContain("explicit-read-visible-hold");
      expect(item.root.textContent).not.toContain(marker);
      expect(item.root.querySelectorAll("tbody tr")).toHaveLength(1);
      expectCalls(item, 2);
    },
  );

  it("shows loading, then a successful empty query without declaring hold absence", async () => {
    const item = mountHolds();
    expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
    expect(item.root.querySelector(".compliance-create-grid")).toBeNull();
    expect(item.root.querySelector(`[title="${empty}"]`)).toBeNull();
    await vi.advanceTimersByTimeAsync(1_000);
    expectCalls(item, 1);
    item.requests[0].response.resolve(json({ holds: [] }));
    await settleUI();
    expect(item.root.querySelector(`[title="${empty}"]`)).not.toBeNull();
    expect(item.root.textContent).not.toContain("暂无生效的 Legal Hold");
    expect(item.root.textContent).toContain("不授权解除保全或清理");
    expect(item.root.querySelector("table")).toBeNull();
    expect(item.root.querySelector(".compliance-create-grid")).not.toBeNull();
    await vi.advanceTimersByTimeAsync(60_000);
    expectCalls(item, 1);
  });

  it.each([403, 503])(
    "retains HTTP %i read error and GET-only explicit retry",
    async (status) => {
      const item = mountHolds();
      item.requests[0].response.resolve(json(undefined, status));
      await settleUI();
      expectReadFailure(item);
      await vi.advanceTimersByTimeAsync(60_000);
      expectCalls(item, 1);
      commandButton(item.root, "重试").click();
      await settleUI();
      item.requests[1].response.resolve(json({ holds: [] }));
      await settleUI();
      expect(item.root.querySelector(`[title="${empty}"]`)).not.toBeNull();
      expectCalls(item, 2);
    },
  );

  it("keeps another failed explicit read an error, never a successful empty result", async () => {
    const item = mountHolds();
    item.requests[0].response.resolve(json({ holds: [null] }));
    await settleUI();
    expectReadFailure(item);
    commandButton(item.root, "重试").click();
    await settleUI();
    item.requests[1].response.resolve(json({ holds: "bad-list" }));
    await settleUI();
    expectReadFailure(item);
    await vi.advanceTimersByTimeAsync(60_000);
    expectCalls(item, 2);
  });

  it("waits for the complete body before rejecting a mixed list", async () => {
    const item = mountHolds();
    let body!: ReadableStreamDefaultController<Uint8Array>;
    item.requests[0].response.resolve(
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            body = controller;
          },
        }),
      ),
    );
    cleanups.push(() => body.error(new Error("test body cleanup")));
    body.enqueue(new TextEncoder().encode('{"data":{"holds":['));
    await settleUI();
    expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
    expect(item.root.querySelector("table")).toBeNull();
    body.enqueue(new TextEncoder().encode(`${JSON.stringify(row)},null]}}`));
    body.close();
    await settleUI();
    expectReadFailure(item);
    expectCalls(item, 1);
  });

  it.each(["valid", "invalid"])(
    "does not adopt a late %s result into a remounted page",
    async (kind) => {
      const old = mountHolds();
      old.app.unmount();
      const current = mountHolds();
      current.requests[0].response.resolve(json({ holds: [] }));
      await settleUI();
      old.requests[0].response.resolve(
        json({ holds: kind === "valid" ? [row] : [row, null] }),
      );
      await settleUI();
      expect(current.root.querySelector(`[title="${empty}"]`)).not.toBeNull();
      expect(current.root.textContent).not.toContain(marker);
      expect(current.root.querySelector('[title="读取失败"]')).toBeNull();
      expectCalls(old, 1);
      expectCalls(current, 1);
    },
  );
});
