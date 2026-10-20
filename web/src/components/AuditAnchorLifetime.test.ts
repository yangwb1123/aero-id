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

let retainedOpen: (() => void) | undefined;
let retainedConfirm: (() => void) | undefined;
vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, getCurrentInstance, h } = await import("vue");
  const { irisCommandTestStubs } = await import("./commandTestFixtures");
  return {
    ...irisCommandTestStubs(),
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
                child.children.trim() === "锚定全部当前审计链",
            )
          )
            retainedOpen = click;
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
        // Retain the original parent's listener, not detached/disabled DOM.
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
  let session = "original-test-session";
  const requests: Array<{
    key: string | null;
    session: string | null;
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
      const headers = new Headers(init?.headers);
      requests.push({
        key: headers.get("Idempotency-Key"),
        session: headers.get("Authorization"),
        signal: init?.signal,
        response,
      });
      return response.promise;
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => session,
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
  return {
    ...mountCommandTest(AuditAnchorsPanel, { client }, { provide: false }),
    client,
    run,
    fetcher,
    requests,
    otherCalls,
    rotateSession: () => {
      session = "replacement-test-session";
    },
  };
}
type MountedAnchors = ReturnType<typeof mountAnchors>;
function confirmationInput(item: MountedAnchors): HTMLInputElement {
  const input = item.root.querySelector<HTMLInputElement>(
    "[data-anchor-dialog] input",
  );
  if (!input) throw new Error("missing original anchor confirmation input");
  return input;
}
async function fill(item: MountedAnchors, phrase: string): Promise<void> {
  const input = confirmationInput(item);
  input.value = phrase;
  input.dispatchEvent(new Event("input"));
  await settleUI();
}
async function captureRun(item: MountedAnchors) {
  expect(retainedOpen).toBeTypeOf("function");
  const open = retainedOpen!;
  commandButton(item.root, "锚定全部当前审计链").click();
  await settleUI();
  await fill(item, anchorRunConfirmation);
  expect(commandButton(item.root, "验证全部链并写入锚点").disabled).toBe(false);
  expect(retainedConfirm).toBeTypeOf("function");
  expect(item.run).not.toHaveBeenCalled();
  return { open, confirm: retainedConfirm! };
}
function expectUnchanged(
  item: MountedAnchors,
  calls: number,
  stored: ReturnType<typeof storageSnapshot>,
): void {
  expect(item.run).toHaveBeenCalledTimes(calls);
  expect(item.fetcher).toHaveBeenCalledTimes(calls);
  expect(item.requests).toHaveLength(calls);
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
function settleRequest(item: MountedAnchors, outcome: Outcome): void {
  const request = item.requests[0];
  if (outcome === "lost acknowledgment") {
    request.response.reject(new TypeError("test acknowledgment lost"));
  } else if (outcome === "invalid JSON") {
    request.response.resolve(new Response("bad-anchor-marker"));
  } else if (outcome === "success") {
    request.response.resolve(json({ anchored: 1, chains: [] }));
  } else {
    request.response.resolve(
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
}

beforeEach(() => {
  retainedOpen = undefined;
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

describe("all-chain anchor admission belongs to the mounted panel", () => {
  it("does not generate an intent key when a retained open listener is invoked after unmount", async () => {
    const item = mountAnchors();
    expect(retainedOpen).toBeTypeOf("function");
    const open = retainedOpen!;
    const makeKey = vi.spyOn(crypto, "randomUUID");
    const stored = storageSnapshot();
    unmountCommandTests();
    open();
    open();
    retainedConfirm!();
    await settleUI();
    expectUnchanged(item, 0, stored);
    expect(makeKey).not.toHaveBeenCalled();
  });

  it("refuses an exact confirmed listener after unmount, without replacing the original key", async () => {
    const item = mountAnchors();
    const { confirm } = await captureRun(item);
    const stored = storageSnapshot();
    const makeKey = vi.spyOn(crypto, "randomUUID");
    unmountCommandTests();
    confirm();
    confirm();
    await settleUI();
    expectUnchanged(item, 0, stored);
    expect(makeKey).not.toHaveBeenCalled();
    expect(item.root.textContent).toBe("");
  });

  it.each(outcomes)(
    "does not admit another old confirm after a mounted %s finishes and the panel unmounts",
    async (outcome) => {
      const item = mountAnchors();
      const { confirm } = await captureRun(item);
      const stored = storageSnapshot();
      confirm();
      settleRequest(item, outcome);
      await settleUI();
      expectUnchanged(item, 1, stored);
      const makeKey = vi.spyOn(crypto, "randomUUID");
      unmountCommandTests();
      confirm();
      await settleUI();
      expectUnchanged(item, 1, stored);
      expect(makeKey).not.toHaveBeenCalled();
    },
  );

  it.each(["same session", "replacement session"])(
    "does not lend a fresh panel or %s to the old exact-confirmed listener",
    async (session) => {
      const previous = mountAnchors();
      const old = await captureRun(previous);
      const stored = storageSnapshot();
      unmountCommandTests();
      if (session === "replacement session") previous.rotateSession();
      const current = mountAnchors();
      const handlers = await captureRun(current);
      const makeKey = vi.spyOn(crypto, "randomUUID");
      old.confirm();
      old.open();
      old.confirm();
      await settleUI();
      expectUnchanged(previous, 0, stored);
      expectUnchanged(current, 0, stored);
      expect(makeKey).not.toHaveBeenCalled();
      expect(confirmationInput(current).value).toBe(anchorRunConfirmation);
      handlers.confirm();
      expectUnchanged(current, 1, stored);
      expect(current.requests[0].session).toBe("Bearer original-test-session");
      expect(current.requests[0].key).toBeTypeOf("string");
      current.requests[0].response.resolve(json({ anchored: 0, chains: [] }));
      await settleUI();
      expectUnchanged(previous, 0, stored);
    },
  );

  it.each(outcomes)(
    "does not revoke an admitted POST on unmount or reopen old admission after its late %s",
    async (outcome) => {
      const previous = mountAnchors();
      const old = await captureRun(previous);
      const stored = storageSnapshot();
      old.confirm();
      const key = previous.requests[0].key;
      expect(previous.run).toHaveBeenCalledExactlyOnceWith(key);
      previous.client.cancelPendingReads();
      unmountCommandTests();
      expect(previous.requests[0].signal?.aborted).toBe(false);
      const current = mountAnchors();
      await captureRun(current);
      await fill(current, "new unconfirmed input");
      const makeKey = vi.spyOn(crypto, "randomUUID");
      old.confirm();
      expectUnchanged(previous, 1, stored);
      settleRequest(previous, outcome);
      await settleUI();
      expect(previous.requests[0].signal?.aborted).toBe(false);
      old.confirm();
      await settleUI();
      expectUnchanged(previous, 1, stored);
      expectUnchanged(current, 0, stored);
      expect(makeKey).not.toHaveBeenCalled();
      expect(confirmationInput(current).value).toBe("new unconfirmed input");
      expect(current.root.textContent).not.toContain("本次已写入");
      expect(current.root.textContent).not.toContain("Anchor unavailable");
      expect(current.root.textContent).not.toContain("bad-anchor-marker");
    },
  );

  it.each(["success", "body lost"])(
    "keeps an admitted response body independent of unmount and refuses a new old confirm after %s",
    async (outcome) => {
      const item = mountAnchors();
      const { confirm } = await captureRun(item);
      const stored = storageSnapshot();
      confirm();
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
      expectUnchanged(item, 1, stored);
      unmountCommandTests();
      item.client.cancelPendingReads();
      expect(item.requests[0].signal?.aborted).toBe(false);
      confirm();
      expectUnchanged(item, 1, stored);
      if (outcome === "body lost") body.error(new Error("test body lost"));
      else {
        body.enqueue(
          new TextEncoder().encode(
            JSON.stringify({ data: { anchored: 1, chains: [] } }),
          ),
        );
        body.close();
      }
      await settleUI();
      confirm();
      await settleUI();
      expectUnchanged(item, 1, stored);
    },
  );

  it("still admits one explicit mounted exact confirmation with a fixed key and no queued repeat", async () => {
    const item = mountAnchors();
    const makeKey = vi.spyOn(crypto, "randomUUID");
    const { confirm } = await captureRun(item);
    const key = makeKey.mock.results[0].value;
    const stored = storageSnapshot();
    expectUnchanged(item, 0, stored);
    confirm();
    confirm();
    await settleUI();
    expectUnchanged(item, 1, stored);
    expect(item.run).toHaveBeenCalledExactlyOnceWith(key);
    expect(item.requests[0].key).toBe(key);
    item.requests[0].response.resolve(json({ anchored: 0, chains: [] }));
    await settleUI();
    confirm();
    expectUnchanged(item, 1, stored);
    expect(item.root.textContent).toContain("本次已写入");
  });
});
