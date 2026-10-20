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
        // Retain the actual ComplianceCommandDialog parent's confirm listener,
        // not a disabled button click or a replacement dialog implementation.
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

const originalScopeID = "Original-Account-Alpha";
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
function hold(scopeType: AuditHoldScope, scopeID: string) {
  return {
    scope_type: scopeType,
    scope_id: scopeID,
    reason: "preserve-original-evidence",
    created_by: "operator",
    created_at: "2026-10-16T01:00:00Z",
    expires_at: null,
  };
}
function mountHolds(
  scopeType: AuditHoldScope = "account",
  scopeID = originalScopeID,
) {
  const rows = [hold(scopeType, scopeID), hold("actor", "Other-Actor")];
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
      if (!init) throw new Error("missing original write options");
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
    rows,
    reads,
    writes,
    fetcher,
    remove,
    put,
    auditExport,
    otherCalls,
  };
}
type MountedHolds = ReturnType<typeof mountHolds>;
function dialog(item: MountedHolds): HTMLElement | null {
  return item.root.querySelector("[data-compliance-dialog]");
}
async function fill(item: MountedHolds, value: string): Promise<void> {
  const input = dialog(item)?.querySelector("input");
  if (!input) throw new Error("missing original Scope ID confirmation input");
  input.value = value;
  input.dispatchEvent(new Event("input"));
  await settleUI();
}
async function openRemove(item: MountedHolds, index = 0) {
  await settleUI();
  const row = item.root.querySelectorAll("tbody tr")[index];
  if (!row) throw new Error("missing original hold row");
  commandButton(row as HTMLElement, "解除").click();
  await settleUI();
  expect(retainedConfirm).toBeTypeOf("function");
  expect(dialog(item)?.textContent).toContain("确认解除 Legal Hold");
  return retainedConfirm!;
}
function expectRemoveCount(
  item: MountedHolds,
  count: number,
  stored: ReturnType<typeof storageSnapshot>,
  readCount = 1,
): void {
  expect(item.remove).toHaveBeenCalledTimes(count);
  expect(item.writes).toHaveLength(count);
  expect(item.reads).toHaveLength(readCount);
  expect(item.fetcher).toHaveBeenCalledTimes(readCount + count);
  expect(item.put).not.toHaveBeenCalled();
  expect(item.auditExport).not.toHaveBeenCalled();
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(storageSnapshot()).toEqual(stored);
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

describe("Legal Hold removal handler requires the exact current Scope ID", () => {
  it.each([
    { name: "empty", phrase: "" },
    { name: "generic confirmation", phrase: "确认" },
    { name: "wrong row", phrase: "Other-Actor" },
    { name: "lowercase", phrase: originalScopeID.toLowerCase() },
    { name: "uppercase", phrase: originalScopeID.toUpperCase() },
    { name: "scope prefix", phrase: `account:${originalScopeID}` },
    { name: "leading space", phrase: ` ${originalScopeID}` },
    { name: "trailing space", phrase: `${originalScopeID} ` },
    { name: "lookalike separator", phrase: "Original–Account-Alpha" },
    { name: "nonbreaking space", phrase: `${originalScopeID}\u00a0` },
    { name: "zero-width suffix", phrase: `${originalScopeID}\u200b` },
  ])("refuses $name before calling the original client", async ({ phrase }) => {
    const item = mountHolds();
    const confirm = await openRemove(item);
    const stored = storageSnapshot();
    await fill(item, phrase);
    expect(commandButton(item.root, "确认解除保全").disabled).toBe(true);
    confirm();
    confirm();
    await settleUI();
    expectRemoveCount(item, 0, stored);
    expect(dialog(item)?.querySelector("input")?.value).toBe(phrase);
    expect(item.root.textContent).not.toContain("Legal Hold 已解除");
    // Correcting the input is not a command; a separate explicit confirm
    // continues the original scope without generating a key or new intent.
    await fill(item, originalScopeID);
    expectRemoveCount(item, 0, stored);
    confirm();
    expectRemoveCount(item, 1, stored);
    expect(item.remove).toHaveBeenCalledWith("account", originalScopeID);
  });

  it("rechecks the latest input after exact confirmation was cleared or changed", async () => {
    const item = mountHolds();
    const confirm = await openRemove(item);
    const stored = storageSnapshot();
    await fill(item, originalScopeID);
    expect(commandButton(item.root, "确认解除保全").disabled).toBe(false);
    for (const value of ["", "Other-Actor", ` ${originalScopeID}`]) {
      await fill(item, value);
      confirm();
      await settleUI();
      expectRemoveCount(item, 0, stored);
      await fill(item, originalScopeID);
    }
    expectRemoveCount(item, 0, stored);
    confirm();
    expectRemoveCount(item, 1, stored);
  });

  it.each<AuditHoldScope>(["tenant", "account", "actor"])(
    "preserves %s scope and requires a separate exact explicit confirmation",
    async (scopeType) => {
      const scopeID = "Opaque Target:Alpha/百分比%?";
      const item = mountHolds(scopeType, scopeID);
      const confirm = await openRemove(item);
      const stored = storageSnapshot();
      await fill(item, scopeID);
      expectRemoveCount(item, 0, stored);
      commandButton(item.root, "确认解除保全").click();
      expectRemoveCount(item, 1, stored);
      expect(item.remove).toHaveBeenCalledWith(scopeType, scopeID);
      expect(item.writes[0].url.pathname).toBe(
        `/v1/admin/audit/holds/${scopeType}/${encodeURIComponent(scopeID)}`,
      );
      expect(item.writes[0].url.search).toBe("");
      expect(item.writes[0].init.method).toBe("DELETE");
      expect(item.writes[0].init.body).toBeUndefined();
      expect(
        new Headers(item.writes[0].init.headers).get("Idempotency-Key"),
      ).toBeNull();
      confirm();
      item.writes[0].response.resolve(json({ removed: true }));
      await settleUI();
      expectRemoveCount(item, 1, stored, 2);
      expect(dialog(item)).toBeNull();
      expect(item.root.textContent).toContain("Legal Hold 已解除");
      confirm();
      await vi.advanceTimersByTimeAsync(60_000);
      expectRemoveCount(item, 1, stored, 2);
    },
  );

  it("requires fresh exact input after cancel/reopen and when selecting another row", async () => {
    const item = mountHolds();
    const confirm = await openRemove(item);
    const stored = storageSnapshot();
    await fill(item, originalScopeID);
    commandButton(dialog(item)!, "取消").click();
    await settleUI();
    confirm();
    expectRemoveCount(item, 0, stored);
    await openRemove(item);
    expect(dialog(item)?.querySelector("input")?.value).toBe("");
    confirm();
    await settleUI();
    expectRemoveCount(item, 0, stored);
    commandButton(dialog(item)!, "取消").click();
    await openRemove(item, 1);
    await fill(item, originalScopeID);
    confirm();
    await settleUI();
    expectRemoveCount(item, 0, stored);
    await fill(item, "Other-Actor");
    expectRemoveCount(item, 0, stored);
    confirm();
    expectRemoveCount(item, 1, stored);
    expect(item.remove).toHaveBeenCalledWith("actor", "Other-Actor");
  });

  it("keeps the original single in-flight guard through fetch/body and never queues changed input", async () => {
    const item = mountHolds();
    const confirm = await openRemove(item);
    const stored = storageSnapshot();
    await fill(item, originalScopeID);
    confirm();
    confirm();
    expectRemoveCount(item, 1, stored);
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
    await fill(item, "not confirmed anymore");
    confirm();
    expectRemoveCount(item, 1, stored);
    expect(item.writes[0].init.signal?.aborted).toBe(false);
    body.enqueue(new TextEncoder().encode('{"data":{"removed":true}}'));
    body.close();
    await settleUI();
    expectRemoveCount(item, 1, stored, 2);
    expect(dialog(item)).toBeNull();
    confirm();
    await settleUI();
    expectRemoveCount(item, 1, stored, 2);
  });

  it.each(["denied", "audit unavailable", "invalid JSON", "lost response"])(
    "does not retry after %s; a reselected hold needs new explicit confirmation",
    async (failure) => {
      const item = mountHolds();
      const confirm = await openRemove(item);
      const stored = storageSnapshot();
      await fill(item, originalScopeID);
      confirm();
      if (failure === "lost response") {
        item.writes[0].response.reject(
          new TypeError("controlled response loss"),
        );
      } else if (failure === "invalid JSON") {
        item.writes[0].response.resolve(new Response("invalid JSON"));
      } else {
        item.writes[0].response.resolve(
          new Response(
            JSON.stringify({
              error: {
                code: "audit_hold.remove_failed",
                message: "controlled hold removal refusal",
              },
            }),
            { status: failure === "denied" ? 403 : 503 },
          ),
        );
      }
      await settleUI();
      expect(dialog(item)).toBeNull();
      expect(item.root.textContent).not.toContain("Legal Hold 已解除");
      expectRemoveCount(item, 1, stored, 2);
      confirm();
      await vi.advanceTimersByTimeAsync(60_000);
      expectRemoveCount(item, 1, stored, 2);
      await openRemove(item);
      expect(dialog(item)?.querySelector("input")?.value).toBe("");
      confirm();
      await settleUI();
      expectRemoveCount(item, 1, stored, 2);
      await fill(item, originalScopeID);
      expectRemoveCount(item, 1, stored, 2);
      confirm();
      expectRemoveCount(item, 2, stored, 2);
      expect(item.remove.mock.calls).toEqual([
        ["account", originalScopeID],
        ["account", originalScopeID],
      ]);
    },
  );

  it.each(["put", "export"])(
    "does not add a Scope ID phrase requirement to the existing %s confirmation",
    async (kind) => {
      const item = mountHolds();
      await settleUI();
      const stored = storageSnapshot();
      if (kind === "put") {
        const form = item.root.querySelector(".compliance-create-grid");
        const input = form?.querySelector("input");
        const reason = form?.querySelector("textarea");
        if (!form || !input || !reason)
          throw new Error("missing original form");
        input.value = "New-Hold-Target";
        input.dispatchEvent(new Event("input"));
        reason.value = "preserve-approved-evidence";
        reason.dispatchEvent(new Event("input"));
        form.dispatchEvent(new Event("submit", { cancelable: true }));
      } else {
        commandButton(item.root, "创建审计导出").click();
      }
      await settleUI();
      expect(dialog(item)?.querySelector("input")).toBeNull();
      expect(item.writes).toHaveLength(0);
      commandButton(dialog(item)!, "确认执行").click();
      expect(item.writes).toHaveLength(1);
      expect(item.writes[0].init.method).toBe("POST");
      expect(item.writes[0].url.pathname).toBe(
        kind === "put" ? "/v1/admin/audit/holds" : "/v1/audit/export",
      );
      expect(
        new Headers(item.writes[0].init.headers).get("Idempotency-Key"),
      ).toBeTruthy();
      expect(item.remove).not.toHaveBeenCalled();
      expect(item.put).toHaveBeenCalledTimes(kind === "put" ? 1 : 0);
      expect(item.auditExport).toHaveBeenCalledTimes(kind === "export" ? 1 : 0);
      for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
      expect(storageSnapshot()).toEqual(stored);
    },
  );
});
