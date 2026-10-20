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
const source = "aero/id";
const partition = "tenant:a/system";
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
function json(
  marker = "original-anchor-hash",
  nextSource = source,
  nextPartition = partition,
): Response {
  return new Response(
    JSON.stringify({
      data: {
        source: nextSource,
        partition: nextPartition,
        chain_sequence: 7,
        anchor_hash: marker,
      },
    }),
  );
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
  const requests: Array<{
    url: URL;
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
    stored: storageSnapshot(),
  };
}
type MountedManifest = ReturnType<typeof mountManifest>;
async function fill(
  item: MountedManifest,
  nextSource = ` ${source} `,
  nextPartition = ` ${partition} `,
): Promise<void> {
  const inputs = item.root.querySelectorAll<HTMLInputElement>(
    ".anchor-query-grid input",
  );
  expect(inputs).toHaveLength(2);
  [nextSource, nextPartition].forEach((value, index) => {
    inputs[index].value = value;
    inputs[index].dispatchEvent(new Event("input"));
  });
  await settleUI();
}
async function captureSubmit(item: MountedManifest): Promise<() => void> {
  await fill(item);
  expect(retainedSubmit).toBeTypeOf("function");
  expect(item.query).not.toHaveBeenCalled();
  return retainedSubmit!;
}
function expectCalls(item: MountedManifest, count: number): void {
  expect(item.query).toHaveBeenCalledTimes(count);
  expect(item.fetcher).toHaveBeenCalledTimes(count);
  expect(item.requests).toHaveLength(count);
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(storageSnapshot()).toEqual(item.stored);
}
function startBody(item: MountedManifest) {
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
  cleanups.push(() => body.error(new Error("test body cleanup")));
  return body;
}
const failures = [
  { label: "denied", status: 403 },
  { label: "audit unavailable", status: 503 },
  { label: "not found", status: 404 },
  { label: "invalid JSON", status: 0 },
  { label: "missing data", status: -1 },
  { label: "connection lost", status: -2 },
];
function failRequest(item: MountedManifest, status: number): void {
  const response = item.requests[0].response;
  if (status === -2) response.reject(new TypeError("test connection lost"));
  else if (status === -1) response.resolve(new Response("{}"));
  else if (status === 0) response.resolve(new Response("bad-manifest-marker"));
  else
    response.resolve(
      new Response(
        JSON.stringify({
          error: {
            code: "audit_anchor.read_failed",
            message: "Manifest unavailable",
          },
        }),
        { status },
      ),
    );
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

describe("one explicit sensitive Manifest read at a time per mounted panel", () => {
  it("ignores same-turn and later submits before generating another request ID", async () => {
    const item = mountManifest();
    const submit = await captureSubmit(item);
    const makeID = vi.spyOn(crypto, "randomUUID");
    submit();
    submit();
    submit();
    expectCalls(item, 1);
    expect(makeID).toHaveBeenCalledTimes(1);
    await settleUI();
    submit();
    expectCalls(item, 1);
    expect(makeID).toHaveBeenCalledTimes(1);
    expect(item.requests[0].signal?.aborted).toBe(false);
    expect(item.query).toHaveBeenCalledExactlyOnceWith(source, partition);
    expect(item.requests[0].url.pathname).toBe(
      "/v1/admin/audit/anchor/manifest/aero%2Fid/tenant%3Aa%2Fsystem",
    );
    item.requests[0].response.resolve(json());
    await settleUI();
    expect(item.root.textContent).toContain("original-anchor-hash");
    await vi.advanceTimersByTimeAsync(60_000);
    expectCalls(item, 1);
    submit();
    expectCalls(item, 2);
    expect(makeID).toHaveBeenCalledTimes(2);
  });

  it.each([
    {
      label: "changed target",
      source: "new/source",
      partition: "tenant:b/system",
    },
    { label: "blank source", source: " ", partition },
    { label: "blank partition", source, partition: " " },
  ])(
    "does not queue or validate a $label while fetch is pending",
    async (target) => {
      const item = mountManifest();
      const submit = await captureSubmit(item);
      submit();
      await fill(item, target.source, target.partition);
      submit();
      await settleUI();
      expectCalls(item, 1);
      expect(item.root.textContent).not.toContain(
        "Source 和 Partition 均不能为空",
      );
      expect(item.requests[0].signal?.aborted).toBe(false);
      item.requests[0].response.resolve(json());
      await settleUI();
      expect(item.root.textContent).toContain("original-anchor-hash");
      expectCalls(item, 1);
      await vi.advanceTimersByTimeAsync(60_000);
      expectCalls(item, 1);
      submit();
      await settleUI();
      if (target.label === "changed target") {
        expectCalls(item, 2);
        expect(item.query).toHaveBeenLastCalledWith(
          target.source,
          target.partition,
        );
        expect(item.requests[1].url.pathname).toBe(
          "/v1/admin/audit/anchor/manifest/new%2Fsource/tenant%3Ab%2Fsystem",
        );
      } else {
        expectCalls(item, 1);
        expect(item.root.textContent).toContain(
          "Source 和 Partition 均不能为空",
        );
      }
    },
  );

  it.each(["success", "body lost", "invalid JSON"])(
    "keeps the single read until its response body ends with %s",
    async (outcome) => {
      const item = mountManifest();
      const submit = await captureSubmit(item);
      submit();
      const body = startBody(item);
      await settleUI();
      body.enqueue(new TextEncoder().encode('{"data":'));
      await settleUI();
      submit();
      submit();
      await settleUI();
      expectCalls(item, 1);
      expect(item.requests[0].signal?.aborted).toBe(false);
      expect(item.root.textContent).not.toContain("original-anchor-hash");
      if (outcome === "body lost") body.error(new Error("test body lost"));
      else {
        body.enqueue(
          new TextEncoder().encode(
            outcome === "invalid JSON"
              ? "bad}"
              : `${JSON.stringify({ source, partition, chain_sequence: 7, anchor_hash: "original-anchor-hash" })}}`,
          ),
        );
        body.close();
      }
      await settleUI();
      if (outcome === "success")
        expect(item.root.textContent).toContain("original-anchor-hash");
      else expect(item.root.querySelector('[tone="danger"]')).not.toBeNull();
      await vi.advanceTimersByTimeAsync(60_000);
      expectCalls(item, 1);
      submit();
      expectCalls(item, 2);
      item.requests[1].response.resolve(json("explicit-new-read"));
      await settleUI();
      expect(item.root.textContent).toContain("explicit-new-read");
    },
  );

  it.each(failures)(
    "requires a new explicit submit after $label and releases only the completed attempt",
    async ({ status }) => {
      const item = mountManifest();
      const submit = await captureSubmit(item);
      submit();
      submit();
      expectCalls(item, 1);
      failRequest(item, status);
      await settleUI();
      expect(item.root.querySelector('[tone="danger"]')).not.toBeNull();
      expect(item.root.textContent).not.toContain("bad-manifest-marker");
      await vi.advanceTimersByTimeAsync(60_000);
      expectCalls(item, 1);
      await fill(item, "new/source", "tenant:b/system");
      expectCalls(item, 1);
      submit();
      expectCalls(item, 2);
      expect(item.query).toHaveBeenLastCalledWith(
        "new/source",
        "tenant:b/system",
      );
      await settleUI();
      expect(item.root.querySelector('[tone="danger"]')).toBeNull();
      submit();
      await settleUI();
      expectCalls(item, 2);
      item.requests[1].response.resolve(
        json("explicit-new-read", "new/source", "tenant:b/system"),
      );
      await settleUI();
      expect(item.root.textContent).toContain("explicit-new-read");
      expectCalls(item, 2);
    },
  );

  it.each(["fetch", "body"])(
    "does not renew the original 30-second deadline on repeated submit during %s",
    async (phase) => {
      const item = mountManifest();
      const submit = await captureSubmit(item);
      submit();
      const request = item.requests[0];
      const body = phase === "body" ? startBody(item) : undefined;
      request.signal?.addEventListener("abort", () => {
        if (body) body.error(new Error("test body abort"));
        else request.response.reject(new Error("test fetch abort"));
      });
      await settleUI();
      await vi.advanceTimersByTimeAsync(29_999);
      submit();
      expectCalls(item, 1);
      expect(request.signal?.aborted).toBe(false);
      await vi.advanceTimersByTimeAsync(1);
      await settleUI();
      expect(request.signal?.aborted).toBe(true);
      expect(item.root.textContent).toContain("aero-id 请求超时");
      await vi.advanceTimersByTimeAsync(60_000);
      expectCalls(item, 1);
      submit();
      expectCalls(item, 2);
      expect(item.requests[1].signal?.aborted).toBe(false);
    },
  );

  it.each(["fetch", "body"])(
    "does not release a noncooperating %s merely because its deadline signal fired",
    async (phase) => {
      const item = mountManifest();
      const submit = await captureSubmit(item);
      submit();
      const request = item.requests[0];
      const body = phase === "body" ? startBody(item) : undefined;
      await settleUI();
      await vi.advanceTimersByTimeAsync(30_000);
      expect(request.signal?.aborted).toBe(true);
      submit();
      await vi.advanceTimersByTimeAsync(60_000);
      submit();
      await settleUI();
      expectCalls(item, 1);
      if (body) body.error(new Error("test late body failure"));
      else request.response.reject(new Error("test late fetch failure"));
      await settleUI();
      expect(item.root.textContent).toContain("aero-id 请求超时");
      expectCalls(item, 1);
      submit();
      expectCalls(item, 2);
    },
  );

  it("is panel-local, not a global lock or automatic coalesced result", async () => {
    const first = mountManifest();
    const submitFirst = await captureSubmit(first);
    const second = mountManifest();
    const submitSecond = await captureSubmit(second);
    submitFirst();
    submitFirst();
    submitSecond();
    submitSecond();
    expectCalls(first, 1);
    expectCalls(second, 1);
    first.requests[0].response.resolve(json("first-panel-result"));
    second.requests[0].response.resolve(json("second-panel-result"));
    await settleUI();
    expect(first.root.textContent).toContain("first-panel-result");
    expect(first.root.textContent).not.toContain("second-panel-result");
    expect(second.root.textContent).toContain("second-panel-result");
    expect(second.root.textContent).not.toContain("first-panel-result");
  });
});
