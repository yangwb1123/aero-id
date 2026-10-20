import { nextTick, type ComponentInternalInstance } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import {
  commandButton,
  mountCommandTest,
  unmountCommandTests,
} from "../components/commandTestFixtures";
import CompliancePage from "./CompliancePage.vue";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("../components/commandTestFixtures")).irisCommandTestStubs(),
);

const cleanup: Array<() => void> = [];
const marker = "late-original-hold";
const empty = "本次查询无可见的生效 Legal Hold";
type Read = (signal: AbortSignal) => Promise<Response>;
function json(holds: unknown = []): Response {
  return new Response(JSON.stringify({ data: { holds } }));
}
function heldRead(phase: "fetch" | "body", ignoreAbort = false) {
  let resolve!: (response: Response) => void;
  let reject!: (reason: Error) => void;
  const pending = new Promise<Response>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  let stream: ReadableStreamDefaultController<Uint8Array> | undefined;
  let settled = false;
  const read: Read = async (signal) => {
    const response =
      phase === "body"
        ? new Response(
            new ReadableStream<Uint8Array>({
              start: (controller) => {
                stream = controller;
              },
            }),
          )
        : undefined;
    signal.addEventListener(
      "abort",
      () => {
        if (ignoreAbort || settled) return;
        settled = true;
        const reason = new DOMException("aborted", "AbortError");
        if (stream) stream.error(reason);
        else reject(reason);
      },
      { once: true },
    );
    return response ?? pending;
  };
  const release = (holds: unknown = []) => {
    if (settled) return;
    settled = true;
    if (stream) {
      stream.enqueue(
        new TextEncoder().encode(JSON.stringify({ data: { holds } })),
      );
      stream.close();
    } else resolve(json(holds));
  };
  cleanup.push(() => release());
  return { read, release };
}
async function settleUI(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
  await nextTick();
}
function mountHolds(first: Read) {
  const queue: Read[] = [first];
  const requests: Array<{ url: URL; signal: AbortSignal }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      if (!init?.signal) throw new Error("missing transport signal");
      expect(init.method).toBe("GET");
      expect(init.body).toBeUndefined();
      expect(new Headers(init.headers).get("Idempotency-Key")).toBeNull();
      const url = new URL(String(input));
      expect(url.pathname).toBe("/v1/admin/audit/holds");
      expect(url.search).toBe("");
      requests.push({ url, signal: init.signal });
      const read = queue.shift();
      if (!read) throw new Error("unexpected automatic read");
      return read(init.signal);
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-session",
    fetcher,
  );
  const reads = vi.spyOn(client.compliance, "listHolds");
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
    reads,
    fetcher,
    requests,
    otherCalls,
    enqueue: (read: Read) => queue.push(read),
  };
}
type MountedHolds = ReturnType<typeof mountHolds>;
function expectReadOnly(item: MountedHolds, count: number): void {
  expect(item.fetcher).toHaveBeenCalledTimes(count);
  expect(item.requests).toHaveLength(count);
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(Object.keys(sessionStorage)).toEqual(["original-recovery-pointer"]);
  expect(sessionStorage.getItem("original-recovery-pointer")).toBe(
    "original-key",
  );
}
function capturedRetry(item: MountedHolds): () => void {
  const button = commandButton(item.root, "重试") as HTMLButtonElement & {
    __vueParentComponent?: ComponentInternalInstance;
  };
  let instance = button.__vueParentComponent;
  while (instance) {
    const retry = instance.vnode.props?.retry;
    if (typeof retry === "function") return retry as () => void;
    instance = instance.parent ?? undefined;
  }
  throw new Error("missing actual PageError retry prop");
}
async function failedRead(): Promise<Response> {
  return new Response(
    JSON.stringify({
      error: {
        code: "audit_hold.list_failed",
        message: "Hold query unavailable",
      },
    }),
    { status: 503 },
  );
}

