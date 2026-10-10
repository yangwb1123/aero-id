import { describe, expect, it, vi } from "vitest";
import { AeroIdTransport } from "./transport";

function client(fetcher: typeof fetch): AeroIdTransport {
  return new AeroIdTransport(
    "https://accounts.example.test/v1",
    () => "token",
    fetcher,
  );
}

describe("transport cancellation", () => {
  it("does not send an already canceled request", async () => {
    const fetcher = vi.fn();
    const controller = new AbortController();
    controller.abort();
    await expect(
      client(fetcher as typeof fetch).request("GET", "/me", {
        signal: controller.signal,
      }),
    ).rejects.toMatchObject({ code: "request.canceled" });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("distinguishes external cancellation while reading a body from the deadline", async () => {
    const fetcher = vi.fn(
      async (_: RequestInfo | URL, init?: RequestInit) =>
        new Response(
          new ReadableStream<Uint8Array>({
            start(stream) {
              init?.signal?.addEventListener("abort", () =>
                stream.error(new DOMException("aborted", "AbortError")),
              );
            },
          }),
        ),
    );
    const controller = new AbortController();
    const request = client(fetcher as typeof fetch).request("GET", "/me", {
      signal: controller.signal,
    });
    controller.abort();
    await expect(request).rejects.toMatchObject({ code: "request.canceled" });
  });

  it("clears identity-owned reads without aborting or retrying writes", async () => {
    const requests: Array<{
      method: string;
      signal: AbortSignal;
      complete: (response: Response) => void;
    }> = [];
    const fetcher = vi.fn(
      (_: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((resolve, reject) => {
          const signal = init?.signal as AbortSignal;
          requests.push({
            method: String(init?.method),
            signal,
            complete: resolve,
          });
          signal.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    const transport = client(fetcher as typeof fetch);
    const read = transport.request("GET", "/me");
    const write = transport.request("POST", "/me/sync", {
      body: {},
      idempotencyKey: "intent",
    });
    transport.cancelPendingReads();
    await expect(read).rejects.toMatchObject({ code: "request.canceled" });
    expect(requests[0].signal.aborted).toBe(true);
    expect(requests[1].signal.aborted).toBe(false);
    requests[1].complete(new Response('{"data":{"status":"pending"}}'));
    await expect(write).resolves.toMatchObject({ data: { status: "pending" } });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});
