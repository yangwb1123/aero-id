import { nextTick } from "vue";
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
let retainedSubmit: (() => void) | undefined;
let retainedExport: (() => void) | undefined;
vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, getCurrentInstance, h } = await import("vue");
  const { irisCommandTestStubs } =
    await import("../components/commandTestFixtures");
  return {
    ...irisCommandTestStubs(),
    IrisCard: defineComponent({
      setup:
        (_, { slots }) =>
        () => {
          const children = slots.default?.();
          const form = children?.find(
            (child) =>
              child.type === "form" &&
              child.props?.class === "compliance-create-grid",
          );
          const listener = form?.props?.onSubmit;
          if (typeof listener === "function")
            retainedSubmit = () =>
              listener(new Event("submit", { cancelable: true }));
          return h("div", [slots.header?.(), children]);
        },
    }),
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
          if (
            children?.some(
              (child) =>
                typeof child.children === "string" &&
                child.children.trim() === "创建审计导出",
            )
          )
            retainedExport = click;
          return h(
            "button",
            { disabled: props.disabled, type: props.type, onClick: click },
            children,
          );
        },
    }),
    IrisDialog: defineComponent({
      props: { open: Boolean },
      setup(props, { slots }) {
        const parent = getCurrentInstance()?.parent?.vnode.props;
        const compliance = parent && Object.hasOwn(parent, "command");
        // Capture real parent listeners, rather than detached/disabled DOM.
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

const scopeID = "Original-Account-Alpha";
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
function json(data: unknown): Response {
  return new Response(JSON.stringify({ data }));
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
function mountHolds(
  scopeType: AuditHoldScope = "account",
  initialSession = "original-test-session",
) {
  let session = initialSession;
  const rows = [
    {
      scope_type: scopeType,
      scope_id: scopeID,
      reason: "preserve-original-evidence",
      created_by: "operator",
      created_at: "2026-10-16T01:00:00Z",
      expires_at: null,
    },
  ];
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
        expect(url.pathname).toBe("/v1/admin/audit/holds");
        expect(url.search).toBe("");
        expect(init.body).toBeUndefined();
        expect(new Headers(init.headers).get("Idempotency-Key")).toBeNull();
        reads.push(url);
        return json({ holds: rows });
      }
      if (!init) throw new Error("missing original command options");
      const response = deferred<Response>();
      cleanups.push(() => response.reject(new Error("test request cleanup")));
      writes.push({ url, init, response });
      return response.promise;
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => session,
    fetcher,
  );
  const remove = vi.spyOn(client.compliance, "removeHold");
  const put = vi.spyOn(client.compliance, "putHold");
  const auditExport = vi.spyOn(client.compliance, "createAuditExport");
  const otherCalls = [
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
    fetcher,
    reads,
    writes,
    remove,
    put,
    auditExport,
    otherCalls,
    rotateSession: () => {
      session = "replacement-test-session";
    },
  };
}
type MountedHolds = ReturnType<typeof mountHolds>;
function dialog(item: MountedHolds): HTMLElement | null {
  return item.root.querySelector("[data-compliance-dialog]");
}
async function openRemove(item: MountedHolds) {
  await settleUI();
  commandButton(item.root, "解除").click();
  await settleUI();
  const input = dialog(item)?.querySelector("input");
  if (!input) throw new Error("missing original Scope ID input");
  input.value = scopeID;
  input.dispatchEvent(new Event("input"));
  await settleUI();
  expect(commandButton(item.root, "确认解除保全").disabled).toBe(false);
  expect(retainedConfirm).toBeTypeOf("function");
  return retainedConfirm!;
}
async function fillHold(item: MountedHolds): Promise<void> {
  await settleUI();
  const form = item.root.querySelector(".compliance-create-grid");
  const input = form?.querySelector("input");
  const reason = form?.querySelector("textarea");
  if (!input || !reason) throw new Error("missing original Hold form");
  input.value = "New-Hold-Target";
  input.dispatchEvent(new Event("input"));
  reason.value = "preserve-approved-evidence";
  reason.dispatchEvent(new Event("input"));
  await settleUI();
  expect(retainedSubmit).toBeTypeOf("function");
  expect(retainedExport).toBeTypeOf("function");
}
async function openOther(item: MountedHolds, kind: "put" | "export") {
  await fillHold(item);
  if (kind === "put") retainedSubmit!();
  else retainedExport!();
  await settleUI();
  expect(dialog(item)?.textContent).toContain(
    kind === "put" ? "确认设置 Legal Hold" : "确认创建审计导出",
  );
  expect(retainedConfirm).toBeTypeOf("function");
  return retainedConfirm!;
}
function expectUnchanged(
  item: MountedHolds,
  count: number,
  stored: ReturnType<typeof storageSnapshot>,
  readCount = 1,
): void {
  expect(item.writes).toHaveLength(count);
  expect(item.reads).toHaveLength(readCount);
  expect(item.fetcher).toHaveBeenCalledTimes(readCount + count);
  expect(
    item.remove.mock.calls.length +
      item.put.mock.calls.length +
      item.auditExport.mock.calls.length,
  ).toBe(count);
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(storageSnapshot()).toEqual(stored);
}
const outcomes = [
  "success",
  "denied",
  "audit unavailable",
  "invalid JSON",
  "lost acknowledgment",
] as const;
type Outcome = (typeof outcomes)[number];
function settleRemove(item: MountedHolds, outcome: Outcome): void {
  const request = item.writes[0];
  if (outcome === "lost acknowledgment") {
    request.response.reject(new TypeError("controlled response loss"));
  } else if (outcome === "invalid JSON") {
    request.response.resolve(new Response("bad-hold-marker"));
  } else if (outcome === "success") {
    request.response.resolve(json({ removed: true }));
  } else {
    request.response.resolve(
      new Response(
        JSON.stringify({
          error: {
            code: "audit_hold.remove_failed",
            message: "Hold removal unavailable",
          },
        }),
        { status: outcome === "denied" ? 403 : 503 },
      ),
    );
  }
}

beforeEach(() => {
  retainedConfirm = undefined;
  retainedSubmit = undefined;
  retainedExport = undefined;
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

describe("compliance command admission belongs to the mounted page", () => {
  it.each<AuditHoldScope>(["tenant", "account", "actor"])(
    "refuses the original exact %s removal confirmation after unmount",
    async (scopeType) => {
      const item = mountHolds(scopeType);
      const confirm = await openRemove(item);
      const stored = storageSnapshot();
      const makeKey = vi.spyOn(crypto, "randomUUID");
      unmountCommandTests();
      confirm();
      confirm();
      await settleUI();
      expectUnchanged(item, 0, stored);
      expect(item.remove).not.toHaveBeenCalled();
      expect(makeKey).not.toHaveBeenCalled();
      expect(item.root.textContent).toBe("");
    },
  );

  it.each(["put", "export"] as const)(
    "also refuses an already prepared %s confirmation after unmount",
    async (kind) => {
      const item = mountHolds();
      const confirm = await openOther(item, kind);
      const stored = storageSnapshot();
      const makeKey = vi.spyOn(crypto, "randomUUID");
      unmountCommandTests();
      confirm();
      confirm();
      await settleUI();
      expectUnchanged(item, 0, stored);
      expect(makeKey).not.toHaveBeenCalled();
    },
  );

  it.each(["put", "export"] as const)(
    "does not prepare a new %s intent or generate a key from an unmounted listener",
    async (kind) => {
      const item = mountHolds();
      await fillHold(item);
      const open = kind === "put" ? retainedSubmit! : retainedExport!;
      const confirm = retainedConfirm!;
      const stored = storageSnapshot();
      const makeKey = vi.spyOn(crypto, "randomUUID");
      unmountCommandTests();
      open();
      open();
      confirm();
      await settleUI();
      expect(makeKey).not.toHaveBeenCalled();
      expectUnchanged(item, 0, stored);
    },
  );

  it.each(["same session", "replacement session"])(
    "does not lend a fresh page or %s to the old exact confirmation",
    async (session) => {
      const previous = mountHolds();
      const oldConfirm = await openRemove(previous);
      const stored = storageSnapshot();
      unmountCommandTests();
      if (session === "replacement session") previous.rotateSession();
      const current = mountHolds(
        "account",
        session === "replacement session"
          ? "replacement-test-session"
          : "original-test-session",
      );
      const confirm = await openRemove(current);
      const makeKey = vi.spyOn(crypto, "randomUUID");
      oldConfirm();
      oldConfirm();
      await settleUI();
      expectUnchanged(previous, 0, stored);
      expectUnchanged(current, 0, stored);
      expect(makeKey).not.toHaveBeenCalled();
      expect(dialog(current)?.querySelector("input")?.value).toBe(scopeID);
      confirm();
      expectUnchanged(current, 1, stored);
      expect(current.remove).toHaveBeenCalledExactlyOnceWith(
        "account",
        scopeID,
      );
      expect(
        new Headers(current.writes[0].init.headers).get("Authorization"),
      ).toBe(
        session === "replacement session"
          ? "Bearer replacement-test-session"
          : "Bearer original-test-session",
      );
    },
  );

  it("does not lend a same-client remount to the old exact confirmation", async () => {
    const previous = mountHolds();
    const oldConfirm = await openRemove(previous);
    const stored = storageSnapshot();
    unmountCommandTests();
    previous.rotateSession();
    const current = {
      ...previous,
      ...mountCommandTest(
        CompliancePage,
        { client: previous.client },
        { provide: false },
      ),
    };
    const confirm = await openRemove(current);
    const makeKey = vi.spyOn(crypto, "randomUUID");
    oldConfirm();
    await settleUI();
    expectUnchanged(current, 0, stored, 2);
    expect(makeKey).not.toHaveBeenCalled();
    expect(dialog(current)?.querySelector("input")?.value).toBe(scopeID);
    confirm();
    expectUnchanged(current, 1, stored, 2);
    expect(
      new Headers(current.writes[0].init.headers).get("Authorization"),
    ).toBe("Bearer replacement-test-session");
  });

  it.each(outcomes)(
    "does not admit a reselected exact confirmation after mounted %s and unmount",
    async (outcome) => {
      const item = mountHolds();
      const confirm = await openRemove(item);
      const stored = storageSnapshot();
      confirm();
      settleRemove(item, outcome);
      await settleUI();
      expectUnchanged(item, 1, stored, 2);
      const nextConfirm = await openRemove(item);
      const makeKey = vi.spyOn(crypto, "randomUUID");
      unmountCommandTests();
      nextConfirm();
      await settleUI();
      expectUnchanged(item, 1, stored, 2);
      expect(makeKey).not.toHaveBeenCalled();
    },
  );

  it.each(outcomes)(
    "does not cancel an admitted DELETE or reopen old admission after late %s",
    async (outcome) => {
      const previous = mountHolds();
      const oldConfirm = await openRemove(previous);
      const stored = storageSnapshot();
      oldConfirm();
      unmountCommandTests();
      previous.client.cancelPendingReads();
      expect(previous.writes[0].init.signal?.aborted).toBe(false);
      const current = mountHolds();
      await openRemove(current);
      oldConfirm();
      settleRemove(previous, outcome);
      await settleUI();
      oldConfirm();
      await settleUI();
      expect(previous.writes[0].init.signal?.aborted).toBe(false);
      expectUnchanged(previous, 1, stored);
      expectUnchanged(current, 0, stored);
      expect(dialog(current)?.querySelector("input")?.value).toBe(scopeID);
      expect(current.root.textContent).not.toContain("Legal Hold 已解除");
      expect(current.root.textContent).not.toContain(
        "Hold removal unavailable",
      );
      expect(current.root.textContent).not.toContain("bad-hold-marker");
    },
  );

  it.each(["success", "body lost"])(
    "preserves an admitted body through unmount and refuses a later confirm after %s",
    async (outcome) => {
      const item = mountHolds();
      const confirm = await openRemove(item);
      const stored = storageSnapshot();
      confirm();
      let body!: ReadableStreamDefaultController<Uint8Array>;
      item.writes[0].response.resolve(
        new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              body = controller;
            },
          }),
        ),
      );
      cleanups.push(() => body.error(new Error("test stream cleanup")));
      await settleUI();
      confirm();
      expectUnchanged(item, 1, stored);
      unmountCommandTests();
      item.client.cancelPendingReads();
      expect(item.writes[0].init.signal?.aborted).toBe(false);
      confirm();
      if (outcome === "body lost") body.error(new Error("test body lost"));
      else {
        body.enqueue(new TextEncoder().encode('{"data":{"removed":true}}'));
        body.close();
      }
      await settleUI();
      confirm();
      await settleUI();
      expectUnchanged(item, 1, stored);
      expect(item.writes[0].init.signal?.aborted).toBe(false);
    },
  );

  it("still admits only one explicit mounted removal with its original scope and no new key", async () => {
    const item = mountHolds();
    const confirm = await openRemove(item);
    const stored = storageSnapshot();
    expectUnchanged(item, 0, stored);
    confirm();
    confirm();
    expectUnchanged(item, 1, stored);
    const request = item.writes[0];
    expect(request.url.pathname).toBe(
      `/v1/admin/audit/holds/account/${scopeID}`,
    );
    expect(request.url.search).toBe("");
    expect(request.init.method).toBe("DELETE");
    expect(request.init.body).toBeUndefined();
    expect(new Headers(request.init.headers).get("Idempotency-Key")).toBeNull();
    settleRemove(item, "success");
    await settleUI();
    confirm();
    expectUnchanged(item, 1, stored, 2);
    expect(item.root.textContent).toContain("Legal Hold 已解除");
  });
});
