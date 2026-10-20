import { nextTick, type ComponentInternalInstance } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { AuditHoldScope } from "../api/compliance";
import {
  commandButton,
  mountCommandTest,
  unmountCommandTests,
} from "../components/commandTestFixtures";
import CompliancePage from "./CompliancePage.vue";

let retainedConfirm: (() => void) | undefined;
vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, getCurrentInstance, h } = await import("vue");
  const { irisCommandTestStubs } =
    await import("../components/commandTestFixtures");
  return {
    ...irisCommandTestStubs(),
    IrisDialog: defineComponent({
      props: { open: Boolean },
      setup(props, { slots }) {
        const parent = getCurrentInstance()?.parent?.vnode.props;
        const compliance = parent && Object.hasOwn(parent, "command");
        // Retain the real parent listener across the list's loading/error
        // branches, rather than dispatching a click on removed/disabled DOM.
        if (compliance && typeof parent.onConfirm === "function") {
          const listener = parent.onConfirm;
          retainedConfirm = () => listener();
        }
        return () =>
          props.open
            ? h(
                "div",
                compliance ? { "data-compliance-dialog": "" } : {},
                slots.default?.(),
              )
            : null;
      },
    }),
  };
});

const scopeID = "Original Scope:Alpha/百分比%?";
const rows = [
  {
    scope_type: "account",
    scope_id: scopeID,
    reason: "preserve-original-evidence",
    created_by: "operator",
    created_at: "2026-10-16T01:00:00Z",
    expires_at: null,
  },
];
const cleanups: Array<() => void> = [];
type Read = (signal: AbortSignal) => Promise<Response>;
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
function json(data: unknown): Response {
  return new Response(JSON.stringify({ data }));
}
async function unavailable(): Promise<Response> {
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
function heldRead(phase: "fetch" | "body", ignoreAbort = false) {
  const pending = deferred<Response>();
  let stream: ReadableStreamDefaultController<Uint8Array> | undefined;
  let done = false;
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
        if (done || ignoreAbort) return;
        done = true;
        const reason = new DOMException("aborted", "AbortError");
        if (stream) stream.error(reason);
        else pending.reject(reason);
      },
      { once: true },
    );
    return response ?? pending.promise;
  };
  const release = (data: unknown = { holds: rows }) => {
    if (done) return;
    done = true;
    if (stream) {
      stream.enqueue(new TextEncoder().encode(JSON.stringify({ data })));
      stream.close();
    } else pending.resolve(json(data));
  };
  cleanups.push(() => release());
  return { read, release };
}
async function settleUI(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
  await nextTick();
}
function storageSnapshot(): Record<string, string | null> {
  return Object.fromEntries(
    Object.keys(sessionStorage).map((key) => [
      key,
      sessionStorage.getItem(key),
    ]),
  );
}
function mountHolds(scopeType: AuditHoldScope = "account") {
  const queue: Read[] = [unavailable];
  const reads: Array<{ url: URL; signal: AbortSignal }> = [];
  const writes: Array<{
    url: URL;
    init: RequestInit;
    response: ReturnType<typeof deferred<Response>>;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      if (!init?.signal) throw new Error("missing original transport signal");
      if (init.method === "GET") {
        expect(url.pathname).toBe("/v1/admin/audit/holds");
        expect(url.search).toBe("");
        expect(init.body).toBeUndefined();
        expect(new Headers(init.headers).get("Idempotency-Key")).toBeNull();
        reads.push({ url, signal: init.signal });
        const read = queue.shift();
        if (!read) throw new Error("unexpected automatic read");
        return read(init.signal);
      }
      const response = deferred<Response>();
      cleanups.push(() => response.reject(new Error("test command cleanup")));
      writes.push({ url, init, response });
      return response.promise;
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-session",
    fetcher,
  );
  const remove = vi.spyOn(client.compliance, "removeHold");
  const otherCalls = [
    vi.spyOn(client.compliance, "putHold"),
    vi.spyOn(client.compliance, "createAuditExport"),
    vi.spyOn(client.compliance, "runAuditAnchors"),
    vi.spyOn(client.compliance, "getAuditAnchorManifest"),
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client.commands, "abandon"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileOperation"),
    vi.spyOn(client, "downloadExport"),
  ];
  return {
    ...mountCommandTest(CompliancePage, { client }, { provide: false }),
    client,
    reads,
    writes,
    fetcher,
    remove,
    otherCalls,
    scopeType,
    enqueue: (read: Read) => queue.push(read),
    valid: () =>
      json({ holds: rows.map((row) => ({ ...row, scope_type: scopeType })) }),
  };
}
type MountedHolds = ReturnType<typeof mountHolds>;
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
async function preparedRemoval(scopeType: AuditHoldScope = "account") {
  const item = mountHolds(scopeType);
  await settleUI();
  const reload = capturedRetry(item);
  item.enqueue(async () => item.valid());
  // Recover using the original, explicit PageError retry. The same actual
  // reload callback is retained for subsequent list reads; no mocked hook.
  commandButton(item.root, "重试").click();
  await settleUI();
  commandButton(item.root, "解除").click();
  await settleUI();
  const input = item.root.querySelector("[data-compliance-dialog] input");
  if (!(input instanceof HTMLInputElement))
    throw new Error("missing Scope ID input");
  input.value = scopeID;
  input.dispatchEvent(new Event("input"));
  await settleUI();
  expect(commandButton(item.root, "确认解除保全").disabled).toBe(false);
  expect(retainedConfirm).toBeTypeOf("function");
  return { item, reload, confirm: retainedConfirm! };
}
function expectCommands(
  item: MountedHolds,
  count: number,
  stored: ReturnType<typeof storageSnapshot>,
  readCount: number,
): void {
  expect(item.remove).toHaveBeenCalledTimes(count);
  expect(item.writes).toHaveLength(count);
  expect(item.reads).toHaveLength(readCount);
  expect(item.fetcher).toHaveBeenCalledTimes(readCount + count);
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(storageSnapshot()).toEqual(stored);
}
function expectOriginalRemoval(item: MountedHolds): void {
  expect(item.remove).toHaveBeenCalledWith(item.scopeType, scopeID);
  expect(item.writes[0].init.method).toBe("DELETE");
  expect(item.writes[0].init.body).toBeUndefined();
  expect(item.writes[0].url.pathname).toBe(
    `/v1/admin/audit/holds/${item.scopeType}/${encodeURIComponent(scopeID)}`,
  );
  expect(item.writes[0].url.search).toBe("");
  expect(
    new Headers(item.writes[0].init.headers).get("Idempotency-Key"),
  ).toBeNull();
}

