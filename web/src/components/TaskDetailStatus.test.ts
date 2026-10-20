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
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
  vi.useRealTimers();
});
const families = [
  {
    kind: "sync",
    path: "/sync/jobs",
    active: ["pending", "running"],
    stopped: ["completed", "partially_completed", "failed", "unknown"],
    foreign: "requested",
  },
  {
    kind: "operation",
    path: "/operations",
    active: ["requested", "running", "waiting", "compensating"],
    stopped: ["unknown", "partially_completed", "completed", "failed"],
    foreign: "pending",
  },
] as const;
type Family = (typeof families)[number];
const originalKey = "01800000-0000-7000-8000-000000000004";
const invalidMessage = "任务详情与原请求不一致";
function task(
  family: Family,
  status: unknown,
  marker = "original-result-marker",
): JsonRecord {
  const common = {
    account_id: testAccountID,
    tenant_id: "tenant-a",
    actor_uid: "original-initiator",
    status,
    result: { status: "unknown", source_operation_id: marker },
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
        steps: [
          {
            step_key: "summary",
            source: "aero-im",
            action: "summary",
            status: "pending",
            dataset_scope: {
              status: "explicit",
              datasets: ["aero-im.workspaces"],
            },
          },
        ],
      };
}
function json(data: JsonRecord): Response {
  return new Response(JSON.stringify({ data }), {
    headers: { "Content-Type": "application/json" },
  });
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
function mountTask(
  family: Family,
  initialStatus: unknown,
  marker = "original-result-marker",
) {
  saveSyncJobReconcileKey(sessionStorage, testReceiptID, originalKey);
  saveOperationReconcileKey(sessionStorage, testReceiptID, originalKey);
  let detail = task(family, initialStatus, marker);
  const requests: Array<{ path: string; method?: string; key: string | null }> =
    [];
  const path = `/v1${family.path}/${testReceiptID}`;
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const requestPath = new URL(String(input)).pathname;
      requests.push({
        path: requestPath,
        method: init?.method,
        key: new Headers(init?.headers).get("Idempotency-Key"),
      });
      if (init?.method !== "GET")
        throw new Error("unexpected task status write");
      if (requestPath.startsWith("/v1/audit/operations/"))
        return json({ operation_id: testReceiptID, events: [] });
      if (requestPath !== path)
        throw new Error("unexpected task status target");
      return json(detail);
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
    path,
    setDetail: (value: JsonRecord) => {
      detail = value;
    },
    reads: () => requests.filter((request) => request.path === path),
  };
}
function expectNoWrites(item: ReturnType<typeof mountTask>): void {
  for (const write of item.writes) expect(write).not.toHaveBeenCalled();
  expect(item.changed).not.toHaveBeenCalled();
  expect(
    item.requests.every(
      (request) => request.method === "GET" && request.key === null,
    ),
  ).toBe(true);
}

describe.each(families)(
  "$kind detail status adoption and existing GET polling",
  (family) => {
    const invalid = [
      { name: "missing", status: undefined },
      { name: "null", status: null },
      { name: "array active status", status: [family.active[0]] },
      { name: "other family", status: family.foreign },
      { name: "canceled", status: "canceled" },
      { name: "unrecognized", status: "untrusted-status-marker" },
    ];
    it.each(invalid)(
      "refuses initial $name status; only explicit retry GETs the original task",
      async ({ status }) => {
        const item = mountTask(family, status, "untrusted-result-marker");
        const stored = storageSnapshot();
        await settleUI();
        expect(item.root.textContent).toContain(invalidMessage);
        expect(item.root.textContent).not.toContain("untrusted-result-marker");
        expect(item.root.textContent).not.toContain("untrusted-status-marker");
        expect(item.root.textContent).not.toContain("刷新状态");
        await vi.advanceTimersByTimeAsync(60_000);
        expect(item.reads()).toHaveLength(1);
        expect(storageSnapshot()).toEqual(stored);
        expectNoWrites(item);

        item.setDetail(task(family, "unknown"));
        commandButton(item.root, "重试").click();
        await settleUI();
        expect(item.root.textContent).toContain("original-result-marker");
        expect(item.root.textContent).not.toContain(invalidMessage);
        expect(commandButton(item.root, "对账未知结果").disabled).toBe(false);
        await vi.advanceTimersByTimeAsync(60_000);
        expect(item.reads()).toHaveLength(2);
        expect(
          item.reads().every((request) => request.path === item.path),
        ).toBe(true);
        expect(storageSnapshot()).toEqual(stored);
        expectNoWrites(item);
      },
    );

    it.each(invalid)(
      "pauses background GET on $name status without adopting its result or clearing original keys",
      async ({ status }) => {
        const item = mountTask(family, family.active[0]);
        await settleUI();
        const stored = storageSnapshot();
        expect(item.root.textContent).toContain("original-result-marker");
        item.setDetail(task(family, status, "untrusted-result-marker"));
        await vi.advanceTimersByTimeAsync(2000);
        await settleUI();
        expect(item.root.textContent).toContain(invalidMessage);
        expect(item.root.textContent).not.toContain("untrusted-result-marker");
        expect(item.root.textContent).not.toContain("刷新状态");
        await vi.advanceTimersByTimeAsync(60_000);
        expect(item.reads()).toHaveLength(2);
        expect(storageSnapshot()).toEqual(stored);
        expectNoWrites(item);

        item.setDetail(task(family, "unknown"));
        commandButton(item.root, "重试").click();
        await settleUI();
        expect(item.root.textContent).toContain("original-result-marker");
        expect(item.root.textContent).not.toContain("untrusted-result-marker");
        await vi.advanceTimersByTimeAsync(60_000);
        expect(item.reads()).toHaveLength(3);
        expect(storageSnapshot()).toEqual(stored);
        expectNoWrites(item);
      },
    );

    it.each([...family.active, ...family.stopped])(
      "keeps valid %s and the original poll/stop behavior without commands",
      async (status) => {
        const item = mountTask(family, status);
        const stored = storageSnapshot();
        await settleUI();
        expect(item.root.textContent).toContain("original-result-marker");
        expect(item.root.textContent).not.toContain(invalidMessage);
        expect(item.reads()).toHaveLength(1);
        await vi.advanceTimersByTimeAsync(2000);
        await settleUI();
        const active = (family.active as readonly string[]).includes(status);
        expect(item.reads()).toHaveLength(active ? 2 : 1);
        if (!active) {
          await vi.advanceTimersByTimeAsync(60_000);
          expect(item.reads()).toHaveLength(1);
        }
        expect(storageSnapshot()).toEqual(stored);
        expectNoWrites(item);
      },
    );
  },
);
