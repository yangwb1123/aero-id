import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import AuditAnchorsPanel from "./AuditAnchorsPanel.vue";
import {
  commandButton,
  mountCommandTest,
  unmountCommandTests,
} from "./commandTestFixtures";
import { anchorRunConfirmation } from "./dialogTypes";

let retainedConfirm: (() => void) | undefined;
vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, getCurrentInstance, h } = await import("vue");
  const { irisCommandTestStubs } = await import("./commandTestFixtures");
  return {
    ...irisCommandTestStubs(),
    IrisDialog: defineComponent({
      props: { open: Boolean },
      setup(props, { slots }) {
        // Capture the real AnchorRunDialog parent's confirm listener, not a
        // click on a disabled button or a replacement dialog implementation.
        const listener = getCurrentInstance()?.parent?.vnode.props?.onConfirm;
        if (typeof listener === "function") retainedConfirm = () => listener();
        return () =>
          props.open
            ? h("div", { "data-anchor-dialog": "" }, slots.default?.())
            : null;
      },
    }),
  };
});

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
function mountAnchors() {
  const requests: Array<{
    key: string | null;
    signal: AbortSignal | null | undefined;
    response: ReturnType<typeof deferred<Response>>;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      expect(url.pathname).toBe("/v1/admin/audit/anchor");
      expect(url.search).toBe("");
      expect(init?.method).toBe("POST");
      expect(init?.body).toBeUndefined();
      const response = deferred<Response>();
      cleanups.push(() => response.reject(new Error("test request cleanup")));
      requests.push({
        key: new Headers(init?.headers).get("Idempotency-Key"),
        signal: init?.signal,
        response,
      });
      return response.promise;
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-session",
    fetcher,
  );
  const run = vi.spyOn(client.compliance, "runAuditAnchors");
  const otherCalls = [
    vi.spyOn(client.compliance, "getAuditAnchorManifest"),
    vi.spyOn(client.compliance, "putHold"),
    vi.spyOn(client.compliance, "removeHold"),
    vi.spyOn(client.compliance, "createAuditExport"),
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client.commands, "abandon"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "downloadExport"),
  ];
  const makeKey = vi.spyOn(crypto, "randomUUID");
  return {
    ...mountCommandTest(AuditAnchorsPanel, { client }, { provide: false }),
    run,
    fetcher,
    requests,
    otherCalls,
    makeKey,
  };
}
type MountedAnchors = ReturnType<typeof mountAnchors>;
function dialog(item: MountedAnchors): HTMLElement | null {
  return item.root.querySelector("[data-anchor-dialog]");
}
function confirmationInput(item: MountedAnchors): HTMLInputElement {
  const input = dialog(item)?.querySelector("input");
  if (!input) throw new Error("missing original anchor confirmation input");
  return input;
}
async function fill(item: MountedAnchors, phrase: string): Promise<void> {
  const input = confirmationInput(item);
  input.value = phrase;
  input.dispatchEvent(new Event("input"));
  await settleUI();
}
async function open(item: MountedAnchors) {
  commandButton(item.root, "锚定全部当前审计链").click();
  await settleUI();
  expect(retainedConfirm).toBeTypeOf("function");
  expect(item.makeKey).toHaveBeenCalledOnce();
  const key = item.makeKey.mock.results[0].value;
  expect(key).toBeTypeOf("string");
  return { key, confirm: retainedConfirm! };
}
function expectUnchanged(
  item: MountedAnchors,
  count: number,
  stored: ReturnType<typeof storageSnapshot>,
): void {
  expect(item.run).toHaveBeenCalledTimes(count);
  expect(item.fetcher).toHaveBeenCalledTimes(count);
  expect(item.requests).toHaveLength(count);
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

describe("all-chain anchor handler requires the exact current phrase", () => {
  it.each([
    { name: "empty", phrase: "" },
    { name: "generic confirmation", phrase: "确认" },
    { name: "lowercase", phrase: "anchor all chains" },
    { name: "singular chain", phrase: "ANCHOR ALL CHAIN" },
    { name: "leading space", phrase: ` ${anchorRunConfirmation}` },
    { name: "trailing space", phrase: `${anchorRunConfirmation} ` },
    { name: "mixed case", phrase: "Anchor ALL CHAINS" },
    { name: "double internal space", phrase: "ANCHOR  ALL CHAINS" },
    { name: "nonbreaking space", phrase: "ANCHOR\u00a0ALL CHAINS" },
    { name: "zero-width suffix", phrase: `${anchorRunConfirmation}\u200b` },
  ])("refuses $name before calling the original client", async ({ phrase }) => {
    const item = mountAnchors();
    const { key, confirm } = await open(item);
    const stored = storageSnapshot();
    await fill(item, phrase);
    expect(commandButton(item.root, "验证全部链并写入锚点").disabled).toBe(
      true,
    );
    expectUnchanged(item, 0, stored);
    confirm();
    confirm();
    await settleUI();
    expectUnchanged(item, 0, stored);
    expect(item.makeKey).toHaveBeenCalledOnce();
    expect(confirmationInput(item).value).toBe(phrase);
    expect(dialog(item)).not.toBeNull();
    expect(item.root.textContent).not.toContain("本次已写入");
    // Correcting input is not a command. A separate explicit confirmation uses
    // the original key, with no new intent or storage/manifest operation.
    await fill(item, anchorRunConfirmation);
    expectUnchanged(item, 0, stored);
    confirm();
    expectUnchanged(item, 1, stored);
    expect(item.run).toHaveBeenCalledWith(key);
    expect(item.requests[0].key).toBe(key);
    item.requests[0].response.resolve(json({ anchored: 0, chains: [] }));
    await settleUI();
    expect(dialog(item)).toBeNull();
    expect(item.root.textContent).toContain("本次已写入");
    confirm();
    await settleUI();
    expectUnchanged(item, 1, stored);
  });

  it("rechecks the current input even for a listener retained while the exact phrase was present", async () => {
    const item = mountAnchors();
    const { key, confirm } = await open(item);
    const stored = storageSnapshot();
    await fill(item, anchorRunConfirmation);
    expect(commandButton(item.root, "验证全部链并写入锚点").disabled).toBe(
      false,
    );
    await fill(item, "");
    confirm();
    await settleUI();
    expectUnchanged(item, 0, stored);
    await fill(item, anchorRunConfirmation);
    expectUnchanged(item, 0, stored);
    confirm();
    expect(item.requests[0].key).toBe(key);
    expectUnchanged(item, 1, stored);
  });

  it("still admits one explicit exact confirmation and never queues repeated confirms during fetch or body read", async () => {
    const item = mountAnchors();
    const { key, confirm } = await open(item);
    const stored = storageSnapshot();
    await fill(item, anchorRunConfirmation);
    expectUnchanged(item, 0, stored);
    commandButton(item.root, "验证全部链并写入锚点").click();
    confirm();
    await settleUI();
    expectUnchanged(item, 1, stored);
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
    cleanups.push(() => body.error(new Error("test stream cleanup")));
    await settleUI();
    confirm();
    await fill(item, "not confirmed anymore");
    confirm();
    expectUnchanged(item, 1, stored);
    expect(item.requests[0].signal?.aborted).toBe(false);
    body.enqueue(
      new TextEncoder().encode(
        JSON.stringify({ data: { anchored: 2, chains: [] } }),
      ),
    );
    body.close();
    await settleUI();
    confirm();
    await settleUI();
    expectUnchanged(item, 1, stored);
    expect(item.requests[0].key).toBe(key);
    expect(dialog(item)).toBeNull();
    expect(item.root.textContent).toContain("2 个锚点");
  });

  it.each([
    "denied",
    "audit unavailable",
    "invalid JSON",
    "lost acknowledgment",
  ])(
    "retains input/key after %s, but refuses a changed phrase before any explicit same-key retry",
    async (outcome) => {
      const item = mountAnchors();
      const { key, confirm } = await open(item);
      const stored = storageSnapshot();
      await fill(item, anchorRunConfirmation);
      confirm();
      expectUnchanged(item, 1, stored);
      if (outcome === "lost acknowledgment") {
        item.requests[0].response.reject(
          new TypeError("test lost acknowledgment"),
        );
      } else if (outcome === "invalid JSON") {
        item.requests[0].response.resolve(new Response("bad-json-marker"));
      } else {
        item.requests[0].response.resolve(
          new Response(
            JSON.stringify({
              error: {
                code: "audit_anchor.unavailable",
                message: "Anchor unavailable",
              },
            }),
            { status: outcome === "denied" ? 403 : 503 },
          ),
        );
      }
      await settleUI();
      expectUnchanged(item, 1, stored);
      expect(confirmationInput(item).value).toBe(anchorRunConfirmation);
      expect(item.root.textContent).not.toContain("本次已写入");
      const errorText = dialog(item)?.textContent;
      await fill(item, "ANCHOR");
      confirm();
      await settleUI();
      expectUnchanged(item, 1, stored);
      expect(dialog(item)?.textContent).toBe(errorText);
      expect(item.root.textContent).not.toContain("bad-json-marker");
      expect(item.makeKey).toHaveBeenCalledTimes(2); // intent + first request ID
      await fill(item, anchorRunConfirmation);
      expectUnchanged(item, 1, stored);
      confirm();
      expectUnchanged(item, 2, stored);
      expect(item.run.mock.calls).toEqual([[key], [key]]);
      expect(item.requests.map((request) => request.key)).toEqual([key, key]);
      item.requests[1].response.resolve(json({ anchored: 0, chains: [] }));
      await settleUI();
      expectUnchanged(item, 2, stored);
    },
  );

  it("requires a fresh exact phrase after cancel/reopen while retaining the original in-memory intent key", async () => {
    const item = mountAnchors();
    const { key, confirm } = await open(item);
    const stored = storageSnapshot();
    await fill(item, anchorRunConfirmation);
    commandButton(item.root, "取消").click();
    await settleUI();
    confirm();
    expectUnchanged(item, 0, stored);
    commandButton(item.root, "锚定全部当前审计链").click();
    await settleUI();
    expect(confirmationInput(item).value).toBe("");
    confirm();
    await settleUI();
    expectUnchanged(item, 0, stored);
    expect(item.makeKey).toHaveBeenCalledOnce();
    await fill(item, anchorRunConfirmation);
    expectUnchanged(item, 0, stored);
    confirm();
    expect(item.requests[0].key).toBe(key);
    expectUnchanged(item, 1, stored);
  });

  it("does not create a key or call the client when confirmation is invoked before opening", async () => {
    const item = mountAnchors();
    const stored = storageSnapshot();
    expect(retainedConfirm).toBeTypeOf("function");
    retainedConfirm!();
    await settleUI();
    expectUnchanged(item, 0, stored);
    expect(item.makeKey).not.toHaveBeenCalled();
    expect(dialog(item)).toBeNull();
  });
});