beforeEach(() => {
  retainedConfirm = undefined;
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

describe.each(["fetch", "body"] as const)(
  "Hold removal requires a successful list read, including pending %s",
  (phase) => {
    it.each<AuditHoldScope>(["tenant", "account", "actor"])(
      "refuses a retained exact %s confirmation until explicit GET recovery finishes",
      async (scopeType) => {
        const { item, reload, confirm } = await preparedRemoval(scopeType);
        const stored = storageSnapshot();
        const pending = heldRead(phase);
        item.enqueue(pending.read);
        reload();
        await settleUI();
        expect(item.root.textContent).toContain("正在读取");
        expect(item.root.querySelector("[data-compliance-dialog]")).toBeNull();
        confirm();
        confirm();
        await settleUI();
        expectCommands(item, 0, stored, 3);
        expect(item.reads[2].signal.aborted).toBe(false);
        pending.release({
          holds: rows.map((row) => ({ ...row, scope_type: scopeType })),
        });
        await settleUI();
        expectCommands(item, 0, stored, 3);
        expect(
          item.root.querySelector<HTMLInputElement>(
            "[data-compliance-dialog] input",
          )?.value,
        ).toBe(scopeID);
        // Restoring the read and exact phrase never submits automatically.
        confirm();
        confirm();
        expectCommands(item, 1, stored, 3);
        expectOriginalRemoval(item);
      },
    );

    it("keeps the original 15-second GET deadline and refuses confirmation after timeout", async () => {
      const { item, reload, confirm } = await preparedRemoval();
      const stored = storageSnapshot();
      const pending = heldRead(phase);
      item.enqueue(pending.read);
      reload();
      await settleUI();
      await vi.advanceTimersByTimeAsync(14_999);
      confirm();
      expectCommands(item, 0, stored, 3);
      expect(item.reads[2].signal.aborted).toBe(false);
      await vi.advanceTimersByTimeAsync(1);
      expect(item.reads[2].signal.aborted).toBe(true);
      expect(item.root.textContent).toContain("请求超时");
      confirm();
      await vi.advanceTimersByTimeAsync(60_000);
      expectCommands(item, 0, stored, 3);
      item.enqueue(async () => item.valid());
      commandButton(item.root, "重试").click();
      await settleUI();
      expectCommands(item, 0, stored, 4);
      confirm();
      expectCommands(item, 1, stored, 4);
      expectOriginalRemoval(item);
    });

    it("does not let a replaced non-cooperating success lend admission after the latest read failed", async () => {
      const { item, reload, confirm } = await preparedRemoval();
      const stored = storageSnapshot();
      const old = heldRead(phase, true);
      item.enqueue(old.read);
      reload();
      await settleUI();
      item.enqueue(unavailable);
      reload();
      await settleUI();
      expect(item.reads[2].signal.aborted).toBe(true);
      old.release();
      await settleUI();
      expect(item.root.querySelector('[title="读取失败"]')).not.toBeNull();
      confirm();
      await vi.advanceTimersByTimeAsync(60_000);
      expectCommands(item, 0, stored, 4);
    });
  },
);

const failures: Array<{ name: string; read: Read }> = [
  { name: "403 denial", read: async () => new Response("{}", { status: 403 }) },
  { name: "503 dependency failure", read: unavailable },
  { name: "invalid JSON", read: async () => new Response("bad-read-marker") },
  { name: "missing holds", read: async () => json({}) },
  { name: "non-array holds", read: async () => json({ holds: {} }) },
  { name: "bad member", read: async () => json({ holds: [...rows, null] }) },
  {
    name: "stale read",
    read: async () =>
      new Response(JSON.stringify({ data: { holds: rows } }), {
        headers: { "X-Aero-Stale-Read": "true" },
      }),
  },
  {
    name: "network loss",
    read: async () => {
      throw new TypeError("controlled read loss");
    },
  },
];
describe("Hold removal never borrows the old list after a read error", () => {
  it.each(failures)(
    "refuses after $name and only resumes on explicit read plus confirm",
    async ({ read }) => {
      const { item, reload, confirm } = await preparedRemoval();
      const stored = storageSnapshot();
      item.enqueue(read);
      reload();
      await settleUI();
      expect(item.root.querySelector('[title="读取失败"]')).not.toBeNull();
      confirm();
      confirm();
      await vi.advanceTimersByTimeAsync(60_000);
      expectCommands(item, 0, stored, 3);
      expect(item.root.textContent).not.toContain("Legal Hold 已解除");
      item.enqueue(async () => item.valid());
      commandButton(item.root, "重试").click();
      await settleUI();
      expectCommands(item, 0, stored, 4);
      expect(
        item.root.querySelector<HTMLInputElement>(
          "[data-compliance-dialog] input",
        )?.value,
      ).toBe(scopeID);
      confirm();
      expectCommands(item, 1, stored, 4);
      expectOriginalRemoval(item);
    },
  );

  it.each(["success", "lost acknowledgment"])(
    "does not withdraw or retry an already-admitted DELETE on list failure (%s)",
    async (outcome) => {
      const { item, reload, confirm } = await preparedRemoval();
      const stored = storageSnapshot();
      confirm();
      expectOriginalRemoval(item);
      item.enqueue(unavailable);
      reload();
      await settleUI();
      const write = item.writes[0];
      expect(write.init.signal?.aborted).toBe(false);
      confirm();
      expectCommands(item, 1, stored, 3);
      // The original post-command refresh is unchanged; it is not a write retry.
      item.enqueue(async () => item.valid());
      if (outcome === "success")
        write.response.resolve(json({ removed: true }));
      else
        write.response.reject(new TypeError("controlled acknowledgment loss"));
      await settleUI();
      confirm();
      await vi.advanceTimersByTimeAsync(60_000);
      expectCommands(item, 1, stored, 4);
      expect(item.root.querySelector("[data-compliance-dialog]")).toBeNull();
    },
  );
});
