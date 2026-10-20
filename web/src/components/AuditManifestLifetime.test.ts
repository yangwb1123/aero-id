import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import AuditAnchorsPanel from "./AuditAnchorsPanel.vue";
import { mountCommandTest, unmountCommandTests } from "./commandTestFixtures";

let retainedSubmit: (() => void) | undefined;
vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, h } = await import("vue");
  const { irisCommandTestStubs } = await import("./commandTestFixtures");
  return {
    ...irisCommandTestStubs(),
    IrisCard: defineComponent({
      setup:
        (_, { slots }) =>
        () => {
          const children = slots.default?.();
          // Capture the real parent's submit listener, not detached DOM.
          const form = children?.find((child) => child.type === "form");
          const listener = form?.props?.onSubmit;
          if (typeof listener === "function")
            retainedSubmit = () =>
              listener(new Event("submit", { cancelable: true }));
          return h("div", children);
        },
    }),
  };
});

const cleanups: Array<() => void> = [];
const originalSource = "aero/id";
const originalPartition = "tenant:a/system";
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
function manifest(marker = "original-anchor-hash") {
  return {
    source: originalSource,
    partition: originalPartition,
    chain_sequence: 7,
    anchor_hash: marker,
  };
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
function mountManifest() {
  let session = "original-test-session";
  const requests: Array<{
    url: URL;
    headers: Headers;
    signal: AbortSignal | null | undefined;
    response: ReturnType<typeof deferred<Response>>;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.method).toBe("GET");
      expect(init?.body).toBeUndefined();
      expect(init?.credentials).toBe("omit");
      const headers = new Headers(init?.headers);
      expect(headers.get("Idempotency-Key")).toBeNull();
      const response = deferred<Response>();
      cleanups.push(() => response.reject(new Error("test request cleanup")));
      requests.push({
        url: new URL(String(input)),
        headers,
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
  const query = vi.spyOn(client.compliance, "getAuditAnchorManifest");
  const otherCalls = [
    vi.spyOn(client.compliance, "runAuditAnchors"),
    vi.spyOn(client.compliance, "listHolds"),
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
    query,
    fetcher,
    requests,
    otherCalls,
    rotateSession: () => {
      session = "replacement-test-session";
    },
  };
}
type MountedManifest = ReturnType<typeof mountManifest>;
async function fill(
  item: MountedManifest,
  source = ` ${originalSource} `,
  partition = ` ${originalPartition} `,
): Promise<void> {
  const inputs = item.root.querySelectorAll<HTMLInputElement>(
    ".anchor-query-grid input",
  );
  expect(inputs).toHaveLength(2);
  [source, partition].forEach((value, index) => {
    inputs[index].value = value;
    inputs[index].dispatchEvent(new Event("input"));
  });
  await settleUI();
}
async function captureSubmit(item: MountedManifest) {
  await fill(item);
  expect(retainedSubmit).toBeTypeOf("function");
  expect(item.query).not.toHaveBeenCalled();
  return retainedSubmit!;
}
function expectUnchanged(
  item: MountedManifest,
  calls: number,
  stored: ReturnType<typeof storageSnapshot>,
): void {
  expect(item.query).toHaveBeenCalledTimes(calls);
  expect(item.fetcher).toHaveBeenCalledTimes(calls);
  expect(item.requests).toHaveLength(calls);
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(storageSnapshot()).toEqual(stored);
}
const outcomes = [
  "success",
  "denied",
  "audit unavailable",
  "not found",
  "invalid JSON",
  "connection lost",
] as const;
type Outcome = (typeof outcomes)[number];
function settleRequest(item: MountedManifest, outcome: Outcome): void {
  const request = item.requests[0];
  if (outcome === "connection lost") {
    request.response.reject(new TypeError("test connection lost"));
  } else if (outcome === "invalid JSON") {
    request.response.resolve(new Response("bad-manifest-marker"));
  } else if (outcome === "success") {
    request.response.resolve(json(manifest()));
  } else {
    request.response.resolve(
      new Response(
        JSON.stringify({
          error: {
            code: "audit_anchor.read_failed",
            message: "Manifest unavailable",
          },
        }),
        {
          status:
            outcome === "denied" ? 403 : outcome === "not found" ? 404 : 503,
        },
      ),
    );
  }
}

beforeEach(() => {
  retainedSubmit = undefined;
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

describe("sensitive Manifest query admission belongs to the mounted panel", () => {
  it("refuses a captured populated submit after unmount, before client or request-ID generation", async () => {
    const item = mountManifest();
    const submit = await captureSubmit(item);
    const stored = storageSnapshot();
    const makeID = vi.spyOn(crypto, "randomUUID");
    unmountCommandTests();
    submit();
    submit();
    await settleUI();
    expectUnchanged(item, 0, stored);
    expect(makeID).not.toHaveBeenCalled();
    expect(item.root.textContent).toBe("");
  });

  it.each(outcomes)(
    "does not admit another old submit after a mounted %s finishes and the panel unmounts",
    async (outcome) => {
      const item = mountManifest();
      const submit = await captureSubmit(item);
      const stored = storageSnapshot();
      submit();
      expectUnchanged(item, 1, stored);
      settleRequest(item, outcome);
      await settleUI();
      expectUnchanged(item, 1, stored);
      const makeID = vi.spyOn(crypto, "randomUUID");
      unmountCommandTests();
      submit();
      await settleUI();
      expectUnchanged(item, 1, stored);
      expect(makeID).not.toHaveBeenCalled();
    },
  );

  it.each(["same session", "replacement session"])(
    "does not lend a new panel or %s to a retained query listener",
    async (session) => {
      const previous = mountManifest();
      const old = await captureSubmit(previous);
      const stored = storageSnapshot();
      unmountCommandTests();
      if (session === "replacement session") previous.rotateSession();
      const current = mountManifest();
      const submit = await captureSubmit(current);
      const makeID = vi.spyOn(crypto, "randomUUID");
      old();
      await settleUI();
      expectUnchanged(previous, 0, stored);
      expectUnchanged(current, 0, stored);
      expect(makeID).not.toHaveBeenCalled();
      submit();
      expectUnchanged(current, 1, stored);
      expect(current.requests[0].headers.get("Authorization")).toBe(
        "Bearer original-test-session",
      );
      current.requests[0].response.resolve(json(manifest("new-anchor-hash")));
      await settleUI();
      expect(current.root.textContent).toContain("new-anchor-hash");
      expectUnchanged(previous, 0, stored);
    },
  );

  it.each(outcomes)(
    "keeps an admitted GET separate from new admission after unmount and late %s",
    async (outcome) => {
      const previous = mountManifest();
      const old = await captureSubmit(previous);
      const stored = storageSnapshot();
      old();
      expect(previous.query).toHaveBeenCalledExactlyOnceWith(
        originalSource,
        originalPartition,
      );
      unmountCommandTests();
      previous.client.cancelPendingReads();
      expect(previous.requests[0].signal?.aborted).toBe(true);
      const current = mountManifest();
      const submit = await captureSubmit(current);
      const makeID = vi.spyOn(crypto, "randomUUID");
      old();
      expectUnchanged(previous, 1, stored);
      expectUnchanged(current, 0, stored);
      expect(makeID).not.toHaveBeenCalled();
      // This controlled fetch ignores cancellation and can still settle.
      settleRequest(previous, outcome);
      await settleUI();
      old();
      await settleUI();
      expectUnchanged(previous, 1, stored);
      expectUnchanged(current, 0, stored);
      expect(makeID).not.toHaveBeenCalled();
      expect(current.root.textContent).not.toContain("original-anchor-hash");
      expect(current.root.textContent).not.toContain("Manifest unavailable");
      expect(current.root.textContent).not.toContain("bad-manifest-marker");
      submit();
      current.requests[0].response.resolve(json(manifest("new-anchor-hash")));
      await settleUI();
      expect(current.root.textContent).toContain("new-anchor-hash");
      expectUnchanged(previous, 1, stored);
    },
  );

  it.each(["success", "body lost"])(
    "does not reopen old query admission after an already admitted body ends with %s",
    async (outcome) => {
      const item = mountManifest();
      const submit = await captureSubmit(item);
      const stored = storageSnapshot();
      submit();
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
      unmountCommandTests();
      item.client.cancelPendingReads();
      expect(item.requests[0].signal?.aborted).toBe(true);
      submit();
      expectUnchanged(item, 1, stored);
      if (outcome === "body lost") body.error(new Error("test body lost"));
      else {
        body.enqueue(
          new TextEncoder().encode(JSON.stringify({ data: manifest() })),
        );
        body.close();
      }
      await settleUI();
      submit();
      await settleUI();
      expectUnchanged(item, 1, stored);
    },
  );

  it("keeps explicit mounted reads, trimmed encoded target and no automatic query or write", async () => {
    const item = mountManifest();
    const submit = await captureSubmit(item);
    const stored = storageSnapshot();
    await vi.advanceTimersByTimeAsync(60_000);
    expectUnchanged(item, 0, stored);
    submit();
    expect(item.query).toHaveBeenCalledExactlyOnceWith(
      originalSource,
      originalPartition,
    );
    expect(item.requests[0].url.pathname).toBe(
      "/v1/admin/audit/anchor/manifest/aero%2Fid/tenant%3Aa%2Fsystem",
    );
    expect(item.requests[0].url.search).toBe("");
    settleRequest(item, "success");
    await settleUI();
    expect(item.root.textContent).toContain("original-anchor-hash");
    await vi.advanceTimersByTimeAsync(60_000);
    expectUnchanged(item, 1, stored);
    submit();
    expectUnchanged(item, 2, stored);
    item.requests[1].response.resolve(json(manifest("explicit-new-read")));
    await settleUI();
    expect(item.root.textContent).toContain("explicit-new-read");
  });

  it.each([
    { source: " ", partition: originalPartition },
    { source: originalSource, partition: " " },
    { source: "", partition: "" },
  ])(
    "retains mounted empty-target validation for $source / $partition",
    async (target) => {
      const item = mountManifest();
      const submit = await captureSubmit(item);
      await fill(item, target.source, target.partition);
      const stored = storageSnapshot();
      submit();
      await settleUI();
      expectUnchanged(item, 0, stored);
      expect(item.root.textContent).toContain("Source 和 Partition 均不能为空");
      await fill(item);
      expectUnchanged(item, 0, stored);
      submit();
      item.requests[0].response.resolve(json(manifest()));
      await settleUI();
      expectUnchanged(item, 1, stored);
      expect(item.root.textContent).toContain("original-anchor-hash");
    },
  );

  it("retains the original 30-second GET deadline without automatically retrying", async () => {
    const item = mountManifest();
    const submit = await captureSubmit(item);
    const stored = storageSnapshot();
    submit();
    item.requests[0].signal?.addEventListener("abort", () =>
      item.requests[0].response.reject(new Error("test fetch abort")),
    );
    await vi.advanceTimersByTimeAsync(29_999);
    expect(item.requests[0].signal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await settleUI();
    expect(item.requests[0].signal?.aborted).toBe(true);
    expect(item.root.textContent).toContain("aero-id 请求超时");
    await vi.advanceTimersByTimeAsync(60_000);
    expectUnchanged(item, 1, stored);
    unmountCommandTests();
    submit();
    await settleUI();
    expectUnchanged(item, 1, stored);
  });
});
