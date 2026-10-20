import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { JsonRecord } from "../types";
import { AeroIdClient } from "../api/aeroId";
import AuditAnchorsPanel from "./AuditAnchorsPanel.vue";
import { mountCommandTest, unmountCommandTests } from "./commandTestFixtures";

vi.mock("@iris-ui-kit/vue", async () => {
  const { irisCommandTestStubs } = await import("./commandTestFixtures");
  return irisCommandTestStubs();
});

const source = "aero/id";
const partition = "tenant:a/system";
const marker = "rejected-manifest-evidence-marker";
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
function manifest(
  anchorHash = marker,
  target = { source, partition },
): JsonRecord {
  return { ...target, chain_sequence: 7, anchor_hash: anchorHash };
}
function json(data: unknown): Response {
  return new Response(JSON.stringify({ data }));
}
async function settleUI(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
  await nextTick();
}
function mountManifest() {
  const requests: Array<{
    url: URL;
    response: ReturnType<typeof deferred<Response>>;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.method).toBe("GET");
      expect(init?.body).toBeUndefined();
      expect(new Headers(init?.headers).get("Idempotency-Key")).toBeNull();
      const response = deferred<Response>();
      cleanups.push(() => response.reject(new Error("test request cleanup")));
      requests.push({ url: new URL(String(input)), response });
      return response.promise;
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-session",
    fetcher,
  );
  const otherCalls = [
    vi.spyOn(client.compliance, "runAuditAnchors"),
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
    requests,
    fetcher,
    otherCalls,
  };
}
type MountedManifest = ReturnType<typeof mountManifest>;
async function fill(
  item: MountedManifest,
  target = { source, partition },
): Promise<void> {
  const inputs = item.root.querySelectorAll<HTMLInputElement>(
    ".anchor-query-grid input",
  );
  expect(inputs).toHaveLength(2);
  [target.source, target.partition].forEach((value, index) => {
    inputs[index].value = ` ${value} `;
    inputs[index].dispatchEvent(new Event("input"));
  });
  await settleUI();
}
function submit(item: MountedManifest): void {
  const form = item.root.querySelector(".anchor-query-grid");
  expect(form).not.toBeNull();
  form!.dispatchEvent(new Event("submit", { cancelable: true }));
}
function expectCalls(item: MountedManifest, count: number): void {
  expect(item.requests).toHaveLength(count);
  expect(item.fetcher).toHaveBeenCalledTimes(count);
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(Object.keys(sessionStorage)).toEqual(["original-recovery-pointer"]);
  expect(sessionStorage.getItem("original-recovery-pointer")).toBe(
    "retained-original-key",
  );
}

beforeEach(() => {
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

describe("Manifest panel rejects evidence for another query", () => {
  it.each([
    { label: "wrong source", fields: { source: "different-source" } },
    { label: "wrong partition", fields: { partition: "different-partition" } },
    { label: "missing source", fields: { source: undefined } },
    { label: "missing partition", fields: { partition: undefined } },
    { label: "source array", fields: { source: [source] } },
    { label: "partition array", fields: { partition: [partition] } },
  ])(
    "does not display $label evidence and needs an explicit original-target GET",
    async ({ fields }) => {
      const item = mountManifest();
      await fill(item);
      expectCalls(item, 0);
      submit(item);
      expectCalls(item, 1);
      item.requests[0].response.resolve(json({ ...manifest(), ...fields }));
      await settleUI();
      expect(item.root.querySelector('[tone="danger"]')).not.toBeNull();
      for (const text of [marker, "different-source", "different-partition"])
        expect(item.root.textContent).not.toContain(text);
      await vi.advanceTimersByTimeAsync(60_000);
      expectCalls(item, 1);
      expect(
        item.root
          .querySelector<HTMLInputElement>(".anchor-query-grid input")
          ?.value.trim(),
      ).toBe(source);
      submit(item);
      expectCalls(item, 2);
      expect(item.requests[1].url.href).toBe(item.requests[0].url.href);
      item.requests[1].response.resolve(
        json(manifest("explicit-original-target-result")),
      );
      await settleUI();
      expect(item.root.textContent).toContain(
        "explicit-original-target-result",
      );
      expect(item.root.textContent).not.toContain(marker);
      expect(item.root.querySelector('[tone="danger"]')).toBeNull();
      expectCalls(item, 2);
    },
  );

  it("binds a pending response to the submitted target, not subsequently edited inputs", async () => {
    const item = mountManifest();
    await fill(item);
    submit(item);
    const changed = { source: "new/source", partition: "tenant:b/system" };
    await fill(item, changed);
    submit(item);
    expectCalls(item, 1);
    item.requests[0].response.resolve(
      json(manifest("original-submitted-target")),
    );
    await settleUI();
    expect(item.root.textContent).toContain("original-submitted-target");
    expect(item.root.querySelector('[tone="danger"]')).toBeNull();
    await vi.advanceTimersByTimeAsync(60_000);
    expectCalls(item, 1);
    submit(item);
    expectCalls(item, 2);
    expect(item.requests[1].url.pathname).toBe(
      "/v1/admin/audit/anchor/manifest/new%2Fsource/tenant%3Ab%2Fsystem",
    );
    item.requests[1].response.resolve(
      json(manifest("explicit-changed-target", changed)),
    );
    await settleUI();
    expect(item.root.textContent).toContain("explicit-changed-target");
    expect(item.root.textContent).not.toContain("original-submitted-target");
  });

  it.each(["source", "partition"])(
    "rejects a response for the newly edited %s instead of its original request",
    async (field) => {
      const item = mountManifest();
      await fill(item);
      submit(item);
      const changed = { source, partition, [field]: `different-${field}` };
      await fill(item, changed);
      item.requests[0].response.resolve(json(manifest(marker, changed)));
      await settleUI();
      expect(item.root.querySelector('[tone="danger"]')).not.toBeNull();
      expect(item.root.textContent).not.toContain(marker);
      await vi.advanceTimersByTimeAsync(60_000);
      expectCalls(item, 1);
    },
  );

  it("checks identity only after the entire body, keeps single-in-flight and clears old evidence on a new read", async () => {
    const item = mountManifest();
    await fill(item);
    submit(item);
    item.requests[0].response.resolve(json(manifest("previous-read-result")));
    await settleUI();
    expect(item.root.textContent).toContain("previous-read-result");
    submit(item);
    let body!: ReadableStreamDefaultController<Uint8Array>;
    item.requests[1].response.resolve(
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            body = controller;
          },
        }),
      ),
    );
    cleanups.push(() => body.error(new Error("test body cleanup")));
    await settleUI();
    body.enqueue(new TextEncoder().encode('{"data":'));
    await settleUI();
    submit(item);
    expectCalls(item, 2);
    expect(item.root.textContent).not.toContain("previous-read-result");
    expect(item.root.querySelector('[tone="danger"]')).toBeNull();
    body.enqueue(
      new TextEncoder().encode(
        `${JSON.stringify({ ...manifest(), partition: "different-partition" })}}`,
      ),
    );
    body.close();
    await settleUI();
    expect(item.root.querySelector('[tone="danger"]')).not.toBeNull();
    expect(item.root.textContent).not.toContain(marker);
    expect(item.root.textContent).not.toContain("previous-read-result");
    await vi.advanceTimersByTimeAsync(60_000);
    expectCalls(item, 2);
  });
});
