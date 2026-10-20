import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import JobDetails from "./JobDetails.vue";
import OperationDetails from "./OperationDetails.vue";
import {
  commandButton,
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
beforeEach(() => {
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

const families = [
  { kind: "sync", path: "/sync/jobs", active: "pending" },
  { kind: "operation", path: "/operations", active: "requested" },
] as const;
type Family = (typeof families)[number];
type Read = (signal: AbortSignal) => Promise<Response>;
const originalKey = "01800000-0000-7000-8000-000000000004";
const loadingMessage = "正在读取平台数据";
function task(family: Family, status: string = family.active): JsonRecord {
  const common = {
    account_id: testAccountID,
    tenant_id: "tenant-a",
    actor_uid: "original-initiator",
    status,
    result: { source_operation_id: "original-result-marker" },
  };
  return family.kind === "sync"
    ? {
        ...common,
        job_id: testReceiptID,
        job_type: "export",
        request: { datasets: ["aero-vault.usage"] },
      }
    : {
        ...common,
        operation_id: testReceiptID,
        operation_type: "sync",
        home_region: "local",
        steps: [],
      };
}
function json(payload: JsonRecord, status = 200, headers = {}): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}
function heldRead(stage: "fetch" | "body", ignoreAbort = false) {
  let resolve!: (response: Response) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<Response>((done, failed) => {
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
    return response ?? promise;
  };
  return {
    read,
    release(payload: JsonRecord, status = 200, headers = {}): void {
      if (settled) return;
      settled = true;
      if (stream) {
        stream.enqueue(new TextEncoder().encode(JSON.stringify(payload)));
        stream.close();
      } else resolve(json(payload, status, headers));
    },
  };
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
function mountTask(family: Family, initialRead?: Read) {
  saveSyncJobReconcileKey(sessionStorage, testReceiptID, originalKey);
  saveOperationReconcileKey(sessionStorage, testReceiptID, originalKey);
  const queue: Read[] = initialRead ? [initialRead] : [];
  const path = `/v1${family.path}/${testReceiptID}`;
  const requests: Array<{
    path: string;
    method?: string;
    key: string | null;
    signal: AbortSignal;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const requestPath = new URL(String(input)).pathname;
      if (!init?.signal) throw new Error("missing read cancellation signal");
      requests.push({
        path: requestPath,
        method: init.method,
        key: new Headers(init.headers).get("Idempotency-Key"),
        signal: init.signal,
      });
      if (init.method !== "GET") throw new Error("unexpected task write");
      if (requestPath.startsWith("/v1/audit/operations/"))
        return json({ data: { operation_id: testReceiptID, events: [] } });
      if (requestPath !== path) throw new Error("unexpected detail target");
      const read = queue.shift();
      return read ? read(init.signal) : json({ data: task(family) });
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
  const changed = vi.fn();
  const mounted = mountCommandTest(
    family.kind === "sync" ? JobDetails : OperationDetails,
    {
      client,
      ...(family.kind === "sync"
        ? { jobId: testReceiptID }
        : { operationId: testReceiptID }),
      onChanged: changed,
    },
  );
  return {
    ...mounted,
    requests,
    writes,
    changed,
    enqueue: (read: Read) => queue.push(read),
    reads: () => requests.filter((request) => request.path === path),
  };
}
function expectReadOnly(
  item: ReturnType<typeof mountTask>,
  stored: ReturnType<typeof storageSnapshot>,
): void {
  expect(storageSnapshot()).toEqual(stored);
  for (const write of item.writes) expect(write).not.toHaveBeenCalled();
  expect(item.changed).not.toHaveBeenCalled();
  expect(
    item.requests.every(
      (request) => request.method === "GET" && request.key === null,
    ),
  ).toBe(true);
}
async function manualRead(
  item: ReturnType<typeof mountTask>,
  pending: ReturnType<typeof heldRead>,
): Promise<void> {
  await settleUI();
  await vi.advanceTimersByTimeAsync(1000);
  item.enqueue(pending.read);
  commandButton(item.root, "刷新状态").click();
  await settleUI();
  expect(item.reads()).toHaveLength(2);
  expect(item.root.textContent).toContain(loadingMessage);
}

describe.each(families)(
  "$kind foreground detail reads versus polling",
  (family) => {
    describe.each(["fetch", "body"] as const)("slow %s", (stage) => {
      it.each([family.active, "unknown", "completed"])(
        "does not cancel manual %s, then respects the original poll/stop rules",
        async (status) => {
          const item = mountTask(family);
          const stored = storageSnapshot();
          const pending = heldRead(stage);
          await manualRead(item, pending);
          await vi.advanceTimersByTimeAsync(10_000);
          expect(item.reads()).toHaveLength(2);
          expect(item.reads()[1].signal.aborted).toBe(false);
          expect(item.root.textContent).toContain(loadingMessage);
          expectReadOnly(item, stored);

          pending.release({
            data: {
              ...task(family, status),
              result: { source_operation_id: "manual-result-marker" },
            },
          });
          await settleUI();
          expect(item.root.textContent).toContain("manual-result-marker");
          expect(item.root.textContent).not.toContain(loadingMessage);
          await vi.advanceTimersByTimeAsync(1999);
          expect(item.reads()).toHaveLength(2);
          await vi.advanceTimersByTimeAsync(1);
          expect(item.reads()).toHaveLength(status === family.active ? 3 : 2);
          if (status !== family.active) {
            await vi.advanceTimersByTimeAsync(60_000);
            expect(item.reads()).toHaveLength(2);
          }
          expectReadOnly(item, stored);
        },
      );
    });

    it.each([
      {
        name: "denied",
        status: 403,
        payload: {
          error: { code: "task.forbidden", message: "current read denied" },
        },
        message: "current read denied",
      },
      {
        name: "unavailable",
        status: 503,
        payload: {
          error: {
            code: "task.unavailable",
            message: "current read unavailable",
          },
        },
        message: "current read unavailable",
      },
      {
        name: "wrong target",
        status: 200,
        payload: {
          data: {
            ...task(family),
            job_id: "wrong-target",
            operation_id: "wrong-target",
          },
        },
        message: "任务详情与原请求不一致",
      },
      {
        name: "stale read",
        status: 200,
        payload: { data: task(family) },
        message: "当前区域无法确认最近一次写入已同步",
        headers: { "X-Aero-Stale-Read": "true" },
      },
    ])(
      "keeps a slow manual $name visible and stops polling; explicit retry only GETs the original ID",
      async ({ status, payload, message, headers }) => {
        const item = mountTask(family);
        const stored = storageSnapshot();
        const pending = heldRead("fetch");
        await manualRead(item, pending);
        await vi.advanceTimersByTimeAsync(5000);
        pending.release(payload, status, headers);
        await settleUI();
        expect(item.root.textContent).toContain(message);
        expect(item.root.textContent).not.toContain("original-result-marker");
        expect(item.root.textContent).not.toContain("wrong-target");
        await vi.advanceTimersByTimeAsync(60_000);
        expect(item.reads()).toHaveLength(2);
        expectReadOnly(item, stored);
        item.enqueue(async () => json({ data: task(family, "unknown") }));
        commandButton(item.root, "重试").click();
        await settleUI();
        expect(item.reads()).toHaveLength(3);
        expect(item.root.textContent).toContain("original-result-marker");
        await vi.advanceTimersByTimeAsync(60_000);
        expect(item.reads()).toHaveLength(3);
        expectReadOnly(item, stored);
      },
    );

    it("does not renew or suppress the original 15-second manual deadline", async () => {
      const item = mountTask(family);
      const stored = storageSnapshot();
      const pending = heldRead("fetch");
      await manualRead(item, pending);
      await vi.advanceTimersByTimeAsync(14_999);
      expect(item.reads()).toHaveLength(2);
      expect(item.reads()[1].signal.aborted).toBe(false);
      await vi.advanceTimersByTimeAsync(1);
      await settleUI();
      expect(item.reads()[1].signal.aborted).toBe(true);
      expect(item.root.textContent).toContain("aero-id 请求超时");
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.reads()).toHaveLength(2);
      expectReadOnly(item, stored);
    });

    it("cancels manual reads on unmount and never adopts their noncooperative late result", async () => {
      const item = mountTask(family);
      const stored = storageSnapshot();
      const pending = heldRead("fetch", true);
      await manualRead(item, pending);
      await vi.advanceTimersByTimeAsync(5000);
      expect(item.reads()).toHaveLength(2);
      unmountCommandTests();
      expect(item.reads()[1].signal.aborted).toBe(true);
      pending.release({ data: task(family, "completed") });
      await settleUI();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.root.textContent).toBe("");
      expect(item.reads()).toHaveLength(2);
      expectReadOnly(item, stored);
    });

    it("still lets an explicit refresh supersede an already-started poll without adopting its late result", async () => {
      const item = mountTask(family);
      const stored = storageSnapshot();
      await settleUI();
      const automatic = heldRead("fetch", true);
      item.enqueue(automatic.read);
      await vi.advanceTimersByTimeAsync(2000);
      expect(item.reads()).toHaveLength(2);
      expect(item.root.textContent).toContain("original-result-marker");
      const manual = heldRead("fetch");
      item.enqueue(manual.read);
      commandButton(item.root, "刷新状态").click();
      await settleUI();
      expect(item.reads()).toHaveLength(3);
      expect(item.reads()[1].signal.aborted).toBe(true);
      expect(item.reads()[2].signal.aborted).toBe(false);
      automatic.release({
        data: {
          ...task(family, "completed"),
          result: { source_operation_id: "late-poll-result-marker" },
        },
      });
      await settleUI();
      await vi.advanceTimersByTimeAsync(5000);
      expect(item.reads()).toHaveLength(3);
      expect(item.root.textContent).toContain(loadingMessage);
      expect(item.root.textContent).not.toContain("late-poll-result-marker");
      manual.release({
        data: {
          ...task(family, "unknown"),
          result: { source_operation_id: "manual-result-marker" },
        },
      });
      await settleUI();
      expect(item.root.textContent).toContain("manual-result-marker");
      expect(item.root.textContent).not.toContain("late-poll-result-marker");
      await vi.advanceTimersByTimeAsync(60_000);
      expect(item.reads()).toHaveLength(3);
      expectReadOnly(item, stored);
    });

    it("keeps a slow initial read and starts the original interval only after adoption", async () => {
      const pending = heldRead("fetch");
      const item = mountTask(family, pending.read);
      const stored = storageSnapshot();
      await vi.advanceTimersByTimeAsync(10_000);
      expect(item.reads()).toHaveLength(1);
      expect(item.reads()[0].signal.aborted).toBe(false);
      pending.release({ data: task(family) });
      await settleUI();
      await vi.advanceTimersByTimeAsync(1999);
      expect(item.reads()).toHaveLength(1);
      await vi.advanceTimersByTimeAsync(1);
      expect(item.reads()).toHaveLength(2);
      expectReadOnly(item, stored);
    });
  },
);