beforeEach(() => {
  sessionStorage.clear();
  sessionStorage.setItem("original-recovery-pointer", "original-key");
  vi.useFakeTimers();
});
afterEach(async () => {
  unmountCommandTests();
  cleanup.splice(0).forEach((release) => release());
  await settleUI();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe.each(["fetch", "body"] as const)(
  "Legal Hold pending %s read lifetime",
  (phase) => {
    it.each([false, true])(
      "propagates unmount cancellation (non-cooperating: %s)",
      async (ignoreAbort) => {
        const pending = heldRead(phase, ignoreAbort);
        const old = mountHolds(pending.read);
        await settleUI();
        const signal = old.requests[0].signal;
        expect(signal.aborted).toBe(false);
        old.app.unmount();
        expect(signal.aborted).toBe(true);
        const current = mountHolds(async () => json());
        await settleUI();
        pending.release([
          { scope_type: "account", scope_id: "old-account", reason: marker },
        ]);
        await settleUI();
        await vi.advanceTimersByTimeAsync(20_000);
        expect(current.root.querySelector(`[title="${empty}"]`)).not.toBeNull();
        expect(current.root.textContent).not.toContain(marker);
        expect(current.root.querySelector('[title="读取失败"]')).toBeNull();
        expectReadOnly(old, 1);
        expectReadOnly(current, 1);
      },
    );

    it("propagates the shared hook's replacement abort without adopting a non-cooperating old result", async () => {
      const item = mountHolds(failedRead);
      await settleUI();
      const retry = capturedRetry(item);
      const pending = heldRead(phase, true);
      item.enqueue(pending.read);
      retry();
      await settleUI();
      const signal = item.requests[1].signal;
      expect(signal.aborted).toBe(false);
      item.enqueue(async () => json());
      retry();
      await settleUI();
      expect(signal.aborted).toBe(true);
      expect(item.root.querySelector(`[title="${empty}"]`)).not.toBeNull();
      pending.release([
        { scope_type: "account", scope_id: "old-account", reason: marker },
      ]);
      await settleUI();
      expect(item.root.textContent).not.toContain(marker);
      expect(item.root.querySelector('[title="读取失败"]')).toBeNull();
      await vi.advanceTimersByTimeAsync(20_000);
      expectReadOnly(item, 3);
    });

    it("cancels an explicit retry on unmount and refuses a retained retry after unmount", async () => {
      const item = mountHolds(failedRead);
      await settleUI();
      const retry = capturedRetry(item);
      const pending = heldRead(phase);
      item.enqueue(pending.read);
      commandButton(item.root, "重试").click();
      await settleUI();
      expectReadOnly(item, 2);
      item.app.unmount();
      expect(item.requests[1].signal.aborted).toBe(true);
      retry();
      await settleUI();
      await vi.advanceTimersByTimeAsync(20_000);
      expectReadOnly(item, 2);
    });

    it("keeps the original deadline and permits only an explicit GET recovery", async () => {
      const pending = heldRead(phase);
      const item = mountHolds(pending.read);
      await settleUI();
      await vi.advanceTimersByTimeAsync(14_999);
      expect(item.requests[0].signal.aborted).toBe(false);
      await vi.advanceTimersByTimeAsync(1);
      expect(item.requests[0].signal.aborted).toBe(true);
      expect(item.root.textContent).toContain("请求超时");
      expect(item.root.querySelector(`[title="${empty}"]`)).toBeNull();
      await vi.advanceTimersByTimeAsync(20_000);
      expectReadOnly(item, 1);
      item.enqueue(async () => json());
      commandButton(item.root, "重试").click();
      await settleUI();
      expect(item.root.querySelector(`[title="${empty}"]`)).not.toBeNull();
      expectReadOnly(item, 2);
    });
  },
);

it("does not use transport-wide cancellation or cancel another mounted page's read", async () => {
  const firstPending = heldRead("fetch");
  const secondPending = heldRead("body");
  const first = mountHolds(firstPending.read);
  first.enqueue(secondPending.read);
  const second = mountCommandTest(
    CompliancePage,
    { client: first.client },
    { provide: false },
  );
  await settleUI();
  first.app.unmount();
  expect(first.requests[0].signal.aborted).toBe(true);
  expect(first.requests[1].signal.aborted).toBe(false);
  secondPending.release();
  await settleUI();
  expect(second.root.querySelector(`[title="${empty}"]`)).not.toBeNull();
  expectReadOnly(first, 2);
});
