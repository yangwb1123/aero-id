import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import {
  commandButton,
  mountCommandTest,
  testReceiptID,
  unmountCommandTests,
} from "../components/commandTestFixtures";
import CompliancePage from "./CompliancePage.vue";

vi.mock("@iris-ui-kit/vue", async () => {
  const { irisCommandTestStubs } =
    await import("../components/commandTestFixtures");
  return irisCommandTestStubs();
});

const marker = "rejected-export-receipt-marker";
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
function json(data: unknown, status = 201): Response {
  return new Response(JSON.stringify({ data }), { status });
}
async function settleUI(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
  await nextTick();
}
function storedPointers(): Record<string, string | null> {
  return Object.fromEntries(
    Object.keys(sessionStorage).map((key) => [
      key,
      sessionStorage.getItem(key),
    ]),
  );
}
function mountExports() {
  const reads: URL[] = [];
  const writes: Array<{
    url: URL;
    init: RequestInit;
    response: ReturnType<typeof deferred<Response>>;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      if (init?.method === "GET") {
        reads.push(url);
        if (url.pathname === "/v1/admin/audit/holds")
          return json({ holds: [] }, 200);
        const id = decodeURIComponent(url.pathname.split("/").at(-1) ?? "");
        return json(
          { job_id: id, job_type: "audit_export", status: "unknown" },
          200,
        );
      }
      if (!init) throw new Error("missing original creation options");
      const response = deferred<Response>();
      cleanups.push(() => response.reject(new Error("test request cleanup")));
      writes.push({ url, init, response });
      return response.promise;
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-session",
    fetcher,
  );
  const create = vi.spyOn(client.compliance, "createAuditExport");
  const otherCalls = [
    vi.spyOn(client.compliance, "putHold"),
    vi.spyOn(client.compliance, "removeHold"),
    vi.spyOn(client.compliance, "runAuditAnchors"),
    vi.spyOn(client.compliance, "getAuditAnchorManifest"),
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client.commands, "abandon"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileSyncJob"),
    vi.spyOn(client, "downloadExport"),
    vi.spyOn(client, "createExportJob"),
  ];
  return {
    ...mountCommandTest(CompliancePage, { client }, { provide: false }),
    client,
    fetcher,
    reads,
    writes,
    create,
    otherCalls,
  };
}
type MountedExports = ReturnType<typeof mountExports>;
async function openExport(item: MountedExports): Promise<void> {
  commandButton(item.root, "创建审计导出").click();
  await settleUI();
  expect(item.root.textContent).toContain("确认创建审计导出");
}
function confirm(item: MountedExports): void {
  commandButton(item.root, "确认执行").click();
}
function creationKey(item: MountedExports, index = 0): string {
  const request = item.writes[index];
  const key = new Headers(request.init.headers).get("Idempotency-Key");
  expect(key).toBeTruthy();
  expect(request.url.pathname).toBe("/v1/audit/export");
  expect(request.url.search).toBe("");
  expect(request.init.method).toBe("POST");
  expect(JSON.parse(String(request.init.body))).toEqual({
    idempotency_key: key,
  });
  return key!;
}
function expectNoOtherEffects(item: MountedExports): void {
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(storedPointers()).toEqual({
    "original-recovery-pointer": "retained-original-key",
  });
}
function expectUnadopted(item: MountedExports): void {
  expect(item.root.textContent).toContain("任务可能已创建");
  expect(item.root.textContent).toContain("确认创建审计导出");
  expect(item.root.textContent).not.toContain("已持久化。");
  expect(item.root.textContent).not.toContain(marker);
  expect(item.reads.map((url) => url.pathname)).toEqual([
    "/v1/admin/audit/holds",
  ]);
  expectNoOtherEffects(item);
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

const badReceipts = [
  { name: "missing ID", data: {} },
  { name: "id alias only", data: { id: testReceiptID } },
  { name: "other family only", data: { operation_id: testReceiptID } },
  { name: "nested ID only", data: { result: { job_id: testReceiptID } } },
  {
    name: "null ID with valid alias",
    data: { job_id: null, id: testReceiptID },
  },
  { name: "boolean ID", data: { job_id: true } },
  { name: "number ID", data: { job_id: 42 } },
  { name: "array ID", data: { job_id: [testReceiptID] } },
  { name: "object ID", data: { job_id: { value: testReceiptID } } },
  { name: "empty ID with alias", data: { job_id: "", id: testReceiptID } },
  { name: "blank ID", data: { job_id: " " } },
  { name: "leading space", data: { job_id: ` ${testReceiptID}` } },
  { name: "trailing space", data: { job_id: `${testReceiptID} ` } },
  { name: "nonbreaking space", data: { job_id: `${testReceiptID}\u00a0` } },
  { name: "zero-width suffix", data: { job_id: `${testReceiptID}\u200b` } },
  { name: "opaque non-UUID", data: { job_id: marker } },
  {
    name: "UUID without separators",
    data: { job_id: testReceiptID.replaceAll("-", "") },
  },
  { name: "UUID URN", data: { job_id: `urn:uuid:${testReceiptID}` } },
  { name: "UUID braces", data: { job_id: `{${testReceiptID}}` } },
  {
    name: "nil UUID",
    data: { job_id: "00000000-0000-0000-0000-000000000000" },
  },
  { name: "wrong-length UUID", data: { job_id: `${testReceiptID}0` } },
];

describe("audit-export creation adopts only a non-nil root job UUID", () => {
  it.each(badReceipts)(
    "refuses $name and preserves the original in-memory key",
    async ({ data }) => {
      const item = mountExports();
      await settleUI();
      await openExport(item);
      expect(item.writes).toHaveLength(0);
      confirm(item);
      const originalKey = creationKey(item);
      item.writes[0].response.resolve(json({ ...data, diagnostic: marker }));
      await settleUI();
      expectUnadopted(item);
      // A malformed creation reply may follow a committed job. No automatic
      // GET, POST, replacement key, download or success is inferred from it.
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.writes).toHaveLength(1);
      expect(item.create).toHaveBeenCalledTimes(1);
      expectUnadopted(item);
      commandButton(item.root, "取消").click();
      await settleUI();
      await openExport(item);
      expect(item.writes).toHaveLength(1);
      confirm(item);
      expect(item.writes).toHaveLength(2);
      expect(creationKey(item, 1)).toBe(originalKey);
      expect(item.create.mock.calls).toEqual([[originalKey], [originalKey]]);
      expectNoOtherEffects(item);
    },
  );

  it("does not retire the original key on an id-alias-only receipt", async () => {
    const item = mountExports();
    await settleUI();
    await openExport(item);
    confirm(item);
    const originalKey = creationKey(item);
    item.writes[0].response.resolve(json({ id: testReceiptID }));
    await settleUI();
    const cancel = [...item.root.querySelectorAll("button")].find(
      (button) => button.textContent === "取消",
    );
    cancel?.click();
    await settleUI();
    await openExport(item);
    expect(item.writes).toHaveLength(1);
    confirm(item);
    expect(creationKey(item, 1)).toBe(originalKey);
    expectNoOtherEffects(item);
  });

  it.each([
    { name: "UUIDv7", id: testReceiptID },
    {
      name: "mixed-case UUID without version restriction",
      id: "A1800000-0000-4000-8000-000000000003",
    },
  ])(
    "adopts the exact root $name, not aliases or download_url",
    async ({ id }) => {
      const item = mountExports();
      await settleUI();
      await openExport(item);
      confirm(item);
      const originalKey = creationKey(item);
      item.writes[0].response.resolve(
        json({
          job_id: id,
          id: marker,
          operation_id: marker,
          status: "pending",
          download_url: `/${marker}`,
        }),
      );
      await settleUI();
      expect(item.root.textContent).toContain(`审计导出任务 ${id} 已持久化。`);
      expect(item.root.textContent).not.toContain("确认创建审计导出");
      expect(item.reads.map((url) => url.pathname)).toEqual([
        "/v1/admin/audit/holds",
        `/v1/sync/jobs/${id}`,
      ]);
      expect(item.writes).toHaveLength(1);
      expectNoOtherEffects(item);
      // A separately confirmed new creation may get a new key only after a
      // valid receipt was adopted; the response's download_url is never used.
      await openExport(item);
      expect(item.writes).toHaveLength(1);
      confirm(item);
      expect(creationKey(item, 1)).not.toBe(originalKey);
      expectNoOtherEffects(item);
    },
  );

  it("keeps a previously adopted job while refusing a later alias-only receipt", async () => {
    const item = mountExports();
    await settleUI();
    await openExport(item);
    confirm(item);
    item.writes[0].response.resolve(json({ job_id: testReceiptID }));
    await settleUI();
    await openExport(item);
    confirm(item);
    const originalKey = creationKey(item, 1);
    const otherID = "01800000-0000-7000-8000-000000000099";
    item.writes[1].response.resolve(json({ id: otherID }));
    await settleUI();
    expect(item.root.textContent).toContain("任务可能已创建");
    expect(item.root.textContent).toContain(`任务详情 · ${testReceiptID}`);
    expect(item.root.textContent).not.toContain(otherID);
    expect(item.reads.map((url) => url.pathname)).toEqual([
      "/v1/admin/audit/holds",
      `/v1/sync/jobs/${testReceiptID}`,
    ]);
    commandButton(item.root, "取消").click();
    await settleUI();
    await openExport(item);
    expect(item.writes).toHaveLength(2);
    confirm(item);
    expect(creationKey(item, 2)).toBe(originalKey);
    expectNoOtherEffects(item);
  });

  it.each(["denied", "unavailable", "invalid JSON", "lost acknowledgment"])(
    "preserves the original key after %s without automatic retry",
    async (outcome) => {
      const item = mountExports();
      await settleUI();
      await openExport(item);
      confirm(item);
      const originalKey = creationKey(item);
      if (outcome === "lost acknowledgment")
        item.writes[0].response.reject(
          new TypeError("controlled response loss"),
        );
      else if (outcome === "invalid JSON")
        item.writes[0].response.resolve(new Response(marker));
      else
        item.writes[0].response.resolve(
          new Response(
            JSON.stringify({
              error: {
                code: "sync.create_failed",
                message: "Export unavailable",
              },
            }),
            { status: outcome === "denied" ? 403 : 503 },
          ),
        );
      await settleUI();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.writes).toHaveLength(1);
      expect(item.reads).toHaveLength(1);
      expect(item.root.textContent).toContain("确认创建审计导出");
      expect(item.root.textContent).not.toContain("已持久化。");
      expect(item.root.textContent).not.toContain(marker);
      confirm(item);
      expect(creationKey(item, 1)).toBe(originalKey);
      expectNoOtherEffects(item);
    },
  );

  it.each(["valid", "invalid"])(
    "waits for the complete %s receipt body and preserves single-in-flight admission",
    async (outcome) => {
      const item = mountExports();
      await settleUI();
      await openExport(item);
      confirm(item);
      const originalKey = creationKey(item);
      let body!: ReadableStreamDefaultController<Uint8Array>;
      item.writes[0].response.resolve(
        new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              body = controller;
            },
          }),
          { status: 201 },
        ),
      );
      cleanups.push(() => body.error(new Error("test body cleanup")));
      body.enqueue(
        new TextEncoder().encode(
          JSON.stringify({
            data: { job_id: outcome === "valid" ? testReceiptID : marker },
          }),
        ),
      );
      await settleUI();
      confirm(item);
      expect(item.writes).toHaveLength(1);
      expect(item.reads).toHaveLength(1);
      expect(item.root.textContent).not.toContain("已持久化。");
      body.close();
      await settleUI();
      if (outcome === "valid")
        expect(item.root.textContent).toContain(
          `审计导出任务 ${testReceiptID} 已持久化。`,
        );
      else {
        expectUnadopted(item);
        confirm(item);
        expect(creationKey(item, 1)).toBe(originalKey);
      }
      expectNoOtherEffects(item);
    },
  );
});
