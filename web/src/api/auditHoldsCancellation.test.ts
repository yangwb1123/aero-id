import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ComplianceClient } from "./compliance";
import { AeroIdTransport } from "./transport";

const cleanup: Array<() => void> = [];
function json(): Response {
  return new Response('{"data":{"holds":[]}}');
}
function setup(fetcher: typeof fetch) {
  return new ComplianceClient(
    new AeroIdTransport(
      "https://accounts.example.test/v1",
      () => "test-session",
      fetcher,
    ),
  );
}
function heldRead(phase: "fetch" | "body") {
  let signal!: AbortSignal;
  let finish!: () => void;
  let settled = false;
  const fetcher = vi.fn(
    async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (!init?.signal) throw new Error("missing transport signal");
      signal = init.signal;
      if (phase === "fetch")
        return new Promise<Response>((resolve, reject) => {
          finish = () => resolve(json());
          signal.addEventListener(
            "abort",
            () => reject(new DOMException("aborted", "AbortError")),
            { once: true },
          );
        });
      return new Response(
        new ReadableStream<Uint8Array>({
          start(stream) {
            finish = () => {
              if (signal.aborted || settled) return;
              settled = true;
              stream.enqueue(new TextEncoder().encode('{"data":{"holds":[]}}'));
              stream.close();
            };
            signal.addEventListener(
              "abort",
              () => stream.error(new DOMException("aborted", "AbortError")),
              { once: true },
            );
          },
        }),
      );
    },
  );
  cleanup.push(() => finish?.());
  return { fetcher, signal: () => signal, finish: () => finish() };
}
function expectRequest(fetcher: ReturnType<typeof heldRead>["fetcher"]): void {
  expect(fetcher).toHaveBeenCalledTimes(1);
  const [input, init] = fetcher.mock.calls[0];
  expect(new URL(String(input)).pathname).toBe("/v1/admin/audit/holds");
  expect(new URL(String(input)).search).toBe("");
  expect(init?.method).toBe("GET");
  expect(init?.body).toBeUndefined();
  expect(init?.credentials).toBe("omit");
  expect(new Headers(init?.headers).get("Idempotency-Key")).toBeNull();
}

beforeEach(() => vi.useFakeTimers());
afterEach(async () => {
  cleanup.splice(0).forEach((release) => release());
  await vi.advanceTimersByTimeAsync(0);
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("Legal Hold list external read cancellation", () => {
  it("refuses a pre-aborted signal without invoking fetch", async () => {
    const fetcher = vi.fn(async () => json());
    const controller = new AbortController();
    controller.abort();
    await expect(
      setup(fetcher).listHolds(controller.signal),
    ).rejects.toMatchObject({
      code: "request.canceled",
    });
    expect(fetcher).not.toHaveBeenCalled();
  });

  describe.each(["fetch", "body"] as const)("pending %s", (phase) => {
    it("forwards external abort without retrying or mistaking it for a timeout", async () => {
      const pending = heldRead(phase);
      const controller = new AbortController();
      const result = setup(pending.fetcher)
        .listHolds(controller.signal)
        .catch((reason: unknown) => reason);
      await vi.advanceTimersByTimeAsync(0);
      expect(pending.signal().aborted).toBe(false);
      controller.abort();
      expect(pending.signal().aborted).toBe(true);
      expect(await result).toMatchObject({ code: "request.canceled" });
      await vi.advanceTimersByTimeAsync(20_000);
      expectRequest(pending.fetcher);
    });

    it("retains the original fifteen-second deadline with a live external signal", async () => {
      const pending = heldRead(phase);
      const controller = new AbortController();
      const result = setup(pending.fetcher)
        .listHolds(controller.signal)
        .catch((reason: unknown) => reason);
      await vi.advanceTimersByTimeAsync(14_999);
      expect(pending.signal().aborted).toBe(false);
      await vi.advanceTimersByTimeAsync(1);
      expect(await result).toMatchObject({ code: "network.timeout" });
      expect(pending.signal().aborted).toBe(true);
      expect(controller.signal.aborted).toBe(false);
      expectRequest(pending.fetcher);
    });

    it("detaches the external abort after successful complete-body adoption", async () => {
      const pending = heldRead(phase);
      const controller = new AbortController();
      const result = setup(pending.fetcher).listHolds(controller.signal);
      await vi.advanceTimersByTimeAsync(0);
      pending.finish();
      await expect(result).resolves.toEqual([]);
      controller.abort();
      await vi.advanceTimersByTimeAsync(20_000);
      expect(pending.signal().aborted).toBe(false);
      expectRequest(pending.fetcher);
    });
  });

  it("does not cancel another independent list request on the shared transport", async () => {
    const signals: AbortSignal[] = [];
    const finishes: Array<() => void> = [];
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((resolve, reject) => {
          if (!init?.signal) throw new Error("missing transport signal");
          const signal = init.signal;
          signals.push(signal);
          finishes.push(() => resolve(json()));
          signal.addEventListener("abort", () => reject(new Error("aborted")), {
            once: true,
          });
        }),
    );
    cleanup.push(() => finishes.forEach((finish) => finish()));
    const client = setup(fetcher);
    const first = new AbortController();
    const second = new AbortController();
    const canceled = client
      .listHolds(first.signal)
      .catch((reason: unknown) => reason);
    const current = client.listHolds(second.signal);
    first.abort();
    expect(signals[0].aborted).toBe(true);
    expect(signals[1].aborted).toBe(false);
    expect(await canceled).toMatchObject({ code: "request.canceled" });
    finishes[1]();
    await expect(current).resolves.toEqual([]);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("retains the two-MiB JSON response limit rather than weakening it for cancellation", async () => {
    const fetcher = vi.fn(
      async () =>
        new Response("", {
          headers: { "Content-Length": String(2 * 1024 * 1024 + 1) },
        }),
    );
    const controller = new AbortController();
    await expect(
      setup(fetcher).listHolds(controller.signal),
    ).rejects.toMatchObject({
      code: "response.too_large",
    });
    expect(controller.signal.aborted).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
